package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebDefaults(t *testing.T) {
	c := Server{}
	c.DefaultV02()
	if c.Web.Enabled || c.Web.Auth.Username != "admin" || c.Web.Auth.SessionTTLSeconds != 28800 {
		t.Fatalf("defaults: %+v", c.Web)
	}
	p, err := WebPassword(c.Web.Auth)
	if err != nil || p != "admin" {
		t.Fatalf("password %q: %v", p, err)
	}
}
func TestWebPasswordSource(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "password")
	if err := os.WriteFile(file, []byte("secret with spaces \n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := WebPassword(WebAuth{PasswordFile: file})
	if err != nil || p != "secret with spaces " {
		t.Fatalf("%q %v", p, err)
	}
	for _, a := range []WebAuth{{Password: "x", PasswordFile: file}, {PasswordFile: filepath.Join(dir, "missing")}} {
		if _, err := WebPassword(a); err == nil {
			t.Fatal("expected failure")
		}
	}
	os.WriteFile(file, nil, 0600)
	if _, err := WebPassword(WebAuth{PasswordFile: file}); err == nil {
		t.Fatal("empty file accepted")
	}
}
func TestWebIdentityBinding(t *testing.T) {
	w := Web{Enabled: true, Auth: WebAuth{IdentityOwner: "owner", SessionTTLSeconds: 28800}}
	u := Identity{Owner: "owner", Projects: []string{"p"}}
	if err := ValidateWeb(w, []Identity{u}); err != nil {
		t.Fatal(err)
	}
	for _, users := range [][]Identity{nil, {u, u}} {
		if err := ValidateWeb(w, users); err == nil {
			t.Fatal("ambiguous or missing identity accepted")
		}
	}
	w.Auth.IdentityOwner = ""
	if err := ValidateWeb(w, []Identity{u}); err == nil {
		t.Fatal("empty owner accepted")
	}
}
func TestWebSessionTTL(t *testing.T) {
	for _, n := range []int{299, 300, 86400, 86401} {
		w := Web{Enabled: true, Auth: WebAuth{Username: "admin", IdentityOwner: "o", SessionTTLSeconds: n}}
		err := ValidateWeb(w, []Identity{{Owner: "o"}})
		if (err == nil) != (n >= 300 && n <= 86400) {
			t.Fatalf("ttl=%d err=%v", n, err)
		}
	}
}
func TestWebExternalOrigin(t *testing.T) {
	for _, origin := range []string{"https://control.example", "http://control.example", "https://control.example/path", "https://user@control.example", "https://control.example?q=x"} {
		w := Web{Enabled: true, ExternalOrigin: origin, Auth: WebAuth{IdentityOwner: "o", SessionTTLSeconds: 300}}
		err := ValidateWeb(w, []Identity{{Owner: "o"}})
		if (err == nil) != (origin == "https://control.example") {
			t.Fatalf("%q: %v", origin, err)
		}
	}
}
func TestWebExplicitEmptyPasswordRejected(t *testing.T) {
	for _, auth := range []string{"password: \"\"", "password_file: \"\"", "password: \"\"\n      password_file: key"} {
		f := filepath.Join(t.TempDir(), "server.yaml")
		body := "server:\n  web:\n    enabled: true\n    auth:\n      identity_owner: o\n      " + auth + "\n  users:\n    - owner: o\n"
		if err := os.WriteFile(f, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(f)
		if err == nil {
			err = ValidateWeb(c.Server.Web, c.Server.Users)
		}
		if err == nil {
			t.Fatal("explicit empty password accepted", auth)
		}
	}
}
