package server

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func reviewFixture(t *testing.T, headers []*tar.Header, contents [][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for i, h := range headers {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(contents[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestArtifactReviewRejectsUnsafeBundles(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  byte
	}{{"../secret.txt", tar.TypeReg}, {"/secret.txt", tar.TypeReg}, {"nested/../a.txt", tar.TypeReg}, {"link.txt", tar.TypeSymlink}, {"hard.txt", tar.TypeLink}} {
		t.Run(tc.name, func(t *testing.T) {
			b := reviewFixture(t, []*tar.Header{{Name: tc.name, Typeflag: tc.typ, Linkname: "secret"}}, [][]byte{nil})
			if _, err := reviewArtifactBundle(b); err == nil {
				t.Fatal("unsafe bundle accepted")
			}
		})
	}
	b := reviewFixture(t, []*tar.Header{{Name: "a.txt"}, {Name: "a.txt"}}, [][]byte{nil, nil})
	if _, err := reviewArtifactBundle(b); err == nil {
		t.Fatal("duplicate accepted")
	}
	text := []byte(strings.Repeat("x", artifactReviewMaxMember+10))
	b = reviewFixture(t, []*tar.Header{{Name: "changes.patch", Size: int64(len(text))}, {Name: "binary.txt", Size: 2}}, [][]byte{text, {0, 1}})
	members, err := reviewArtifactBundle(b)
	if err != nil || len(members) != 2 || !members[0].Truncated || len(*members[0].Text) != artifactReviewMaxMember || members[1].Text != nil {
		t.Fatalf("bounds/binary: %+v %v", members, err)
	}
}
func TestArtifactReviewAuthorizationAndIntegrity(t *testing.T) {
	h := newJobHarness(t, true)
	j, err := h.s.SubmitJob(h.ctx, "review", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	if done := h.wait(t, j.ID); done.State != "SUCCEEDED" {
		t.Fatal(done.State)
	}
	var aid, file string
	if err = h.s.db.SQL.QueryRow(`SELECT a.id,a.path FROM artifacts a JOIN tasks t ON t.id=a.task WHERE t.job_id=? AND a.state='ACCEPTED' LIMIT 1`, j.ID).Scan(&aid, &file); err != nil {
		t.Fatal(err)
	}
	url := "/v1/jobs/" + j.ID + "/artifacts/" + aid + "/review"
	code, b := h.request(t, "GET", url, "", nil)
	if code != 200 {
		t.Fatalf("review=%d %s", code, b)
	}
	var out struct {
		Current bool                   `json:"current_generation"`
		Members []artifactReviewMember `json:"members"`
	}
	if err = json.Unmarshal(b, &out); err != nil || !out.Current || len(out.Members) == 0 {
		t.Fatalf("provenance=%s %v", b, err)
	}
	other, err := h.s.SubmitJob(h.ctx, "other-review", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	code, _ = h.request(t, "GET", "/v1/jobs/"+other.ID+"/artifacts/"+aid+"/review", "", nil)
	if code != 404 {
		t.Fatalf("cross Job artifact=%d", code)
	}
	p := filepath.Join(h.s.cfg.DataDir, "artifacts", file)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 1
	if err = os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	code, b = h.request(t, "GET", url, "", nil)
	if code != 400 || !bytes.Contains(b, []byte("ARTIFACT_CORRUPT")) {
		t.Fatalf("tamper=%d %s", code, b)
	}
}
