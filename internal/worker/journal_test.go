package worker

import (
	"context"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/protobuf/proto"
)

func TestCommandReplayAndStopTombstone(t *testing.T) {
	d, e := store.Open(t.TempDir(), store.WorkerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	w := &Worker{db: d}
	ctx := context.Background()
	a := &pb.Assignment{TaskId: "task", AttemptId: "attempt", Generation: 1, LeaseToken: "lease"}
	start := &pb.Command{CommandId: "start", Kind: "start", Assignment: a}
	launch, e := w.accept(ctx, start)
	if e != nil || !launch {
		t.Fatalf("start: %v %v", launch, e)
	}
	launch, e = w.accept(ctx, start)
	if e != nil || launch {
		t.Fatalf("replay: %v %v", launch, e)
	}
	conflict := proto.Clone(start).(*pb.Command)
	conflict.Assignment.LeaseToken = "changed"
	if _, e = w.accept(ctx, conflict); e == nil {
		t.Fatal("conflicting command accepted")
	}
	b := &pb.Assignment{TaskId: "task2", AttemptId: "attempt2", Generation: 1, LeaseToken: "lease2"}
	_, e = w.accept(ctx, &pb.Command{CommandId: "stop-first", Kind: "stop", Assignment: b})
	if e != nil {
		t.Fatal(e)
	}
	launch, e = w.accept(ctx, &pb.Command{CommandId: "late-start", Kind: "start", Assignment: b})
	if e != nil || launch {
		t.Fatalf("late start bypassed tombstone: %v %v", launch, e)
	}
	var count int
	_ = d.SQL.QueryRow("SELECT count(*) FROM runs").Scan(&count)
	if count != 2 {
		t.Fatalf("duplicate runs: %d", count)
	}
}
