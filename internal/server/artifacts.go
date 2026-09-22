package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var artifactID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Server) UploadArtifact(stream grpc.ClientStreamingServer[pb.ArtifactChunk, pb.Artifact]) error {
	p, e := rpcutil.Worker(stream.Context())
	if e != nil {
		return e
	}
	first, e := stream.Recv()
	if e != nil {
		return e
	}
	m := first.Metadata
	if m == nil || first.Attempt == nil || !artifactID.MatchString(m.ArtifactId) || m.Size < 0 || m.Size > s.cfg.MaxArtifactBytes || len(m.Sha256) != 64 {
		return status.Error(codes.InvalidArgument, "invalid artifact metadata")
	}
	a, e := s.checkAttempt(stream.Context(), s.db.SQL, p.Identity.WorkerID, first.Attempt)
	if e != nil {
		return dbErr(e)
	}
	if m.TaskId != a.task || m.AttemptId != first.Attempt.AttemptId || a.released {
		return status.Error(codes.FailedPrecondition, "artifact attempt mismatch or released")
	}
	dir := filepath.Join(s.cfg.DataDir, "artifacts")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return status.Error(codes.Unavailable, "artifact storage unavailable")
	}
	f, e := os.CreateTemp(dir, ".upload-")
	if e != nil {
		return status.Error(codes.Unavailable, "artifact storage unavailable")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	var size int64
	write := func(b []byte) error {
		size += int64(len(b))
		if len(b) > 256<<10 || size > m.Size {
			return status.Error(codes.ResourceExhausted, "artifact size limit")
		}
		_, e = io.MultiWriter(f, h).Write(b)
		return e
	}
	if e = write(first.Data); e != nil {
		return e
	}
	for {
		chunk, e := stream.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if chunk.Metadata != nil || chunk.Attempt != nil {
			return status.Error(codes.InvalidArgument, "metadata only in first chunk")
		}
		if e = write(chunk.Data); e != nil {
			return e
		}
	}
	if size != m.Size || hex.EncodeToString(h.Sum(nil)) != m.Sha256 {
		return status.Error(codes.InvalidArgument, "artifact size/hash mismatch")
	}
	if e = f.Sync(); e != nil {
		return status.Error(codes.Unavailable, "artifact sync failed")
	}
	if e = f.Close(); e != nil {
		return e
	}
	// Linking publishes without overwriting an existing artifact ID.
	final := filepath.Join(dir, m.ArtifactId)
	if e = os.Link(f.Name(), final); e != nil {
		if !errors.Is(e, os.ErrExist) {
			return status.Error(codes.Unavailable, "artifact publish failed")
		}
		existing, e := os.Open(final)
		if e != nil {
			return e
		}
		check := sha256.New()
		n, e := io.Copy(check, existing)
		existing.Close()
		if e != nil || n != m.Size || hex.EncodeToString(check.Sum(nil)) != m.Sha256 {
			return status.Error(codes.AlreadyExists, "artifact ID conflict")
		}
	}
	df, e := os.Open(dir)
	if e != nil {
		return e
	}
	e = df.Sync()
	df.Close()
	if e != nil {
		return e
	}
	e = s.db.Tx(stream.Context(), func(q store.Query) error {
		current, e := s.checkAttempt(stream.Context(), q, p.Identity.WorkerID, first.Attempt)
		if e != nil {
			return e
		}
		if current.released {
			return status.Error(codes.FailedPrecondition, "attempt already completed")
		}
		_, e = q.ExecContext(stream.Context(), "INSERT INTO artifacts(id,task,attempt,kind,hash,size,path) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING", m.ArtifactId, m.TaskId, m.AttemptId, m.Kind, m.Sha256, m.Size, m.ArtifactId)
		if e != nil {
			return e
		}
		var task, attempt, kind, hash string
		var n int64
		if e = q.QueryRowContext(stream.Context(), "SELECT task,attempt,kind,hash,size FROM artifacts WHERE id=?", m.ArtifactId).Scan(&task, &attempt, &kind, &hash, &n); e != nil {
			return e
		}
		if task != m.TaskId || attempt != m.AttemptId || kind != m.Kind || hash != m.Sha256 || n != m.Size {
			return status.Error(codes.AlreadyExists, "artifact metadata conflict")
		}
		return nil
	})
	if e != nil {
		return dbErr(e)
	}
	return stream.SendAndClose(m)
}
func (s *Server) ListArtifacts(ctx context.Context, r *pb.TaskRef) (*pb.Artifacts, error) {
	if _, e := s.authorized(ctx, r.TaskId); e != nil {
		return nil, e
	}
	rows, e := s.db.SQL.QueryContext(ctx, "SELECT id,task,attempt,kind,hash,size FROM artifacts WHERE task=? ORDER BY id", r.TaskId)
	if e != nil {
		return nil, dbErr(e)
	}
	defer rows.Close()
	out := new(pb.Artifacts)
	for rows.Next() {
		a := new(pb.Artifact)
		if e = rows.Scan(&a.ArtifactId, &a.TaskId, &a.AttemptId, &a.Kind, &a.Sha256, &a.Size); e != nil {
			return nil, dbErr(e)
		}
		out.Artifacts = append(out.Artifacts, a)
	}
	return out, dbErr(rows.Err())
}
func (s *Server) DownloadArtifact(r *pb.ArtifactRef, stream grpc.ServerStreamingServer[pb.Chunk]) error {
	if _, e := s.authorized(stream.Context(), r.TaskId); e != nil {
		return e
	}
	var path string
	if e := s.db.SQL.QueryRowContext(stream.Context(), "SELECT path FROM artifacts WHERE task=? AND id=?", r.TaskId, r.ArtifactId).Scan(&path); e != nil {
		return dbErr(e)
	}
	if !artifactID.MatchString(path) {
		return status.Error(codes.Internal, "invalid stored artifact path")
	}
	f, e := os.Open(filepath.Join(s.cfg.DataDir, "artifacts", path))
	if e != nil {
		return status.Error(codes.NotFound, "artifact file missing")
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	for {
		n, e := f.Read(buf)
		if n > 0 {
			if se := stream.Send(&pb.Chunk{Data: buf[:n]}); se != nil {
				return se
			}
		}
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
	}
}
