package server

import (
	"context"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func fitsJob(p *session, t *pb.Task, jc *pb.JobExecution) bool {
	key := job.TemplateKey(job.Execution{RuntimeProfile: t.Spec.RuntimeProfile, PolicyRef: t.Spec.PolicyRef, AcceptanceProfile: t.Spec.AcceptanceProfile})
	for _, r := range p.hello.Runtimes {
		if r.Profile == t.Spec.RuntimeProfile && r.TemplateDigests[key] == jc.TemplateDigest {
			return true
		}
	}
	return false
}

// One assignment per group per pass; a rotating cursor prevents an older Job
// from repeatedly winning a single newly available account slot.
func (s *Server) scheduleQueued(ctx context.Context, peers []*session) error {
	// At most 64 groups per priority and 32 queued children per group enter memory.
	// With no dispatch, advance the scan so an unserviceable page cannot starve later jobs.
	for priority := int32(10); priority >= 0; priority-- {
		now := store.Now()
		rows, e := s.db.SQL.QueryContext(ctx, `SELECT coalesce(job_id,id) AS grp FROM tasks WHERE state='QUEUED' AND priority=? AND retry_after<=? GROUP BY grp ORDER BY (grp<=?),grp LIMIT 64`, priority, now, s.queueCursor[priority])
		if e != nil {
			return e
		}
		var groups []string
		for rows.Next() {
			var group string
			if e = rows.Scan(&group); e != nil {
				break
			}
			groups = append(groups, group)
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if re != nil {
			return re
		}
		assigned := false
		for _, group := range groups {
			rows, e = s.db.SQL.QueryContext(ctx, `SELECT id FROM tasks WHERE state='QUEUED' AND priority=? AND retry_after<=? AND (job_id=? OR (job_id IS NULL AND id=?)) ORDER BY created,id LIMIT 32`, priority, store.Now(), group, group)
			if e != nil {
				return e
			}
			var ids []string
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					break
				}
				ids = append(ids, id)
			}
			re = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if re != nil {
				return re
			}
			for _, id := range ids {
				if e = s.assign(ctx, id, peers); e != nil {
					return e
				}
				var state string
				if e = s.db.SQL.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id=?", id).Scan(&state); e != nil {
					return e
				}
				if state == "STARTING" {
					s.queueCursor[priority] = group
					assigned = true
					break
				}
			}
		}
		if !assigned && len(groups) > 0 {
			s.queueCursor[priority] = groups[len(groups)-1]
		}
	}
	return nil
}
