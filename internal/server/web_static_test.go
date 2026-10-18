package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebStaticRoutes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>Control</html>"), 0600)
	h, e := newWebStaticHandler(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"/ui/", "/ui/jobs/test"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Control") {
			t.Fatal(p, w.Code)
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing CSP")
		}
	}
}
func TestWebStaticBoundary(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("Control"), 0600)
	os.Mkdir(filepath.Join(dir, "sub"), 0700)
	os.WriteFile(filepath.Join(dir, "sub", "secret"), []byte("not public"), 0600)
	outside := filepath.Join(t.TempDir(), "external")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(dir, "linked"))
	h, e := newWebStaticHandler(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"/v1/unknown", "/ui/../secret", "/ui/sub/", "/ui/linked", "/ui/missing.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code == 200 {
			t.Fatal(p, w.Body.String())
		}
	}
	_ = http.MethodGet
}
func TestWebStaticMissingAssets(t *testing.T) {
	if _, e := newWebStaticHandler(t.TempDir()); e == nil {
		t.Fatal("missing index accepted")
	}
}
