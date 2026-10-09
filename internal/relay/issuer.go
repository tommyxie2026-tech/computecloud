package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Issuer keeps only short-lived, unclaimed Worker tickets. It is a routing
// credential service, not a Job or Worker execution state store.
type Issuer struct {
	broker      *Broker
	serverID    string
	serverToken [32]byte
	workers     map[[32]byte]string
	mu          sync.Mutex
	pending     map[string]pendingTicket
}

type pendingTicket struct {
	Ticket    string
	ExpiresMS int64
}

func NewIssuer(b *Broker, serverID string, serverToken []byte, workerTokens map[string][]byte) (*Issuer, error) {
	if b == nil || !validID(serverID) || len(serverToken) < 32 || len(workerTokens) == 0 {
		return nil, errors.New("relay issuer requires broker, server identity and strong private credentials")
	}
	i := &Issuer{broker: b, serverID: serverID, serverToken: sha256.Sum256(serverToken), workers: map[[32]byte]string{}, pending: map[string]pendingTicket{}}
	if i.serverToken == sha256.Sum256(b.key) {
		return nil, errors.New("relay issuer token must differ from signing key")
	}
	for id, token := range workerTokens {
		if !validID(id) || len(token) < 32 {
			return nil, errors.New("invalid relay Worker identity or credential")
		}
		hash := sha256.Sum256(token)
		if hash == i.serverToken || hash == sha256.Sum256(b.key) || i.workers[hash] != "" {
			return nil, errors.New("relay issuer credentials must be unique by role and Worker")
		}
		i.workers[hash] = id
	}
	return i, nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (i *Issuer) principal(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if len(token) < 32 || strings.TrimSpace(token) != token {
		return "", false
	}
	hash := sha256.Sum256([]byte(token))
	if hash == i.serverToken {
		return i.serverID, true
	}
	id := i.workers[hash]
	return id, false
}

func (i *Issuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "TLS required", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	identity, server := i.principal(r)
	if identity == "" {
		i.broker.recordRejected(rejectionInvalid)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/relay/pairs":
		if !server {
			i.broker.recordRejected(rejectionInvalid)
			http.Error(w, "server role required", http.StatusForbidden)
			return
		}
		i.issue(w, r)
	case "/v1/relay/pairs/claim":
		if server {
			i.broker.recordRejected(rejectionInvalid)
			http.Error(w, "worker role required", http.StatusForbidden)
			return
		}
		i.claim(w, identity)
	default:
		http.NotFound(w, r)
	}
}

func (i *Issuer) issue(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var input struct {
		WorkerID string `json:"worker_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		http.Error(w, "invalid issue request", http.StatusBadRequest)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || !validID(input.WorkerID) {
		http.Error(w, "invalid issue request", http.StatusBadRequest)
		return
	}
	known := false
	for _, id := range i.workers {
		if id == input.WorkerID {
			known = true
			break
		}
	}
	if !known {
		i.broker.recordRejected(rejectionInvalid)
		http.Error(w, "unknown Worker", http.StatusForbidden)
		return
	}
	now := time.Now()
	i.mu.Lock()
	defer i.mu.Unlock()
	for id, pending := range i.pending {
		if pending.ExpiresMS <= now.UnixMilli() {
			delete(i.pending, id)
		}
	}
	if len(i.pending) >= 1024 {
		i.broker.recordRejected(rejectionCapacity)
		http.Error(w, "issuer capacity", http.StatusTooManyRequests)
		return
	}
	if i.pending[input.WorkerID].ExpiresMS > now.UnixMilli() {
		i.broker.recordRejected(rejectionReplay)
		http.Error(w, "pair already pending", http.StatusConflict)
		return
	}
	pairID, err := randomID()
	if err != nil {
		http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
		return
	}
	epoch, err := randomID()
	if err != nil {
		http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
		return
	}
	// A pending claim must expire when the waiting Broker socket does. Otherwise
	// a late Worker can consume a ticket whose Server side has already left.
	claim := Ticket{RelayEpoch: i.broker.Epoch(), PairID: pairID, ServerID: i.serverID, WorkerID: input.WorkerID, Epoch: epoch,
		Role: "server", IssuedMS: now.UnixMilli(), ExpiresMS: now.Add(i.broker.limits.PairTimeout).UnixMilli()}
	serverTicket, err := Sign(i.broker.key, claim, now)
	if err != nil {
		http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
		return
	}
	claim.Role = "worker"
	workerTicket, err := Sign(i.broker.key, claim, now)
	if err != nil {
		http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
		return
	}
	i.pending[input.WorkerID] = pendingTicket{Ticket: workerTicket, ExpiresMS: claim.ExpiresMS}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"ticket": serverTicket})
}

func (i *Issuer) claim(w http.ResponseWriter, workerID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	pending := i.pending[workerID]
	if pending.ExpiresMS <= time.Now().UnixMilli() || pending.Ticket == "" {
		delete(i.pending, workerID)
		i.broker.recordRejected(rejectionNotReady)
		http.Error(w, "no pending pair", http.StatusNotFound)
		return
	}
	delete(i.pending, workerID)
	_ = json.NewEncoder(w).Encode(map[string]string{"ticket": pending.Ticket})
}
