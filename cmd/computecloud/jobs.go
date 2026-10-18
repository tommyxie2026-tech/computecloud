package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type jobOptions struct {
	op, file, id, key, control, artifact, out, cursor, node, record string
	after                                                           int64
	limit                                                           int
}

func runJob(ctx context.Context, c config.Client, o jobOptions) error {
	if o.op == "compile-graph" {
		raw, e := os.ReadFile(o.file)
		if e != nil {
			return e
		}
		spec, e := graph.Decode(raw)
		if e != nil {
			var ce *graph.CoverageError
			if errors.As(e, &ce) {
				if outErr := json.NewEncoder(os.Stdout).Encode(ce); outErr != nil {
					return outErr
				}
			}
			return e
		}
		plan, e := graph.Compile(spec)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(plan)
	}
	base, e := url.Parse(c.HTTPURL)
	if e != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return fmt.Errorf("client.http_url must be a server origin")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && c.TLS.InsecureLoopback && rpcutil.Loopback(base.Host)) {
		return fmt.Errorf("HTTP requires TLS or explicitly enabled literal loopback")
	}
	tok, e := config.Token(c.TokenFile)
	if e != nil {
		return e
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.TLS.CAFile != "" {
		pem, e := os.ReadFile(c.TLS.CAFile)
		if e != nil {
			return e
		}
		pool, e := x509.SystemCertPool()
		if e != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("invalid client CA file")
		}
		tlsConfig.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsConfig
	tr.ResponseHeaderTimeout = 15 * time.Second
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: 20 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if o.op == "download" {
		client.Timeout = 5 * time.Minute
	}
	controlLeaseToken := ""
	request := func(method, path string, body []byte) (*http.Response, error) {
		r, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.HTTPURL, "/")+path, bytes.NewReader(body))
		if e != nil {
			return nil, e
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Content-Type", "application/json")
		if controlLeaseToken != "" {
			r.Header.Set("X-Control-Lease", controlLeaseToken)
		}
		if o.key != "" {
			r.Header.Set("Idempotency-Key", o.key)
		}
		res, e := client.Do(r)
		if e != nil {
			return nil, e
		}
		if res.StatusCode >= 300 {
			defer res.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, b)
		}
		return res, nil
	}
	read := func(method, path string, body []byte) ([]byte, error) {
		res, e := request(method, path, body)
		if e != nil {
			return nil, e
		}
		defer res.Body.Close()
		b, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		if e == nil && len(b) > 1<<20 {
			e = fmt.Errorf("response too large")
		}
		return b, e
	}
	print := func(method, path string, body []byte) error {
		b, e := read(method, path, body)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(os.Stdout, strings.TrimSpace(string(b)))
		return e
	}
	path := "/v1/jobs/" + url.PathEscape(o.id)
	switch o.op {
	case "recover-graph":
		return runRecoveryCLI(ctx, o.id, o.file, read, func(token string) { controlLeaseToken = token })
	case "read-source-status":
		return print("GET", path+"/graph/source-reads/"+url.PathEscape(o.node), nil)
	case "coverage":
		return print("GET", path+"/graph/coverage-verifications/"+url.PathEscape(o.key), nil)
	case "verify-coverage":
		raw, e := os.ReadFile(o.file)
		if e != nil {
			return e
		}
		return print("POST", path+"/graph/coverage-verifications", raw)
	case "list":
		return print("GET", "/v1/jobs?limit="+strconv.Itoa(o.limit)+"&before="+url.QueryEscape(o.cursor), nil)
	case "submit", "submit-graph":
		if o.key == "" || o.file == "" {
			return fmt.Errorf("job submit requires --key and --file; reuse the key after transport failure")
		}
		b, e := os.ReadFile(o.file)
		if e != nil {
			return e
		}
		endpoint := "/v1/jobs"
		if o.op == "submit-graph" {
			endpoint = "/v1/graphs"
		}
		return print("POST", endpoint, b)
	case "capabilities":
		return print("GET", "/v1/capabilities", nil)
	}
	if o.id == "" {
		return fmt.Errorf("--id required")
	}
	switch o.op {
	case "graph-record":
		if o.node == "" || o.record == "" {
			return fmt.Errorf("--node and --record required")
		}
		return print("GET", path+"/graph/record/"+url.PathEscape(o.node)+"/"+url.PathEscape(o.record), nil)
	case "propose-graph":
		b, e := os.ReadFile(o.file)
		if e != nil {
			return e
		}
		holderBody, _ := json.Marshal(map[string]string{"holder_id": "cli-proposal-" + store.ID()})
		leaseRaw, e := read("POST", path+"/control-lease", holderBody)
		if e != nil {
			return e
		}
		var lease struct {
			Token string `json:"lease_token"`
		}
		if e = json.Unmarshal(leaseRaw, &lease); e != nil || lease.Token == "" {
			return fmt.Errorf("invalid control lease response")
		}
		controlLeaseToken = lease.Token
		defer func() { _, _ = read("DELETE", path+"/control-lease", holderBody) }()
		return print("POST", path+"/goal/replans", b)
	case "graph":
		return print("GET", path+"/graph", nil)
	case "graph-validity":
		return print("GET", path+"/graph/validity", nil)
	case "verify-record", "read-source", "issue-import":
		b, e := os.ReadFile(o.file)
		if e != nil {
			return e
		}
		endpoint := path + "/graph/import-grants"
		if o.op == "verify-record" {
			endpoint = path + "/graph/verifications"
		}
		if o.op == "read-source" {
			endpoint = path + "/graph/source-reads"
		}
		return print("POST", endpoint, b)
	case "get-import", "revoke-import":
		if o.key == "" {
			return fmt.Errorf("--key grant ID required")
		}
		endpoint := path + "/graph/import-grants/" + url.PathEscape(o.key)
		if o.op == "revoke-import" {
			return print("POST", endpoint+"/revoke", []byte(`{}`))
		}
		return print("GET", endpoint, nil)
	case "invalidate-graph", "evaluate-graph":
		b, e := os.ReadFile(o.file)
		if e != nil {
			return e
		}
		endpoint := path + "/graph/evaluations"
		if o.op == "invalidate-graph" {
			endpoint = path + "/graph/invalidations"
		}
		return print("POST", endpoint, b)
	case "trace":
		return print("GET", path+"/trace?limit="+strconv.Itoa(o.limit)+"&before="+url.QueryEscape(o.cursor), nil)
	case "get":
		return print("GET", path, nil)
	case "result":
		return print("GET", path+"/result", nil)
	case "tasks", "artifacts":
		return print("GET", path+"/"+o.op+"?limit="+strconv.Itoa(o.limit)+"&after="+url.QueryEscape(o.cursor), nil)
	case "events":
		return print("GET", path+"/events?after_seq="+strconv.FormatInt(o.after, 10)+"&limit="+strconv.Itoa(o.limit), nil)
	case "cancel":
		if o.control == "" {
			o.control = store.ID()
		}
		b, _ := json.Marshal(map[string]string{"control_id": o.control, "reason": "requested by CLI"})
		return print("POST", path+"/cancel", b)
	case "watch":
		for {
			b, e := read("GET", path+"/events?after_seq="+strconv.FormatInt(o.after, 10)+"&limit=100", nil)
			if e != nil {
				return e
			}
			var page struct {
				Events []json.RawMessage `json:"events"`
				Next   int64             `json:"next_seq,string"`
				More   bool              `json:"has_more"`
			}
			if e = json.Unmarshal(b, &page); e != nil {
				return e
			}
			for _, ev := range page.Events {
				if _, e = fmt.Fprintln(os.Stdout, string(ev)); e != nil {
					return e
				}
			}
			o.after = page.Next
			if page.More {
				continue
			}
			b, e = read("GET", path, nil)
			if e != nil {
				return e
			}
			var j struct {
				State string `json:"state"`
				Last  int64  `json:"last_seq,string"`
			}
			if e = json.Unmarshal(b, &j); e != nil {
				return e
			}
			if (j.State == "SUCCEEDED" || j.State == "FAILED" || j.State == "CANCELED") && j.Last <= o.after {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	case "download":
		if o.out == "" || o.artifact == "" {
			return fmt.Errorf("job download requires --artifact and --out")
		}
		res, e := request("GET", path+"/artifacts/"+url.PathEscape(o.artifact), nil)
		if e != nil {
			return e
		}
		defer res.Body.Close()
		f, e := os.OpenFile(o.out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		ok := false
		defer func() {
			f.Close()
			if !ok {
				os.Remove(o.out)
			}
		}()
		h := sha256.New()
		if _, e = io.Copy(io.MultiWriter(f, h), res.Body); e != nil {
			return e
		}
		if hex.EncodeToString(h.Sum(nil)) != res.Header.Get("X-Content-SHA256") {
			return fmt.Errorf("artifact hash mismatch")
		}
		if e = f.Sync(); e != nil {
			return e
		}
		ok = true
		return nil
	default:
		return fmt.Errorf("unknown job subcommand %q", o.op)
	}
}
