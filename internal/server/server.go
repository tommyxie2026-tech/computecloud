package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type session struct {
	hello    *pb.WorkerHello
	identity config.Identity
	frames   chan *pb.ServerFrame
	cancel   context.CancelFunc
}
type Server struct {
	pb.UnimplementedRuntimeServiceServer
	cfg          config.Server
	db           *store.DB
	auth         *rpcutil.Auth
	mu           sync.Mutex
	peers        map[string]*session
	notify       chan struct{}
	grpc         *grpc.Server
	jobCursor    string
	queueCursor  map[int32]string
	modelHandler http.Handler
}

func New(c config.Server) (*Server, error) {
	c.DefaultV02()
	if e := c.Validate(); e != nil {
		return nil, e
	}
	a, e := rpcutil.NewAuth(c.Users, c.Workers)
	if e != nil {
		return nil, e
	}
	d, e := store.Open(c.DataDir, store.ServerSchema)
	if e != nil {
		return nil, e
	}
	s := &Server{cfg: c, db: d, auth: a, peers: map[string]*session{}, notify: make(chan struct{}, 1), queueCursor: map[int32]string{}}
	if c.ModelGateway.Enabled {
		g, e := newModelGateway(s)
		if e != nil {
			d.Close()
			return nil, e
		}
		s.modelHandler = g
	} else {
		if _, e = d.SQL.Exec("UPDATE gateway_requests SET state='UNKNOWN',finished=?,error_code='SERVER_RESTARTED',usage_complete=0 WHERE state='STARTED'", store.Now()); e != nil {
			d.Close()
			return nil, e
		}
	}
	return s, nil
}
func (s *Server) Close() error {
	if g, ok := s.modelHandler.(*modelGateway); ok {
		g.client.CloseIdleConnections()
	}
	return s.db.Close()
}
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tls, e := rpcutil.ServerTLS(l.Addr().String(), s.cfg.TLS)
	if e != nil {
		return e
	}
	var httpDone chan error
	if s.cfg.HTTP.Listen != "" {
		hl, e := net.Listen("tcp", s.cfg.HTTP.Listen)
		if e != nil {
			return e
		}
		defer hl.Close()
		httpDone = make(chan error, 1)
		go func() {
			e := s.serveHTTP(ctx, hl)
			httpDone <- e
			if e != nil {
				cancel()
			}
		}()
	}
	g := grpc.NewServer(tls, grpc.UnaryInterceptor(s.auth.Unary), grpc.StreamInterceptor(s.auth.Stream), grpc.MaxRecvMsgSize(8<<20), grpc.MaxSendMsgSize(8<<20))
	s.grpc = g
	pb.RegisterRuntimeServiceServer(g, s)
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx) }()
	go func() { <-ctx.Done(); g.Stop() }()
	e = g.Serve(l)
	stopping := ctx.Err() != nil
	cancel()
	<-done
	if httpDone != nil {
		if he := <-httpDone; he != nil {
			return he
		}
	}
	if errors.Is(e, grpc.ErrServerStopped) || stopping {
		return nil
	}
	return e
}
func (s *Server) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
func wait(ctx context.Context, ms int) error {
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (s *Server) ConnectWorker(stream grpc.BidiStreamingServer[pb.WorkerFrame, pb.ServerFrame]) error {
	p, e := rpcutil.Worker(stream.Context())
	if e != nil {
		return e
	}
	first, e := stream.Recv()
	if e != nil {
		return e
	}
	h := first.GetHello()
	if h == nil || h.WorkerId != p.Identity.WorkerID || h.Epoch == "" || h.Slots < 1 || h.Slots > 64 || len(h.Runtimes) > 16 {
		return status.Error(codes.InvalidArgument, "invalid worker registration")
	}
	for _, r := range h.Runtimes {
		for _, c := range r.Credentials {
			if !config.Contains(p.Identity.Credentials, c) {
				return status.Error(codes.PermissionDenied, "credential not authorized for worker")
			}
		}
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	peer := &session{hello: h, identity: p.Identity, frames: make(chan *pb.ServerFrame, 64), cancel: cancel}
	if _, e = s.db.SQL.ExecContext(ctx, "INSERT INTO workers VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,hello=excluded.hello,seen=excluded.seen", h.WorkerId, h.Epoch, encode(h), store.Now()); e != nil {
		return dbErr(e)
	}
	s.mu.Lock()
	if old := s.peers[h.WorkerId]; old != nil {
		old.cancel()
	}
	s.peers[h.WorkerId] = peer
	s.mu.Unlock()
	s.wake()
	defer func() {
		s.mu.Lock()
		if s.peers[h.WorkerId] == peer {
			delete(s.peers, h.WorkerId)
		}
		s.mu.Unlock()
	}()
	recvErr := make(chan error, 1)
	go func() {
		for {
			f, e := stream.Recv()
			if e != nil {
				recvErr <- e
				return
			}
			if e = s.receive(ctx, peer, f); e != nil {
				recvErr <- e
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-recvErr:
			if e == io.EOF {
				return nil
			}
			return e
		case f := <-peer.frames:
			if e = stream.Send(f); e != nil {
				return e
			}
		case <-ticker.C:
			rows, e := s.db.SQL.QueryContext(ctx, "SELECT body FROM commands WHERE worker=? AND acked=0 ORDER BY CASE kind WHEN 'stop' THEN 0 ELSE 1 END,id LIMIT 32", h.WorkerId)
			if e != nil {
				return dbErr(e)
			}
			var cmds []*pb.Command
			for rows.Next() {
				var b []byte
				if e = rows.Scan(&b); e != nil {
					break
				}
				c := new(pb.Command)
				if e = decode(b, c); e != nil {
					break
				}
				cmds = append(cmds, c)
			}
			re := rows.Err()
			rows.Close()
			if e != nil {
				return dbErr(e)
			}
			if re != nil {
				return dbErr(re)
			}
			for _, c := range cmds {
				deliver := false
				e = s.db.Tx(ctx, func(q store.Query) error {
					var e error
					deliver, e = commandDeliverable(ctx, q, c, h.WorkerId, h.Epoch)
					if e != nil {
						return e
					}
					if !deliver {
						_, e = q.ExecContext(ctx, "UPDATE commands SET acked=1 WHERE id=? AND worker=? AND acked=0", c.CommandId, h.WorkerId)
					}
					return e
				})
				if e != nil {
					return dbErr(e)
				}
				if !deliver {
					continue
				}
				if c.Kind == "start" && c.Assignment.GetGateway() != nil {
					c.Assignment.Gateway.Token = modelAttemptToken(c.Assignment)
				}
				if e = stream.Send(&pb.ServerFrame{Body: &pb.ServerFrame_Command{Command: c}}); e != nil {
					return e
				}
			}
		}
	}
}
func commandDeliverable(ctx context.Context, q store.Query, c *pb.Command, worker, epoch string) (bool, error) {
	if c == nil || c.Assignment == nil || (c.Kind != "start" && c.Kind != "stop") {
		return false, nil
	}
	a := c.Assignment
	var n int
	e := q.QueryRowContext(ctx, `SELECT count(*)
		FROM attempts x
		JOIN tasks t ON t.id=x.task
		WHERE x.id=? AND x.task=? AND x.worker=? AND x.epoch=?
		  AND x.generation=? AND x.token=? AND x.released=0
		  AND t.attempt=x.id AND t.current_generation=x.generation`,
		a.AttemptId, a.TaskId, worker, epoch, a.Generation, a.LeaseToken,
	).Scan(&n)
	return n == 1, e
}

func (s *Server) receive(ctx context.Context, p *session, f *pb.WorkerFrame) error {
	s.mu.Lock()
	current := s.peers[p.hello.WorkerId] == p
	s.mu.Unlock()
	if !current || ctx.Err() != nil {
		return status.Error(codes.FailedPrecondition, "STALE_CONNECTION")
	}
	if a := f.GetAck(); a != nil {
		if a.State != "RECEIVED" {
			return status.Error(codes.InvalidArgument, "invalid command ACK")
		}
		_, e := s.db.SQL.ExecContext(ctx, "UPDATE commands SET acked=1 WHERE id=? AND worker=?", a.CommandId, p.hello.WorkerId)
		return dbErr(e)
	}
	r := f.GetRenew()
	if r == nil {
		return status.Error(codes.InvalidArgument, "expected renew or ACK")
	}
	if len(r.Attempts) > 64 {
		return status.Error(codes.ResourceExhausted, "too many attempts")
	}
	if _, e := s.db.SQL.ExecContext(ctx, "UPDATE workers SET seen=? WHERE id=? AND epoch=?", store.Now(), p.hello.WorkerId, p.hello.Epoch); e != nil {
		return dbErr(e)
	}
	for _, ref := range r.Attempts {
		grant := &pb.LeaseGrant{AttemptId: ref.AttemptId, TtlMs: int64(s.cfg.LeaseSeconds) * 1000}
		e := s.db.Tx(ctx, func(q store.Query) error {
			a, e := s.checkAttempt(ctx, q, p.hello.WorkerId, ref)
			if e != nil {
				return nil
			}
			var state string
			if e = q.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id=?", a.task).Scan(&state); e != nil {
				return e
			}
			if a.released || a.until <= store.Now() || state == "CANCELING" || state == "RECONCILING" || terminal(state) {
				return nil
			}
			allowed, e := s.jobAllowsExecution(ctx, q, a.task)
			if e != nil {
				return e
			}
			if !allowed {
				return nil
			}
			_, e = q.ExecContext(ctx, "UPDATE attempts SET lease_until=? WHERE id=?", store.Now()+grant.TtlMs, ref.AttemptId)
			grant.Valid = e == nil
			return e
		})
		if e != nil {
			return dbErr(e)
		}
		select {
		case p.frames <- &pb.ServerFrame{Body: &pb.ServerFrame_Lease{Lease: grant}}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type attemptRow struct {
	task, worker, token                 string
	epoch, currentAttempt, currentEpoch string
	generation, currentGeneration       int64
	until, seq                          int64
	released                            bool
	finalHash                           string
}

func (s *Server) readAttemptIdentity(ctx context.Context, q store.Query, worker string, r *pb.AttemptRef) (attemptRow, error) {
	var a attemptRow
	if r == nil {
		return a, status.Error(codes.InvalidArgument, "attempt required")
	}
	e := q.QueryRowContext(ctx, `SELECT a.task,a.worker,a.epoch,a.generation,a.token,a.lease_until,a.released,a.worker_seq,a.final_hash,
		t.attempt,t.current_generation,coalesce(w.epoch,'')
		FROM attempts a
		JOIN tasks t ON t.id=a.task
		LEFT JOIN workers w ON w.id=a.worker
		WHERE a.id=?`, r.AttemptId).Scan(
		&a.task, &a.worker, &a.epoch, &a.generation, &a.token, &a.until, &a.released, &a.seq, &a.finalHash,
		&a.currentAttempt, &a.currentGeneration, &a.currentEpoch,
	)
	if e != nil {
		return a, e
	}
	if a.worker != worker ||
		a.generation != r.Generation ||
		a.token != r.LeaseToken ||
		a.currentAttempt != r.AttemptId ||
		a.currentGeneration != r.Generation {
		return a, status.Error(codes.FailedPrecondition, "STALE_ATTEMPT")
	}
	return a, nil
}

func (s *Server) checkAttempt(ctx context.Context, q store.Query, worker string, r *pb.AttemptRef) (attemptRow, error) {
	a, e := s.readAttemptIdentity(ctx, q, worker, r)
	if e != nil {
		return a, e
	}
	if a.currentEpoch == "" || a.epoch != a.currentEpoch {
		return a, status.Error(codes.FailedPrecondition, "STALE_ATTEMPT")
	}
	return a, nil
}
func (s *Server) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(s.cfg.TickMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.notify:
		}
		if e := s.tick(ctx); e != nil && ctx.Err() == nil {
			slog.Error("scheduler step failed", "error", e)
		}
	}
}
func (s *Server) tick(ctx context.Context) error {
	if e := s.reconcile(ctx); e != nil {
		return e
	}
	if e := s.advanceJobs(ctx); e != nil {
		return e
	}
	if s.cfg.Maintenance {
		return nil
	}
	s.mu.Lock()
	var peers []*session
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.Unlock()
	sort.Slice(peers, func(i, j int) bool { return peers[i].hello.WorkerId < peers[j].hello.WorkerId })
	return s.scheduleQueued(ctx, peers)
}
func fits(p *session, t *pb.Task) bool {
	if !config.Contains(p.identity.Projects, t.Spec.ProjectId) {
		return false
	}
	for _, r := range p.hello.Runtimes {
		if r.Profile != t.Spec.RuntimeProfile || !config.Contains(r.Models, t.Spec.Model) || !config.Contains(r.Credentials, t.Spec.CredentialRef) || !config.Contains(r.Repositories, t.Spec.Workspace.RepositoryRef) || !config.Contains(r.Policies, t.Spec.PolicyRef) || !config.Contains(r.Verifiers, t.Spec.AcceptanceProfile) {
			continue
		}
		good := true
		for _, c := range t.Spec.RequiredCapabilities {
			good = good && config.Contains(r.Capabilities, c)
		}
		if good {
			return true
		}
	}
	return false
}
func (s *Server) assign(ctx context.Context, id string, peers []*session) error {
	return s.db.Tx(ctx, func(q store.Query) error {
		t, _, e := readTask(ctx, q, id)
		if e != nil {
			return e
		}
		if t.State != "QUEUED" {
			return nil
		}
		jc, j, e := s.assignmentJob(ctx, q, id)
		if e != nil {
			return e
		}
		if j != nil {
			if j.StopReason != "" || terminal(j.State) || j.State == "RECONCILING" || j.Deadline <= store.Now() {
				return nil
			}
			if route := j.frozen.Routes[t.Spec.CredentialRef]; route != "" && (!s.cfg.ModelGateway.Enabled || j.frozen.RouteDigests[route] != config.RouteDigest(s.cfg.ModelGateway.Routes[route])) {
				_, e = q.ExecContext(ctx, "UPDATE tasks SET blocker='GATEWAY_ROUTE_CHANGED' WHERE id=?", id)
				return e
			}
			var active int
			if e = q.QueryRowContext(ctx, "SELECT count(*) FROM attempts a JOIN tasks t ON a.task=t.id WHERE t.job_id=? AND a.released=0", j.ID).Scan(&active); e != nil {
				return e
			}
			if active >= j.parallelism {
				_, e = q.ExecContext(ctx, "UPDATE tasks SET blocker='JOB_CAPACITY_EXHAUSTED' WHERE id=?", id)
				return e
			}
		}
		var deadline, retryAfter int64
		if e = q.QueryRowContext(ctx, "SELECT deadline,retry_after FROM tasks WHERE id=?", id).Scan(&deadline, &retryAfter); e != nil {
			return e
		}
		now := store.Now()
		if deadline <= now {
			return setState(ctx, q, id, "FAILED", "DEADLINE_EXCEEDED", "deadline reached before dispatch")
		}
		if retryAfter > now {
			return nil
		}
		var creds, projects int
		if e = q.QueryRowContext(ctx, "SELECT count(*) FROM attempts a JOIN tasks t ON a.task=t.id WHERE a.released=0 AND json_extract(t.spec,'$.credential_ref')=?", t.Spec.CredentialRef).Scan(&creds); e != nil {
			return e
		}
		if e = q.QueryRowContext(ctx, "SELECT count(*) FROM attempts a JOIN tasks t ON a.task=t.id WHERE a.released=0 AND t.project=?", t.Spec.ProjectId).Scan(&projects); e != nil {
			return e
		}
		blocker := "NO_READY_WORKER"
		var chosen *session
		load := int(^uint(0) >> 1)
		if creds >= s.cfg.Credentials[t.Spec.CredentialRef] || projects >= s.cfg.MaxProjectTasks {
			blocker = "CAPACITY_EXHAUSTED"
		} else {
			for _, p := range peers {
				if !fits(p, t) {
					continue
				}
				if jc != nil && !fitsJob(p, t, jc) {
					blocker = "TEMPLATE_OR_CAPABILITY_MISMATCH"
					continue
				}
				var active int
				var seen int64
				if e = q.QueryRowContext(ctx, "SELECT seen FROM workers WHERE id=?", p.hello.WorkerId).Scan(&seen); e != nil {
					return e
				}
				if store.Now()-seen > int64(s.cfg.LeaseSeconds)*1000 {
					continue
				}
				if e = q.QueryRowContext(ctx, "SELECT count(*) FROM attempts WHERE worker=? AND released=0", p.hello.WorkerId).Scan(&active); e != nil {
					return e
				}
				if active >= int(p.hello.Slots) {
					blocker = "CAPACITY_EXHAUSTED"
					continue
				}
				if active < load {
					chosen = p
					load = active
				}
			}
		}
		if chosen == nil {
			_, e = q.ExecContext(ctx, "UPDATE tasks SET blocker=? WHERE id=?", blocker, id)
			return e
		}
		var currentGeneration int64
		if e = q.QueryRowContext(ctx, "SELECT current_generation FROM tasks WHERE id=?", id).Scan(&currentGeneration); e != nil {
			return e
		}
		nextGeneration := currentGeneration + 1
		a := &pb.Assignment{TaskId: id, AttemptId: store.ID(), Generation: nextGeneration, LeaseToken: store.ID() + store.ID(), LeaseTtlMs: int64(s.cfg.LeaseSeconds) * 1000, DeadlineMs: deadline, Spec: t.Spec, Job: jc}
		if _, e = q.ExecContext(ctx, "INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until) VALUES(?,?,?,?,?,?,?)", a.AttemptId, id, chosen.hello.WorkerId, chosen.hello.Epoch, nextGeneration, a.LeaseToken, store.Now()+a.LeaseTtlMs); e != nil {
			return e
		}
		if e = s.bindGateway(ctx, q, a, j); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, "UPDATE tasks SET attempt=?,worker=?,current_generation=?,blocker='',retry_after=0 WHERE id=?", a.AttemptId, chosen.hello.WorkerId, nextGeneration, id); e != nil {
			return e
		}
		if e = setState(ctx, q, id, "STARTING", "", ""); e != nil {
			return e
		}
		if e = setTaskStageState(ctx, q, id, "RUNNING"); e != nil {
			return e
		}
		if j != nil && j.State == "QUEUED" {
			next := "MAPPING"
			if j.Mode == "single" {
				next = "EXECUTING"
			}
			if e = jobState(ctx, q, j, next, "", ""); e != nil {
				return e
			}
		}
		return saveCommand(ctx, q, chosen.hello.WorkerId, "start", a)
	})
}
func saveCommand(ctx context.Context, q store.Query, worker, kind string, a *pb.Assignment) error {
	c := &pb.Command{CommandId: store.ID(), Kind: kind, Assignment: a}
	_, e := q.ExecContext(ctx, "INSERT INTO commands(id,task,attempt,worker,kind,body) VALUES(?,?,?,?,?,?)", c.CommandId, a.TaskId, a.AttemptId, worker, kind, encode(c))
	return e
}
func (s *Server) stopCommand(ctx context.Context, q store.Query, t *pb.Task) error {
	var exists int
	if e := q.QueryRowContext(ctx, "SELECT count(*) FROM commands WHERE attempt=? AND kind='stop'", t.AttemptId).Scan(&exists); e != nil {
		return e
	}
	if exists > 0 {
		return nil
	}
	a := &pb.Assignment{TaskId: t.TaskId, AttemptId: t.AttemptId}
	if e := q.QueryRowContext(ctx, "SELECT generation,token FROM attempts WHERE id=?", t.AttemptId).Scan(&a.Generation, &a.LeaseToken); e != nil {
		return e
	}
	return saveCommand(ctx, q, t.WorkerId, "stop", a)
}
func (s *Server) reconcile(ctx context.Context) error {
	return s.db.Tx(ctx, func(q store.Query) error {
		rows, e := q.QueryContext(ctx, "SELECT t.id,t.deadline,a.lease_until FROM tasks t JOIN attempts a ON a.id=t.attempt WHERE a.released=0 AND t.state NOT IN ('SUCCEEDED','FAILED','CANCELED')")
		if e != nil {
			return e
		}
		type item struct {
			id              string
			deadline, lease int64
		}
		var list []item
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.deadline, &v.lease); e != nil {
				break
			}
			list = append(list, v)
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if re != nil {
			return re
		}
		for _, v := range list {
			t, _, e := readTask(ctx, q, v.id)
			if e != nil {
				return e
			}
			if t.State == "CANCELING" {
				continue
			}
			if v.deadline <= store.Now() {
				if e = setState(ctx, q, t.TaskId, "CANCELING", "DEADLINE_EXCEEDED", "deadline reached"); e != nil {
					return e
				}
				if e = s.stopCommand(ctx, q, t); e != nil {
					return e
				}
			} else if v.lease <= store.Now() {
				if e = setState(ctx, q, t.TaskId, "RECONCILING", "WORKER_LOST", "awaiting cleanup evidence"); e != nil {
					return e
				}
				if e = s.stopCommand(ctx, q, t); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
func (s *Server) ListWorkers(ctx context.Context, _ *pb.Empty) (*pb.Workers, error) {
	if _, e := rpcutil.User(ctx); e != nil {
		return nil, e
	}
	rows, e := s.db.SQL.QueryContext(ctx, "SELECT w.id,w.hello,w.seen,(SELECT count(*) FROM attempts a WHERE a.worker=w.id AND a.released=0) FROM workers w ORDER BY w.id")
	if e != nil {
		return nil, dbErr(e)
	}
	defer rows.Close()
	out := new(pb.Workers)
	for rows.Next() {
		w := new(pb.WorkerStatus)
		var b []byte
		if e = rows.Scan(&w.WorkerId, &b, &w.LastSeenMs, &w.Active); e != nil {
			return nil, dbErr(e)
		}
		h := new(pb.WorkerHello)
		if e = decode(b, h); e != nil {
			return nil, dbErr(e)
		}
		w.Slots = h.Slots
		w.Runtimes = h.Runtimes
		s.mu.Lock()
		w.Online = s.peers[w.WorkerId] != nil
		s.mu.Unlock()
		w.Online = w.Online && store.Now()-w.LastSeenMs < int64(s.cfg.LeaseSeconds)*1000
		out.Workers = append(out.Workers, w)
	}
	return out, dbErr(rows.Err())
}
