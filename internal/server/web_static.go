package server

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// os.Root prevents symlinks and path components from escaping the asset root.
func newWebStaticHandler(dir string) (http.Handler, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("web assets directory unavailable")
	}
	defer root.Close()
	entry, err := root.Open("index.html")
	if err != nil {
		return nil, errors.New("web assets index.html required")
	}
	info, err := entry.Stat()
	entry.Close()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("invalid web index")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/ui/") {
			http.NotFound(w, r)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/ui/")
		if strings.Contains(rel, "\\") || strings.Contains(rel, "\x00") || strings.Contains(rel, "..") {
			http.NotFound(w, r)
			return
		}
		if rel == "" {
			rel = "index.html"
		}
		if !fs.ValidPath(rel) {
			http.NotFound(w, r)
			return
		}
		current, err := os.OpenRoot(dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer current.Close()
		file, err := current.Open(rel)
		if err != nil {
			if !os.IsNotExist(err) || path.Ext(rel) != "" {
				http.NotFound(w, r)
				return
			}
			file, err = current.Open("index.html")
			rel = "index.html"
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if ct := mime.TypeByExtension(filepath.Ext(rel)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		http.ServeContent(w, r, rel, stat.ModTime(), file)
	}), nil
}
