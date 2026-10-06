package server

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const artifactReviewMaxBundle = 8 << 20
const artifactReviewMaxMember = 64 << 10
const artifactReviewMaxText = 256 << 10

type artifactReviewMember struct {
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	Text      *string `json:"text"`
	Truncated bool    `json:"truncated"`
}

func reviewArtifactBundle(b []byte) ([]artifactReviewMember, error) {
	if len(b) > artifactReviewMaxBundle {
		return nil, fmt.Errorf("ARTIFACT_REVIEW_TOO_LARGE")
	}
	tr := tar.NewReader(bytes.NewReader(b))
	out := []artifactReviewMember{}
	seen := map[string]bool{}
	remaining := artifactReviewMaxText
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(out) >= 64 || h.Size < 0 || len(h.Name) > 256 || path.Clean(h.Name) != h.Name || strings.ContainsAny(h.Name, "\\\x00") || path.IsAbs(h.Name) || h.Name == ".." || strings.HasPrefix(h.Name, "../") || seen[h.Name] || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA) {
			return nil, fmt.Errorf("INVALID_REVIEW_BUNDLE")
		}
		seen[h.Name] = true
		member := artifactReviewMember{Name: h.Name, Size: h.Size}
		switch path.Ext(h.Name) {
		case ".json", ".txt", ".md", ".patch", ".diff", ".log":
			limit := min(artifactReviewMaxMember, remaining)
			raw, err := io.ReadAll(io.LimitReader(tr, int64(limit)))
			if err != nil {
				return nil, err
			}
			member.Truncated = int64(len(raw)) < h.Size
			// A truncated UTF-8 suffix is excluded without interpreting the content.
			if member.Truncated {
				for len(raw) > 0 && !utf8.Valid(raw) {
					raw = raw[:len(raw)-1]
					if limit-len(raw) > 3 {
						break
					}
				}
			}
			if utf8.Valid(raw) && !bytes.ContainsRune(raw, 0) {
				text := string(raw)
				member.Text = &text
				remaining -= len(raw)
			}
		}
		out = append(out, member)
	}
	return out, nil
}
func (s *Server) httpArtifactReview(w http.ResponseWriter, r *http.Request) {
	jid := r.PathValue("id")
	if _, err := s.jobAuthorized(r.Context(), jid, "jobs:read"); err != nil {
		httpError(w, err)
		return
	}
	var file, hash, attempt string
	var size, generation int64
	var current bool
	err := s.db.SQL.QueryRowContext(r.Context(), `SELECT a.path,a.hash,a.size,a.attempt,a.generation,(a.attempt=t.attempt AND a.generation=t.current_generation) FROM artifacts a JOIN tasks t ON t.id=a.task WHERE t.job_id=? AND a.id=? AND a.state='ACCEPTED'`, jid, r.PathValue("artifact")).Scan(&file, &hash, &size, &attempt, &generation, &current)
	if err != nil {
		httpError(w, dbErr(err))
		return
	}
	if size < 0 || size > artifactReviewMaxBundle {
		httpError(w, status.Error(codes.ResourceExhausted, "ARTIFACT_REVIEW_TOO_LARGE"))
		return
	}
	if !artifactID.MatchString(file) {
		httpError(w, status.Error(codes.Internal, "INVALID_ARTIFACT_PATH"))
		return
	}
	fd, err := unix.Open(filepath.Join(s.cfg.DataDir, "artifacts", file), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		httpError(w, status.Error(codes.NotFound, "ARTIFACT_MISSING"))
		return
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		httpError(w, status.Error(codes.FailedPrecondition, "ARTIFACT_CORRUPT"))
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, artifactReviewMaxBundle+1))
	sum := sha256.Sum256(b)
	if err != nil || int64(len(b)) != size || hex.EncodeToString(sum[:]) != hash {
		httpError(w, status.Error(codes.FailedPrecondition, "ARTIFACT_CORRUPT"))
		return
	}
	members, err := reviewArtifactBundle(b)
	if err != nil {
		httpError(w, status.Error(codes.FailedPrecondition, "INVALID_REVIEW_BUNDLE"))
		return
	}
	jsonResponse(w, 200, map[string]any{"artifact_id": r.PathValue("artifact"), "sha256": hash, "attempt_id": attempt, "generation": generation, "current_generation": current, "members": members})
}
