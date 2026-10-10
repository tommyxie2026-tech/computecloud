package config

import (
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"strings"
	"testing"
)

func TestConversationJobsDefaultOff(t *testing.T) {
	var c Server
	c.DefaultV02()
	if c.ConversationJobs.Enabled || c.ConversationJobs.LoopbackListen != "" {
		t.Fatalf("conversation endpoint unexpectedly enabled: %#v", c.ConversationJobs)
	}
}

func validConversationConfig() Server {
	c := Server{Jobs: Jobs{Enabled: true, Templates: []JobTemplate{{RuntimeProfile: "codex_exec", PolicyRef: "write", AcceptanceProfile: "check", Digest: strings.Repeat("a", 64)}}}, Credentials: map[string]int{"account": 1}, Users: []Identity{{Owner: "runner", Projects: []string{"project"}, Credentials: []string{"account"}, Scopes: []string{"jobs:submit", "jobs:read", "jobs:cancel"}}, {Owner: "caller", Projects: []string{"project"}, ConversationProfile: "safe", Scopes: []string{"conversations:submit", "conversations:read"}}}, ConversationJobs: ConversationJobs{Enabled: true, MaxRequestBytes: 256 << 10, Profiles: []ConversationProfile{{ID: "safe", PublicModel: "remote-codex", ExecutionOwner: "runner", ProjectID: "project", Workspace: job.Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("b", 40)}, Execution: job.Execution{Engine: "codex", RuntimeProfile: "codex_exec", Model: "model-a", CredentialRef: "account", PolicyRef: "write", AcceptanceProfile: "check"}, Limits: job.Limits{TimeoutSeconds: 100, MaxAttemptsPerTask: 1}, MaxInputBytes: 128 << 10, MaxOutputTokens: 1000, MaxOutputBytes: 1 << 20, MaxActive: 4}}}}
	c.DefaultV02()
	return c
}

func TestConversationConfigRequiresFixedProfileAndSeparateScopes(t *testing.T) {
	c := validConversationConfig()
	if err := c.ValidateV02(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	c = validConversationConfig()
	c.Users[1].ConversationProfile = "missing"
	if err := c.ValidateV02(); err == nil {
		t.Fatal("unknown profile accepted")
	}
	c = validConversationConfig()
	c.ConversationJobs.Profiles[0].PublicModel = "bad alias"
	if err := c.ValidateV02(); err == nil {
		t.Fatal("invalid model alias accepted")
	}
	c = validConversationConfig()
	c.Users[1].Scopes = append(c.Users[1].Scopes, "jobs:submit")
	if err := c.ValidateV02(); err == nil {
		t.Fatal("conversation token with general Job scope accepted")
	}
	c = validConversationConfig()
	c.ConversationJobs.Profiles[0].MaxInputBytes = 1 << 20
	if err := c.ValidateV02(); err == nil {
		t.Fatal("profile input beyond bounded request accepted")
	}
	c = validConversationConfig()
	c.Users[0].Scopes = []string{"jobs:submit", "jobs:read"}
	if err := c.ValidateV02(); err == nil {
		t.Fatal("execution identity without cancel permission accepted")
	}
}

func TestConversationPlaintextAddressMustBeLiteralLoopback(t *testing.T) {
	c := Server{Jobs: Jobs{Enabled: true}, ConversationJobs: ConversationJobs{Enabled: true, LoopbackListen: "0.0.0.0:8081", MaxRequestBytes: 1024}}
	if err := c.ValidateV02(); err == nil {
		t.Fatal("expected non-loopback listener to be rejected")
	}
}
