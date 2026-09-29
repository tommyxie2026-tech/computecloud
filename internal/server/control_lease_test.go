package server

import (
	"net/http/httptest"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestControlWriteLeaseExclusionRenewRelease(t *testing.T) {
	h := newJobHarness(t, false, func(cfg *config.Server) {
		cfg.Users[0].Scopes = append(cfg.Users[0].Scopes, "jobs:control")
	})
	j, err := h.s.SubmitJob(h.ctx, "lease-job", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}

	first, err := h.s.acquireControlWriteLease(h.ctx, j.ID, "device-a")
	if err != nil || first.LeaseToken == "" {
		t.Fatalf("acquire=%+v err=%v", first, err)
	}
	if _, err = h.s.acquireControlWriteLease(h.ctx, j.ID, "device-b"); status.Code(err) != codes.AlreadyExists || status.Convert(err).Message() != "WRITE_LEASE_HELD" {
		t.Fatalf("second holder err=%v", err)
	}
	renewed, err := h.s.renewControlWriteLease(h.ctx, j.ID, "device-a", first.LeaseToken)
	if err != nil || renewed.ExpiresAtMS <= first.ExpiresAtMS {
		t.Fatalf("renew=%+v err=%v", renewed, err)
	}

	req := httptest.NewRequest("POST", "/v1/jobs/"+j.ID+"/control/cancel", nil).WithContext(h.ctx)
	req.Header.Set("X-Control-Lease", first.LeaseToken)
	if err = h.s.requireControlWriteLease(req, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.db.SQL.Exec("UPDATE control_write_leases SET expires_at=? WHERE job_id=?", store.Now()-1, j.ID); err != nil {
		t.Fatal(err)
	}
	if err = h.s.requireControlWriteLease(req, j.ID); status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "WRITE_LEASE_INVALID" {
		t.Fatalf("expired lease err=%v", err)
	}

	reacquired, err := h.s.acquireControlWriteLease(h.ctx, j.ID, "device-b")
	if err != nil || reacquired.LeaseToken == "" {
		t.Fatalf("reacquire=%+v err=%v", reacquired, err)
	}
	if err = h.s.releaseControlWriteLease(h.ctx, j.ID, "device-b", reacquired.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.acquireControlWriteLease(h.ctx, j.ID, "device-c"); err != nil {
		t.Fatalf("post-release acquire err=%v", err)
	}
}

func TestControlWriteLeaseWrongTokenRejected(t *testing.T) {
	h := newJobHarness(t, false, func(cfg *config.Server) {
		cfg.Users[0].Scopes = append(cfg.Users[0].Scopes, "jobs:control")
	})
	j, err := h.s.SubmitJob(h.ctx, "lease-token-job", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.acquireControlWriteLease(h.ctx, j.ID, "device-a"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/jobs/"+j.ID+"/control/cancel", nil).WithContext(h.ctx)
	req.Header.Set("X-Control-Lease", "wrong")
	if err = h.s.requireControlWriteLease(req, j.ID); status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "WRITE_LEASE_INVALID" {
		t.Fatalf("wrong token err=%v", err)
	}
}
