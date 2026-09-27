package server

import (
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

func runtimeV2Task(required ...string) *pb.Task {
	return &pb.Task{Spec: &pb.TaskSpec{
		ProjectId: "p",
		RuntimeProfile: "fixture",
		Model: "m",
		CredentialRef: "cred",
		PolicyRef: "policy",
		AcceptanceProfile: "verify",
		RequiredCapabilities: required,
		Workspace: &pb.Workspace{RepositoryRef: "repo"},
	}}
}

func runtimeV2Peer(caps ...string) *session {
	return &session{
		identity: config.Identity{Projects: []string{"p"}},
		hello: &pb.WorkerHello{Runtimes: []*pb.Runtime{{
			Profile: "fixture",
			Models: []string{"m"},
			Credentials: []string{"cred"},
			Repositories: []string{"repo"},
			Policies: []string{"policy"},
			Verifiers: []string{"verify"},
			Capabilities: caps,
		}}},
	}
}

func TestRuntimeV2NamespacedCapabilityMatching(t *testing.T) {
	p := runtimeV2Peer("runtime:event_stream", "tool:shell", "environment:process", "event_stream")
	for _, required := range [][]string{
		{"runtime:event_stream"},
		{"tool:shell"},
		{"environment:process"},
		{"event_stream"},
		{"runtime:event_stream", "tool:shell", "environment:process"},
	} {
		if !fits(p, runtimeV2Task(required...)) {
			t.Fatalf("capabilities did not match: %v", required)
		}
	}
	if fits(p, runtimeV2Task("tool:browser")) {
		t.Fatal("missing tool capability matched")
	}
}
