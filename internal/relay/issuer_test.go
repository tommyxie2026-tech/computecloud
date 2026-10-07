package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func issuerRequest(t *testing.T, client *http.Client, url, token string, body any) (int, string) {
	t.Helper()
	var raw bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&raw).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(http.MethodPost, url, &raw)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return resp.StatusCode, result.Ticket
}

func TestIssuerPairClaimBoundedAndRoleSeparated(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	broker, err := New(key, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	serverToken := "server-token-0123456789-abcdefghijk"
	workerToken := "worker-token-0123456789-abcdefghijk"
	otherToken := "other-token-0123456789-abcdefghijk"
	issuer, err := NewIssuer(broker, "server", []byte(serverToken), map[string][]byte{"worker-a": []byte(workerToken), "worker-b": []byte(otherToken)})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewTLSServer(issuer)
	defer service.Close()
	issueURL := service.URL + "/v1/relay/pairs"
	claimURL := issueURL + "/claim"
	if status, _ := issuerRequest(t, service.Client(), issueURL, workerToken, map[string]string{"worker_id": "worker-a"}); status != http.StatusForbidden {
		t.Fatalf("Worker minted a pair: %d", status)
	}
	if status, _ := issuerRequest(t, service.Client(), issueURL, serverToken, map[string]string{"worker_id": "unknown"}); status != http.StatusForbidden {
		t.Fatalf("unknown Worker pair status=%d", status)
	}
	status, serverTicket := issuerRequest(t, service.Client(), issueURL, serverToken, map[string]string{"worker_id": "worker-a"})
	if status != http.StatusCreated || serverTicket == "" {
		t.Fatalf("issue=%d ticket=%q", status, serverTicket)
	}
	if status, _ := issuerRequest(t, service.Client(), issueURL, serverToken, map[string]string{"worker_id": "worker-a"}); status != http.StatusConflict {
		t.Fatalf("duplicate pending pair status=%d", status)
	}
	if status, _ := issuerRequest(t, service.Client(), claimURL, otherToken, nil); status != http.StatusNotFound {
		t.Fatalf("other Worker claimed ticket: %d", status)
	}
	if status, _ := issuerRequest(t, service.Client(), claimURL, serverToken, nil); status != http.StatusForbidden {
		t.Fatalf("Server claimed Worker ticket: %d", status)
	}
	status, workerTicket := issuerRequest(t, service.Client(), claimURL, workerToken, nil)
	if status != http.StatusOK || workerTicket == "" {
		t.Fatalf("claim=%d ticket=%q", status, workerTicket)
	}
	if status, _ := issuerRequest(t, service.Client(), claimURL, workerToken, nil); status != http.StatusNotFound {
		t.Fatalf("ticket replay status=%d", status)
	}
	now := time.Now()
	serverClaim, err := Verify(key, serverTicket, now)
	if err != nil {
		t.Fatal(err)
	}
	workerClaim, err := Verify(key, workerTicket, now)
	if err != nil {
		t.Fatal(err)
	}
	if serverClaim.Role != "server" || workerClaim.Role != "worker" || serverClaim.PairID != workerClaim.PairID || serverClaim.Epoch != workerClaim.Epoch || serverClaim.WorkerID != "worker-a" || serverClaim.RelayEpoch != broker.Epoch() {
		t.Fatalf("issued pair mismatch: server=%+v worker=%+v", serverClaim, workerClaim)
	}
	if status, _ := issuerRequest(t, service.Client(), issueURL, "invalid-token-0123456789-abcdefghi", map[string]string{"worker_id": "worker-a"}); status != http.StatusUnauthorized {
		t.Fatalf("invalid auth status=%d", status)
	}
}

func TestIssuerRequiresTLSAndStrongDistinctCredentials(t *testing.T) {
	broker, err := New(bytes.Repeat([]byte("k"), 32), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewIssuer(broker, "server", []byte("short"), map[string][]byte{"worker": bytes.Repeat([]byte("w"), 32)}); err == nil {
		t.Fatal("weak token accepted")
	}
	token := bytes.Repeat([]byte("s"), 32)
	if _, err := NewIssuer(broker, "server", token, map[string][]byte{"worker": token}); err == nil {
		t.Fatal("reused token accepted")
	}
	if _, err := NewIssuer(broker, "server", bytes.Repeat([]byte("k"), 32), map[string][]byte{"worker": bytes.Repeat([]byte("w"), 32)}); err == nil {
		t.Fatal("signing key reused as issuer token")
	}
	issuer, err := NewIssuer(broker, "server", token, map[string][]byte{"worker": bytes.Repeat([]byte("w"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	plain := httptest.NewServer(issuer)
	defer plain.Close()
	if status, _ := issuerRequest(t, plain.Client(), plain.URL+"/v1/relay/pairs", string(token), map[string]string{"worker_id": "worker"}); status != http.StatusForbidden {
		t.Fatalf("plaintext issue status=%d", status)
	}
}
