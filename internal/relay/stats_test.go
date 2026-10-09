package relay

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStatsTrackConnectionsBytesRejectionsAndBackpressure(t *testing.T) {
	b, err := New(bytes.Repeat([]byte{1}, 32), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b.recordPairOpened()
	b.recordForwarded(32768, true)
	b.recordForwarded(1024, false)
	b.recordRejected(rejectionInvalid)
	b.recordRejected(rejectionReplay)
	b.recordRejected(rejectionCapacity)
	b.recordRejected(rejectionQuota)
	b.recordRejected(rejectionTimeout)

	got := b.Stats()
	if got.ActiveConnections != 1 || got.AcceptedPairs != 1 || got.BytesIn != 33792 || got.BytesOut != 33792 || got.BackpressureTotal != 1 {
		t.Fatalf("traffic stats=%+v", got)
	}
	if got.RejectedInvalid != 1 || got.RejectedReplay != 1 || got.RejectedCapacity != 1 || got.RejectedQuota != 1 || got.RejectedTimeout != 1 {
		t.Fatalf("rejection stats=%+v", got)
	}
	b.recordPairClosed()
	if got = b.Stats(); got.ActiveConnections != 0 || got.AcceptedPairs != 1 {
		t.Fatalf("closed stats=%+v", got)
	}
}

func TestStatsContainNoSensitiveValues(t *testing.T) {
	b, err := New(bytes.Repeat([]byte{2}, 32), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b.recordPairOpened()
	b.recordForwarded(64, false)
	raw, err := json.Marshal(b.Stats())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ticket-secret", "bearer-secret", "private prompt", "artifact payload", "worker-identity"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("sensitive value %q leaked in %s", secret, raw)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if _, ok := value.(float64); !ok {
			t.Fatalf("metric %s has non-numeric value %T", key, value)
		}
	}
}
