package config

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/url"
	"os"
	"strings"
)

type Web struct {
	ExternalOrigin string  `yaml:"external_origin"`
	Enabled        bool    `yaml:"enabled"`
	AssetsDir      string  `yaml:"assets_dir"`
	Auth           WebAuth `yaml:"auth"`
}
type WebAuth struct {
	passwordSet       bool
	passwordFileSet   bool
	Username          string `yaml:"username"`
	Password          string `yaml:"password"`
	PasswordFile      string `yaml:"password_file"`
	IdentityOwner     string `yaml:"identity_owner"`
	SessionTTLSeconds int    `yaml:"session_ttl_seconds"`
}

// Preserve explicit empty secrets so an empty deployment substitution fails closed.
func (a *WebAuth) UnmarshalYAML(n *yaml.Node) error {
	type plain WebAuth
	allowed := map[string]bool{"username": true, "password": true, "password_file": true, "identity_owner": true, "session_ttl_seconds": true}
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if !allowed[key] {
			return fmt.Errorf("unknown web auth field %q", key)
		}
	}
	var v plain
	if err := n.Decode(&v); err != nil {
		return err
	}
	*a = WebAuth(v)
	for i := 0; i < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "password":
			a.passwordSet = true
		case "password_file":
			a.passwordFileSet = true
		}
	}
	return nil
}
func (w *Web) defaults() {
	if w.Auth.Username == "" {
		w.Auth.Username = "admin"
	}
	if w.Auth.SessionTTLSeconds == 0 {
		w.Auth.SessionTTLSeconds = 28800
	}
	if w.AssetsDir == "" {
		w.AssetsDir = "/usr/share/computecloud/control"
	}
}
func WebPassword(a WebAuth) (string, error) {
	if (a.passwordSet || a.Password != "") && (a.passwordFileSet || a.PasswordFile != "") {
		return "", errors.New("web password and password_file are mutually exclusive")
	}
	if (a.passwordSet && a.Password == "") || (a.passwordFileSet && a.PasswordFile == "") {
		return "", errors.New("explicit web password source must not be empty")
	}
	if a.PasswordFile != "" {
		b, err := os.ReadFile(a.PasswordFile)
		if err != nil {
			return "", errors.New("cannot read web password_file")
		}
		p := strings.TrimRight(string(b), "\r\n")
		if p == "" {
			return "", errors.New("web password_file is empty")
		}
		return p, nil
	}
	if a.Password != "" {
		return a.Password, nil
	}
	return "admin", nil
}
func ValidateWeb(w Web, users []Identity) error {
	if !w.Enabled {
		return nil
	}
	if w.ExternalOrigin != "" {
		u, e := url.Parse(w.ExternalOrigin)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("web external_origin must be an HTTPS origin without a path")
		}
	}
	if w.Auth.SessionTTLSeconds < 300 || w.Auth.SessionTTLSeconds > 86400 {
		return errors.New("web session_ttl_seconds must be 300..86400")
	}
	if w.Auth.IdentityOwner == "" {
		return errors.New("web identity_owner required")
	}
	count := 0
	for _, u := range users {
		if u.Owner == w.Auth.IdentityOwner {
			count++
		}
	}
	if count != 1 {
		return errors.New("web identity_owner must uniquely match one user")
	}
	_, err := WebPassword(w.Auth)
	return err
}
