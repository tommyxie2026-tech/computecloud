package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/conversation"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type conversationRecord struct {
	ID, Owner, Profile, Protocol, JobID, State string
	Request                                    conversation.Request
}

func (s *Server) conversationProfile(ctx context.Context, scope string) (rpcutil.Principal, config.ConversationProfile, error) {
	p, err := rpcutil.Require(ctx, scope, false)
	if err != nil {
		return p, config.ConversationProfile{}, err
	}
	if !s.cfg.ConversationJobs.Enabled {
		return p, config.ConversationProfile{}, status.Error(codes.FailedPrecondition, "CONVERSATION_JOBS_DISABLED")
	}
	for _, profile := range s.cfg.ConversationJobs.Profiles {
		if p.Identity.ConversationProfile == profile.ID && config.Contains(p.Identity.Projects, profile.ProjectID) {
			return p, profile, nil
		}
	}
	return p, config.ConversationProfile{}, status.Error(codes.PermissionDenied, "conversation profile not authorized")
}

func (s *Server) beginConversationRequest(ctx context.Context, r conversation.Request, idem string) (conversationRecord, error) {
	caller, p, err := s.conversationProfile(ctx, "conversations:submit")
	if err != nil {
		return conversationRecord{}, err
	}
	if r.Model != p.PublicModel {
		return conversationRecord{}, status.Error(codes.PermissionDenied, "model alias not authorized")
	}
	textBytes := 0
	var transcript strings.Builder
	for _, m := range r.Transcript {
		textBytes += len(m.Text)
		transcript.WriteString("[")
		transcript.WriteString(m.Role)
		transcript.WriteString("]\n")
		transcript.WriteString(m.Text)
		transcript.WriteString("\n\n")
	}
	if textBytes > p.MaxInputBytes || r.MaxOutputTokens < 1 || r.MaxOutputTokens > p.MaxOutputTokens {
		return conversationRecord{}, status.Error(codes.ResourceExhausted, "conversation profile limit exceeded")
	}
	requestHash := job.Hash(job.JSON(r))
	profileHash := job.Hash(job.JSON(p))
	if idem == "" {
		idem = "auto_" + requestHash
	}
	if !job.ValidKey(idem) {
		return conversationRecord{}, status.Error(codes.InvalidArgument, "invalid idempotency key")
	}
	var existing conversationRecord
	err = s.db.Tx(ctx, func(q store.Query) error {
		var raw []byte
		var oldHash string
		var oldProfileHash string
		e := q.QueryRowContext(ctx, "SELECT request_id,owner,profile_id,protocol,request_blob,job_id,state,request_hash,profile_hash FROM conversation_requests WHERE owner=? AND profile_id=? AND idem_key=?", caller.Identity.Owner, p.ID, idem).Scan(&existing.ID, &existing.Owner, &existing.Profile, &existing.Protocol, &raw, &existing.JobID, &existing.State, &oldHash, &oldProfileHash)
		if e == nil {
			if oldHash != requestHash || oldProfileHash != profileHash {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			return json.Unmarshal(raw, &existing.Request)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var active int
		if e = q.QueryRowContext(ctx, `SELECT count(*) FROM conversation_requests cr LEFT JOIN jobs j ON j.id=cr.job_id WHERE cr.owner=? AND cr.profile_id=? AND ((cr.job_id='' AND cr.state='PENDING') OR (cr.job_id<>'' AND (j.id IS NULL OR j.state NOT IN ('SUCCEEDED','FAILED','CANCELED'))))`, caller.Identity.Owner, p.ID).Scan(&active); e != nil {
			return e
		}
		if active >= p.MaxActive {
			return status.Error(codes.ResourceExhausted, "CONVERSATION_LIMIT")
		}
		existing = conversationRecord{ID: "cnv_" + store.ID(), Owner: caller.Identity.Owner, Profile: p.ID, Protocol: r.Protocol, State: "PENDING", Request: r}
		_, e = q.ExecContext(ctx, `INSERT INTO conversation_requests(request_id,owner,profile_id,protocol,idem_key,request_hash,profile_hash,request_blob,state,created,updated) VALUES(?,?,?,?,?,?,?,?,'PENDING',?,?)`, existing.ID, existing.Owner, p.ID, r.Protocol, idem, requestHash, profileHash, job.JSON(r), store.Now(), store.Now())
		return e
	})
	if err != nil {
		return conversationRecord{}, dbErr(err)
	}
	return existing, nil
}

func (s *Server) ensureConversationJob(ctx context.Context, rec conversationRecord, r conversation.Request) (conversationRecord, *Job, error) {
	for _, p := range s.cfg.ConversationJobs.Profiles {
		if p.ID != rec.Profile {
			continue
		}
		var execIdentity config.Identity
		for _, u := range s.cfg.Users {
			if u.Owner == p.ExecutionOwner && config.Contains(u.Projects, p.ProjectID) && config.Contains(u.Credentials, p.Execution.CredentialRef) {
				execIdentity = u
				break
			}
		}
		if execIdentity.Owner == "" {
			return rec, nil, status.Error(codes.FailedPrecondition, "conversation execution identity unavailable")
		}
		delegated, err := rpcutil.ConversationJobContext(ctx, execIdentity, p.ID, "submit")
		if err != nil {
			return rec, nil, err
		}
		var transcript strings.Builder
		for _, m := range r.Transcript {
			fmt.Fprintf(&transcript, "[%s]\n%s\n\n", m.Role, m.Text)
		}
		spec := job.Spec{SchemaVersion: "v0.2", ProjectID: p.ProjectID, Mode: "single", Workspace: p.Workspace, Input: job.Input{Text: transcript.String()}, Execution: &p.Execution, Limits: p.Limits}
		body := job.JSON(spec)
		j, err := s.SubmitJob(delegated, "conversation_"+rec.ID, body)
		if err != nil {
			return rec, nil, err
		}
		_, err = s.db.SQL.ExecContext(ctx, "UPDATE conversation_requests SET job_id=?,state='SUBMITTED',updated=? WHERE request_id=? AND owner=?", j.ID, store.Now(), rec.ID, rec.Owner)
		if err != nil {
			return rec, nil, dbErr(err)
		}
		rec.JobID = j.ID
		rec.State = "SUBMITTED"
		return rec, j, nil
	}
	return rec, nil, status.Error(codes.PermissionDenied, "conversation profile not authorized")
}
