package server

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) inputArtifact(ctx context.Context, worker string, r *pb.InputArtifactRequest) (string, error) {
	var path string
	e := s.db.Tx(ctx, func(q store.Query) error {
		a, e := s.checkAttempt(ctx, q, worker, r.Attempt)
		if e != nil {
			return e
		}
		allowed, e := s.jobAllowsExecution(ctx, q, a.task)
		if e != nil {
			return e
		}
		if a.released || a.until <= store.Now() || !allowed {
			return status.Error(codes.FailedPrecondition, "input execution lease not valid")
		}
		jc, j, e := s.assignmentJob(ctx, q, a.task)
		if e != nil {
			return e
		}
		if jc == nil || jc.Stage != "reduce" {
			return status.Error(codes.PermissionDenied, "only Reduce may download inputs")
		}
		var manifest job.Manifest
		if e = json.Unmarshal(j.manifest, &manifest); e != nil {
			return e
		}
		if job.Hash(j.manifest) != j.manifestHash {
			return status.Error(codes.FailedPrecondition, "manifest corrupt")
		}
		var found *job.ManifestItem
		for i := range manifest.Items {
			if manifest.Items[i].ArtifactID == r.ArtifactId {
				found = &manifest.Items[i]
				break
			}
		}
		if found == nil {
			return status.Error(codes.NotFound, "input artifact not authorized")
		}
		var hash string
		var size int64
		e = q.QueryRowContext(ctx, `SELECT a.path,a.hash,a.size FROM artifacts a JOIN tasks t ON a.task=t.id JOIN attempts x ON a.attempt=x.id WHERE a.id=? AND a.task=? AND a.attempt=? AND t.job_id=? AND t.owner=? AND t.project=? AND t.state='SUCCEEDED' AND t.attempt=x.id AND t.current_generation=x.generation AND a.generation=x.generation AND a.state='ACCEPTED' AND x.released=1`, r.ArtifactId, found.TaskID, found.AttemptID, j.ID, j.owner, j.project).Scan(&path, &hash, &size)
		if e != nil {
			return e
		}
		if hash != found.SHA256 || size != found.Size || !artifactID.MatchString(path) {
			return status.Error(codes.FailedPrecondition, "input metadata mismatch")
		}
		return nil
	})
	return path, dbErr(e)
}
func (s *Server) DownloadInputArtifact(r *pb.InputArtifactRequest, stream grpc.ServerStreamingServer[pb.Chunk]) error {
	p, e := rpcutil.Worker(stream.Context())
	if e != nil {
		return e
	}
	path, e := s.inputArtifact(stream.Context(), p.Identity.WorkerID, r)
	if e != nil {
		return e
	}
	f, e := os.Open(filepath.Join(s.cfg.DataDir, "artifacts", path))
	if e != nil {
		return status.Error(codes.NotFound, "input artifact missing")
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	for {
		if _, e = s.inputArtifact(stream.Context(), p.Identity.WorkerID, r); e != nil {
			return e
		}
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
