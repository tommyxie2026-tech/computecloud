package server

import (
	"context"
	"fmt"
	"sort"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxSchedulerGroups = 512

func fitsJob(p *session, t *pb.Task, jc *pb.JobExecution) bool {
	key := job.TemplateKey(job.Execution{RuntimeProfile: t.Spec.RuntimeProfile, PolicyRef: t.Spec.PolicyRef, AcceptanceProfile: t.Spec.AcceptanceProfile})
	for _, r := range p.hello.Runtimes {
		if r.Profile == t.Spec.RuntimeProfile && r.TemplateDigests[key] == jc.TemplateDigest {
			return true
		}
	}
	return false
}

type queueGroup struct {
	project   string
	group     string
	base      int32
	effective int32
	oldest    int64
}

func effectivePriority(base int32, queuedAt, now, agingMS int64) int32 {
	if base < 0 {
		base = 0
	}
	if base > 10 {
		base = 10
	}
	if agingMS <= 0 || queuedAt <= 0 || now <= queuedAt {
		return base
	}
	boost := int32((now - queuedAt) / agingMS)
	if boost > 10-base {
		boost = 10 - base
	}
	return base + boost
}

func rotateStrings(values []string, after string) []string {
	if len(values) < 2 || after == "" {
		return values
	}
	i := sort.SearchStrings(values, after)
	for i < len(values) && values[i] <= after {
		i++
	}
	if i == 0 || i >= len(values) {
		if i >= len(values) {
			return append(append([]string(nil), values...), nil...)
		}
		return values
	}
	out := append([]string(nil), values[i:]...)
	out = append(out, values[:i]...)
	return out
}

func rotateGroups(values []queueGroup, after string) []queueGroup {
	if len(values) < 2 || after == "" {
		return values
	}
	i := sort.Search(len(values), func(i int) bool { return values[i].group > after })
	if i <= 0 || i >= len(values) {
		if i >= len(values) {
			return append([]queueGroup(nil), values...)
		}
		return values
	}
	out := append([]queueGroup(nil), values[i:]...)
	out = append(out, values[:i]...)
	return out
}

func (s *Server) queuedGroups(ctx context.Context, now int64) ([]queueGroup, error) {
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT project,coalesce(job_id,id) AS grp,max(priority),min(updated)
		FROM tasks
		WHERE state='QUEUED' AND retry_after<=? AND deadline>?
		GROUP BY project,grp
		ORDER BY min(updated),project,grp
		LIMIT ?`, now, now, maxSchedulerGroups)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]queueGroup, 0, 64)
	agingMS := s.cfg.Jobs.SchedulerAgingSeconds * 1000
	for rows.Next() {
		var v queueGroup
		if err = rows.Scan(&v.project, &v.group, &v.base, &v.oldest); err != nil {
			return nil, err
		}
		v.effective = effectivePriority(v.base, v.oldest, now, agingMS)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Server) dispatchGroup(ctx context.Context, g queueGroup, peers []*session) (bool, error) {
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT id
		FROM tasks
		WHERE state='QUEUED' AND retry_after<=? AND deadline>?
		  AND project=? AND (job_id=? OR (job_id IS NULL AND id=?))
		ORDER BY priority DESC,updated,created,id
		LIMIT 32`, store.Now(), store.Now(), g.project, g.group, g.group)
	if err != nil {
		return false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if rowErr != nil {
		return false, rowErr
	}
	for _, id := range ids {
		if err = s.assign(ctx, id, peers); err != nil {
			return false, err
		}
		var state string
		if err = s.db.SQL.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id=?", id).Scan(&state); err != nil {
			return false, err
		}
		if state == "STARTING" {
			s.groupCursor[fmt.Sprintf("%d:%s", g.effective, g.project)] = g.group
			return true, nil
		}
	}
	return false, nil
}

// scheduleQueued performs bounded hierarchical fair queuing:
//
//   effective priority (base priority + age boost)
//       -> round-robin project
//           -> round-robin Job/group
//               -> oldest runnable Task
//
// Hard execution constraints remain inside assign(). Aging affects ordering only;
// it never bypasses retry_after, deadline, permissions, capabilities or quotas.
func (s *Server) scheduleQueued(ctx context.Context, peers []*session) error {
	now := store.Now()
	groups, err := s.queuedGroups(ctx, now)
	if err != nil {
		return err
	}
	buckets := map[int32]map[string][]queueGroup{}
	for _, g := range groups {
		if buckets[g.effective] == nil {
			buckets[g.effective] = map[string][]queueGroup{}
		}
		buckets[g.effective][g.project] = append(buckets[g.effective][g.project], g)
	}
	for priority := int32(10); priority >= 0; priority-- {
		projects := buckets[priority]
		if len(projects) == 0 {
			continue
		}
		names := make([]string, 0, len(projects))
		for project := range projects {
			names = append(names, project)
			sort.Slice(projects[project], func(i, j int) bool {
				if projects[project][i].oldest != projects[project][j].oldest {
					return projects[project][i].oldest < projects[project][j].oldest
				}
				return projects[project][i].group < projects[project][j].group
			})
			key := fmt.Sprintf("%d:%s", priority, project)
			projects[project] = rotateGroups(projects[project], s.groupCursor[key])
		}
		sort.Strings(names)
		names = rotateStrings(names, s.projectCursor[priority])

		remaining := true
		for remaining {
			remaining = false
			for _, project := range names {
				queue := projects[project]
				if len(queue) == 0 {
					continue
				}
				remaining = true
				g := queue[0]
				projects[project] = queue[1:]
				assigned, err := s.dispatchGroup(ctx, g, peers)
				if err != nil {
					return err
				}
				if assigned {
					s.projectCursor[priority] = project
				}
			}
		}
	}
	return nil
}

func (s *Server) checkQueueAdmission(ctx context.Context, q store.Query, project string, additional int) error {
	if additional < 1 {
		return nil
	}
	var total, projectTotal int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE state='QUEUED'").Scan(&total); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE state='QUEUED' AND project=?", project).Scan(&projectTotal); err != nil {
		return err
	}
	if total+additional > s.cfg.Jobs.MaxQueuedTasks {
		return status.Error(codes.ResourceExhausted, "QUEUE_BACKPRESSURE_GLOBAL")
	}
	if projectTotal+additional > s.cfg.Jobs.MaxQueuedTasksPerProject {
		return status.Error(codes.ResourceExhausted, "QUEUE_BACKPRESSURE_PROJECT")
	}
	return nil
}
