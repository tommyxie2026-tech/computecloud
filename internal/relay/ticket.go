// Package relay implements experimental, memory-only byte transport. It owns no
// Job, Task, Attempt, command, artifact or scheduler state.
package relay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const MaxTicketTTL = time.Minute
const MaxTicketBytes = 2048

type Ticket struct {
	RelayEpoch string `json:"relay_epoch"`
	PairID     string `json:"pair_id"`
	ServerID   string `json:"server_id"`
	WorkerID   string `json:"worker_id"`
	Epoch      string `json:"connection_epoch"`
	Role       string `json:"role"`
	Nonce      string `json:"nonce"`
	IssuedMS   int64  `json:"issued_ms"`
	ExpiresMS  int64  `json:"expires_ms"`
}

func validID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
func (t Ticket) validate(now time.Time) error {
	if !validID(t.RelayEpoch) || !validID(t.PairID) || !validID(t.ServerID) || !validID(t.WorkerID) || !validID(t.Epoch) || !validID(t.Nonce) || (t.Role != "server" && t.Role != "worker") || t.IssuedMS <= 0 || t.IssuedMS > now.UnixMilli() || t.ExpiresMS <= now.UnixMilli() || t.ExpiresMS <= t.IssuedMS || t.ExpiresMS-t.IssuedMS > MaxTicketTTL.Milliseconds() {
		return errors.New("invalid or expired relay ticket")
	}
	return nil
}
func Sign(key []byte, t Ticket, now time.Time) (string, error) {
	if len(key) < 32 {
		return "", errors.New("relay signing key must contain at least 32 bytes")
	}
	if t.Nonce == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		t.Nonce = hex.EncodeToString(b)
	}
	if err := t.validate(now); err != nil {
		return "", err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	h := hmac.New(sha256.New, key)
	h.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}
func Verify(key []byte, token string, now time.Time) (Ticket, error) {
	var t Ticket
	if len(key) < 32 || len(token) > MaxTicketBytes {
		return t, errors.New("invalid relay ticket")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return t, errors.New("invalid relay ticket")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return t, errors.New("invalid relay ticket")
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(parts[0]))
	if !hmac.Equal(signature, h.Sum(nil)) {
		return t, errors.New("invalid relay ticket")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return t, errors.New("invalid relay ticket")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err = d.Decode(&t); err != nil {
		return t, errors.New("invalid relay ticket")
	}
	return t, t.validate(now)
}
