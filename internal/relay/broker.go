package relay

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type Limits struct {
	Connections, PairsPerWorker int
	PairTimeout, IdleTimeout    time.Duration
	BytesPerSecond              int64
}

func DefaultLimits() Limits {
	return Limits{Connections: 128, PairsPerWorker: 2, PairTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, BytesPerSecond: 1 << 20}
}

type pendingPair struct {
	ticket Ticket
	conn   net.Conn
	mate   chan net.Conn
	done   chan struct{}
}
type Broker struct {
	epoch   string
	key     []byte
	limits  Limits
	slots   chan struct{}
	mu      sync.Mutex
	pairs   map[string]*pendingPair
	workers map[string]int
	spent   map[string]int64
}

func New(key []byte, limits Limits) (*Broker, error) {
	if len(key) < 32 || limits.Connections < 2 || limits.Connections > 1024 || limits.PairsPerWorker < 1 || limits.PairsPerWorker > 16 || limits.PairTimeout < time.Millisecond || limits.PairTimeout > MaxTicketTTL || limits.IdleTimeout < time.Millisecond || limits.IdleTimeout > 10*time.Minute || limits.BytesPerSecond < 32768 || limits.BytesPerSecond > 1<<30 {
		return nil, errors.New("invalid relay key or limits")
	}
	epoch := make([]byte, 16)
	if _, err := rand.Read(epoch); err != nil {
		return nil, err
	}
	return &Broker{epoch: hex.EncodeToString(epoch), key: append([]byte(nil), key...), limits: limits, slots: make(chan struct{}, limits.Connections), pairs: map[string]*pendingPair{}, workers: map[string]int{}, spent: map[string]int64{}}, nil
}
func (b *Broker) Epoch() string { return b.epoch }

func (b *Broker) Serve(ctx context.Context, l net.Listener, config *tls.Config) error {
	if config == nil || len(config.Certificates) == 0 || config.MinVersion < tls.VersionTLS12 {
		return errors.New("relay outer TLS 1.2+ certificate required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l = tls.NewListener(l, config.Clone())
	defer l.Close()
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			l.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case b.slots <- struct{}{}:
			wg.Add(1)
			go func() { defer wg.Done(); defer func() { <-b.slots }(); b.handle(ctx, conn) }()
		default:
			conn.Close()
		}
	}
}
func readLine(conn net.Conn, limit int) ([]byte, error) {
	out := make([]byte, 0, 512)
	var one [1]byte
	for len(out) <= limit {
		n, err := conn.Read(one[:])
		if n == 1 {
			if one[0] == '\n' {
				return out, nil
			}
			out = append(out, one[0])
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("relay registration too large")
}
func writeReady(conn net.Conn) error {
	_, err := io.WriteString(conn, "{\"ready\":true}\n")
	return err
}
func (b *Broker) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	conn.SetDeadline(time.Now().Add(b.limits.PairTimeout))
	raw, err := readLine(conn, MaxTicketBytes+64)
	if err != nil {
		return
	}
	var request struct {
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return
	}
	ticket, err := Verify(b.key, request.Ticket, time.Now())
	if err != nil {
		return
	}
	pair, first, err := b.attach(conn, ticket)
	if err != nil {
		return
	}
	if !first {
		select {
		case <-pair.done:
		case <-ctx.Done():
		}
		return
	}
	defer close(pair.done)
	defer b.release(ticket, pair)
	timeout := time.NewTimer(min(b.limits.PairTimeout, time.Until(time.UnixMilli(ticket.ExpiresMS))))
	defer timeout.Stop()
	var mate net.Conn
	select {
	case mate = <-pair.mate:
	case <-timeout.C:
		return
	case <-ctx.Done():
		return
	}
	defer mate.Close()
	if err = writeReady(conn); err != nil {
		return
	}
	if err = writeReady(mate); err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	mate.SetDeadline(time.Time{})
	failures := make(chan error, 2)
	go func() { failures <- b.copy(ctx, conn, mate) }()
	go func() { failures <- b.copy(ctx, mate, conn) }()
	<-failures
	conn.Close()
	mate.Close()
	<-failures
}
func (b *Broker) attach(conn net.Conn, t Ticket) (*pendingPair, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().UnixMilli()
	if t.RelayEpoch != b.epoch {
		return nil, false, errors.New("stale relay epoch")
	}
	for key, expiry := range b.spent {
		if expiry <= now {
			delete(b.spent, key)
		}
	}
	if b.spent["nonce:"+t.Nonce] > now || b.spent["pair:"+t.PairID] > now || len(b.spent)+2 > 4096 {
		return nil, false, errors.New("relay ticket replay or capacity")
	}
	if pair := b.pairs[t.PairID]; pair != nil {
		old := pair.ticket
		if old.ServerID != t.ServerID || old.WorkerID != t.WorkerID || old.Epoch != t.Epoch || old.Role == t.Role || old.ExpiresMS <= now {
			return nil, false, errors.New("relay pair identity mismatch")
		}
		b.spent["nonce:"+t.Nonce] = t.ExpiresMS
		b.spent["pair:"+t.PairID] = max(old.ExpiresMS, t.ExpiresMS)
		delete(b.pairs, t.PairID)
		pair.mate <- conn
		return pair, false, nil
	}
	if b.workers[t.WorkerID] >= b.limits.PairsPerWorker {
		return nil, false, errors.New("relay worker quota")
	}
	pair := &pendingPair{ticket: t, conn: conn, mate: make(chan net.Conn, 1), done: make(chan struct{})}
	b.spent["nonce:"+t.Nonce] = t.ExpiresMS
	b.pairs[t.PairID] = pair
	b.workers[t.WorkerID]++
	return pair, true, nil
}
func (b *Broker) release(t Ticket, p *pendingPair) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pairs[t.PairID] == p {
		delete(b.pairs, t.PairID)
	}
	b.workers[t.WorkerID]--
	if b.workers[t.WorkerID] == 0 {
		delete(b.workers, t.WorkerID)
	}
}
func (b *Broker) copy(ctx context.Context, dst, src net.Conn) error {
	buffer := make([]byte, 32<<10)
	for {
		src.SetReadDeadline(time.Now().Add(b.limits.IdleTimeout))
		n, err := src.Read(buffer)
		if n > 0 {
			dst.SetWriteDeadline(time.Now().Add(b.limits.IdleTimeout))
			written := 0
			for written < n {
				m, e := dst.Write(buffer[written:n])
				written += m
				if e != nil {
					return e
				}
				if m == 0 {
					return io.ErrNoProgress
				}
			}
			// One bounded buffer burst, followed by pacing; no unbounded forwarding queue.
			timer := time.NewTimer(time.Duration(int64(n) * int64(time.Second) / b.limits.BytesPerSecond))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// Connect establishes only the outer rendezvous. The returned connection must be
// wrapped by the existing inner Server TLS/gRPC stack before carrying application data.
func Connect(ctx context.Context, address, token string, config *tls.Config) (net.Conn, error) {
	if config == nil || config.InsecureSkipVerify || config.MinVersion < tls.VersionTLS12 || len(token) > MaxTicketBytes {
		return nil, errors.New("verified relay TLS and bounded ticket required")
	}
	d := tls.Dialer{Config: config.Clone()}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	if when, has := ctx.Deadline(); has && when.Before(deadline) {
		deadline = when
	}
	conn.SetDeadline(deadline)
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)
	raw, _ := json.Marshal(map[string]string{"ticket": token})
	if _, err = conn.Write(append(raw, '\n')); err != nil {
		return nil, err
	}
	line, err := readLine(conn, 64)
	if err != nil {
		return nil, err
	}
	if string(line) != "{\"ready\":true}" {
		return nil, fmt.Errorf("relay pair rejected")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}
