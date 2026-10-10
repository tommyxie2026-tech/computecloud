package server

import (
	"context"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/conversation"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConversationRequestDurableIdempotency(t *testing.T) {
	db, err := store.Open(t.TempDir(), store.ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := config.ConversationProfile{ID: "safe", PublicModel: "agent-model", ProjectID: "p", MaxInputBytes: 4096, MaxOutputTokens: 100}
	s := &Server{db: db, cfg: config.Server{Jobs: config.Jobs{Enabled: true}, ConversationJobs: config.ConversationJobs{Enabled: true, Profiles: []config.ConversationProfile{p}}}}
	ctx := context.Background()
	ctx = rpcutil.WithPrincipal(ctx, rpcutil.Principal{Identity: config.Identity{Owner: "caller", Projects: []string{"p"}, ConversationProfile: "safe", Scopes: []string{"conversations:submit"}}})
	r := conversation.Request{Protocol: "messages", Model: "agent-model", MaxOutputTokens: 20, Transcript: []conversation.Message{{Role: "user", Text: "hello"}}}
	first, err := s.beginConversationRequest(ctx, r, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.beginConversationRequest(ctx, r, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.ID == "" {
		t.Fatalf("idempotent replay created a new request: %q %q", first.ID, second.ID)
	}
	r.Transcript[0].Text = "different"
	if _, err = s.beginConversationRequest(ctx, r, "key-1"); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestConversationSubmissionCannotDelegateWrongProfile(t *testing.T) {
	ctx := rpcutil.WithPrincipal(context.Background(), rpcutil.Principal{Identity: config.Identity{Owner: "caller", ConversationProfile: "other", Scopes: []string{"conversations:submit"}}})
	if _, err := rpcutil.ConversationJobContext(ctx, config.Identity{Owner: "executor", Scopes: []string{"jobs:submit"}}, "safe", "submit"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected delegation denial, got %v", err)
	}
}
