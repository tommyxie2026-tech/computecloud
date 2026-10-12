package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const serverV19 = `CREATE TABLE api_keys (
  id TEXT PRIMARY KEY,
  digest TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  owner TEXT NOT NULL,
  projects_json BLOB NOT NULL,
  scopes_json BLOB NOT NULL,
  credentials_json BLOB NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX api_keys_owner_status ON api_keys(owner,revoked_at,expires_at,created_at);
CREATE INDEX api_keys_prefix ON api_keys(prefix);
`

// APIKey stores managed-key metadata. Digest is intentionally omitted from JSON
// and ListAPIKeys never populates it.
type APIKey struct {
	ID          string   `json:"id"`
	Digest      string   `json:"-"`
	Prefix      string   `json:"prefix"`
	Owner       string   `json:"owner"`
	Projects    []string `json:"projects"`
	Scopes      []string `json:"scopes"`
	Credentials []string `json:"credentials"`
	Label       string   `json:"label"`
	CreatedAt   int64    `json:"created_at"`
	ExpiresAt   int64    `json:"expires_at"`
	RevokedAt   int64    `json:"revoked_at,omitempty"`
}

func validKeyList(values []string, allowEmpty bool) bool {
	if len(values) == 0 {
		return allowEmpty
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func validateAPIKey(key APIKey) error {
	decoded, err := hex.DecodeString(key.Digest)
	if err != nil || len(decoded) != 32 || key.ID == "" || key.Owner == "" || !strings.HasPrefix(key.Prefix, "cc_live_") || len(key.Prefix) > 32 ||
		!validKeyList(key.Projects, false) || !validKeyList(key.Scopes, false) || !validKeyList(key.Credentials, true) || len(key.Label) > 128 ||
		key.CreatedAt < 1 || key.ExpiresAt <= key.CreatedAt {
		return errors.New("invalid API key metadata")
	}
	return nil
}

func (d *DB) CreateAPIKey(ctx context.Context, key APIKey) error {
	if err := validateAPIKey(key); err != nil {
		return err
	}
	projects, _ := json.Marshal(key.Projects)
	scopes, _ := json.Marshal(key.Scopes)
	credentials, _ := json.Marshal(key.Credentials)
	_, err := d.SQL.ExecContext(ctx, `INSERT INTO api_keys(id,digest,prefix,owner,projects_json,scopes_json,credentials_json,label,created_at,expires_at,revoked_at) VALUES(?,?,?,?,?,?,?,?,?,?,0)`, key.ID, key.Digest, key.Prefix, key.Owner, projects, scopes, credentials, key.Label, key.CreatedAt, key.ExpiresAt)
	return err
}

func (d *DB) LookupAPIKeyByDigest(ctx context.Context, digest string, now int64) (APIKey, error) {
	var key APIKey
	var projects, scopes, credentials []byte
	err := d.SQL.QueryRowContext(ctx, `SELECT id,digest,prefix,owner,projects_json,scopes_json,credentials_json,label,created_at,expires_at,revoked_at FROM api_keys WHERE digest=? AND revoked_at=0 AND expires_at>?`, digest, now).Scan(&key.ID, &key.Digest, &key.Prefix, &key.Owner, &projects, &scopes, &credentials, &key.Label, &key.CreatedAt, &key.ExpiresAt, &key.RevokedAt)
	if err != nil {
		return APIKey{}, err
	}
	for target, raw := range map[*[]string][]byte{&key.Projects: projects, &key.Scopes: scopes, &key.Credentials: credentials} {
		if err = json.Unmarshal(raw, target); err != nil {
			return APIKey{}, fmt.Errorf("corrupt API key metadata")
		}
	}
	return key, nil
}

func (d *DB) ListAPIKeys(ctx context.Context, owner string) ([]APIKey, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT id,prefix,owner,projects_json,scopes_json,credentials_json,label,created_at,expires_at,revoked_at FROM api_keys WHERE owner=? ORDER BY created_at DESC,id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		var key APIKey
		var projects, scopes, credentials []byte
		if err = rows.Scan(&key.ID, &key.Prefix, &key.Owner, &projects, &scopes, &credentials, &key.Label, &key.CreatedAt, &key.ExpiresAt, &key.RevokedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(projects, &key.Projects); err != nil {
			return nil, errors.New("corrupt API key metadata")
		}
		if err = json.Unmarshal(scopes, &key.Scopes); err != nil {
			return nil, errors.New("corrupt API key metadata")
		}
		if err = json.Unmarshal(credentials, &key.Credentials); err != nil {
			return nil, errors.New("corrupt API key metadata")
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (d *DB) RevokeAPIKey(ctx context.Context, owner, id string, now int64) error {
	result, err := d.SQL.ExecContext(ctx, `UPDATE api_keys SET revoked_at=CASE WHEN revoked_at=0 THEN ? ELSE revoked_at END WHERE owner=? AND id=?`, now, owner, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}
