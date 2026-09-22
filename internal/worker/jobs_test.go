package worker

import (
	"archive/tar"
	"bytes"
	"context"
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"google.golang.org/grpc"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"github.com/tommyxie2026-tech/computecloud/internal/workspace"
)

func TestInputArchiveRejectsTraversalLinksDuplicatesAndLargeMembers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		typ    byte
		repeat bool
	}{{"../outside", tar.TypeReg, false}, {"/absolute", tar.TypeReg, false}, {"findings.json", tar.TypeSymlink, false}, {"findings.json", tar.TypeReg, true}} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for i := 0; i < 1+boolInt(tc.repeat); i++ {
			if e := tw.WriteHeader(&tar.Header{Name: tc.name, Typeflag: tc.typ, Size: 0, Linkname: "../outside"}); e != nil {
				t.Fatal(e)
			}
		}
		tw.Close()
		if e := extractBundle(&buf, t.TempDir(), 1<<20); e == nil {
			t.Fatalf("accepted invalid member %q", tc.name)
		}
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "findings.json", Size: 1024})
	tw.Write(make([]byte, 1024))
	tw.Close()
	if e := extractBundle(&buf, t.TempDir(), 100); e == nil {
		t.Fatal("size limit ignored")
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func TestPatchPathsRejectSymlinksAndEvidenceProvenance(t *testing.T) {
	repo, commit := testutil.Repository(t)
	ctx := context.Background()
	if e := os.Symlink("/tmp", filepath.Join(repo, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := workspace.Diff(ctx, repo, commit); e != nil {
		t.Fatal(e)
	}
	if _, e := changedPaths(ctx, repo, commit); e == nil {
		t.Fatal("symlink patch accepted")
	}
	f := job.Finding{ID: "f1", Severity: "high", Summary: "fixture", Path: "README.md", LineStart: 1, LineEnd: 1, Evidence: "test repository", Origin: "map", Sources: []job.Source{{PartitionKey: "a", FindingID: "f1"}}}
	if e := validateFindings(ctx, repo, commit, []job.Finding{f}, true, map[string]job.Finding{}); e == nil {
		t.Fatal("forged source accepted")
	}
	if e := validateFindings(ctx, repo, commit, []job.Finding{f}, true, map[string]job.Finding{"a/f1": f}); e != nil {
		t.Fatal(e)
	}
}

type inputClient struct {
	pb.RuntimeServiceClient
	data []byte
}

func (c inputClient) DownloadInputArtifact(context.Context, *pb.InputArtifactRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[pb.Chunk], error) {
	return &inputStream{data: c.data}, nil
}

type inputStream struct {
	grpc.ClientStream
	data []byte
}

func (s *inputStream) Recv() (*pb.Chunk, error) {
	if len(s.data) == 0 {
		return nil, io.EOF
	}
	data := s.data
	s.data = nil
	return &pb.Chunk{Data: data}, nil
}
func TestDownloadInputVerifiesFrozenHashAndSize(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range []string{"report.json", "changes.patch", "stderr.log"} {
		tw.WriteHeader(&tar.Header{Name: name, Size: 0})
	}
	tw.Close()
	raw := buf.Bytes()
	item := job.ManifestItem{PartitionKey: "a", ArtifactID: "fixture", SHA256: job.Hash(raw), Size: int64(len(raw))}
	for _, bad := range []bool{false, true} {
		data := append([]byte(nil), raw...)
		if bad {
			data[0] ^= 1
		}
		w := &Worker{client: inputClient{data: data}}
		_, e := w.downloadInput(context.Background(), &pb.Assignment{AttemptId: "attempt"}, item, t.TempDir())
		if (e != nil) != bad {
			t.Fatalf("bad=%v error=%v", bad, e)
		}
	}
}
