package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TicketClient struct {
	address   string
	issuerURL string
	tokenFile string
	outerTLS  *tls.Config
	http      *http.Client
}

func NewTicketClient(address, issuerURL, tokenFile, caFile, serverName string) (*TicketClient, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return nil, errors.New("Relay address must be host:port")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return nil, err
	}
	u, err := url.Parse(issuerURL)
	if err != nil || u.Scheme != "https" || u.Hostname() != host || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Relay issuer requires an HTTPS origin")
	}
	if _, err := net.LookupPort("tcp", u.Port()); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(tokenFile) || !filepath.IsAbs(caFile) {
		return nil, errors.New("Relay credential and CA paths must be absolute")
	}
	if serverName == "" {
		serverName = host
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid Relay CA")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName}
	transport := &http.Transport{TLSClientConfig: tlsConfig.Clone()}
	return &TicketClient{address: address, issuerURL: strings.TrimSuffix(issuerURL, "/"), tokenFile: tokenFile,
		outerTLS: tlsConfig, http: &http.Client{Transport: transport, Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Relay issuer redirect rejected") }}}, nil
}

func (c *TicketClient) Close() { c.http.CloseIdleConnections() }

func (c *TicketClient) request(ctx context.Context, path string, body any) (string, error) {
	info, err := os.Stat(c.tokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("Relay issuer credential must be a private regular file")
	}
	token, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(token))
	if len(secret) < 32 || strings.ContainsAny(secret, "\r\n\x00") {
		return "", errors.New("invalid Relay issuer credential")
	}
	var data io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		data = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuerURL+path, data)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("Relay issuer status %d", resp.StatusCode)
	}
	var result struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxTicketBytes+128)).Decode(&result); err != nil {
		return "", err
	}
	if result.Ticket == "" || len(result.Ticket) > MaxTicketBytes {
		return "", errors.New("Relay issuer returned invalid ticket")
	}
	return result.Ticket, nil
}

func (c *TicketClient) Issue(ctx context.Context, workerID string) (string, error) {
	if !validID(workerID) {
		return "", errors.New("invalid Worker identity")
	}
	return c.request(ctx, "/v1/relay/pairs", map[string]string{"worker_id": workerID})
}
func (c *TicketClient) Claim(ctx context.Context) (string, error) {
	return c.request(ctx, "/v1/relay/pairs/claim", nil)
}
func (c *TicketClient) Connect(ctx context.Context, ticket string) (net.Conn, error) {
	return Connect(ctx, c.address, ticket, c.outerTLS)
}
