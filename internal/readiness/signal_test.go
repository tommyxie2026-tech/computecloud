package readiness

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestSignalFreshnessBoundsAndCompatibility(t *testing.T) {
	now := int64(100000)
	good := &pb.ExecutionSignal{ObservedAtMs: now, MeasuredAtMs: now, FreeSlots: 1, WorkspacePrepareP50Ms: 10, RepositoryRefs: []string{"repo"}}
	cases := []struct {
		name     string
		change   func(*pb.ExecutionSignal)
		received int64
		valid    bool
	}{
		{"fresh", func(*pb.ExecutionSignal) {}, now, true},
		{"stale receipt", func(*pb.ExecutionSignal) {}, now - TTLMS - 1, false},
		{"stale observation", func(s *pb.ExecutionSignal) { s.ObservedAtMs = now - TTLMS - 1 }, now, false},
		{"stale sample", func(s *pb.ExecutionSignal) { s.MeasuredAtMs = now - TTLMS - 1 }, now, false},
		{"future", func(s *pb.ExecutionSignal) { s.ObservedAtMs = now + 5001 }, now, false},
		{"negative", func(s *pb.ExecutionSignal) { s.WorkspacePrepareP50Ms = -1 }, now, false},
		{"oversize", func(s *pb.ExecutionSignal) { s.RepositoryRefs = make([]string, 33) }, now, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := proto.Clone(good).(*pb.ExecutionSignal)
			c.change(s)
			if (Fresh(s, c.received, now) != nil) != c.valid {
				t.Fatal("incorrect validity")
			}
		})
	}
	fresh := Fresh(good, now, now)
	fresh.RepositoryRefs[0] = "mutated"
	if good.RepositoryRefs[0] != "repo" {
		t.Fatal("projection aliases input")
	}
	old, _ := proto.Marshal(&pb.Renew{Attempts: []*pb.AttemptRef{{AttemptId: "a"}}})
	var r pb.Renew
	if proto.Unmarshal(old, &r) != nil || r.Readiness != nil || len(r.Attempts) != 1 {
		t.Fatal("legacy renew rejected")
	}
	raw, _ := proto.Marshal(&pb.Renew{Readiness: good})
	if proto.Unmarshal(raw, &r) != nil || !proto.Equal(r.Readiness, good) {
		t.Fatal("signal roundtrip failed")
	}
}
