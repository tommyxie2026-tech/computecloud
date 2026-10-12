package config

import "testing"

func TestIdentityWorkloadLimitsDefaultsAndValidation(t *testing.T) {
	c := Server{Users: []Identity{{Owner: "user"}}}
	c.DefaultV02()
	if got, want := c.Users[0].Limits, (IdentityLimits{MaxActiveJobs: 8, MaxQueuedJobs: 32, MaxModelRequests: 4}); got != want {
		t.Fatalf("default limits=%+v want %+v", got, want)
	}
	c.Users[0].Limits.MaxModelRequests = 0
	if err := c.ValidateV02(); err == nil {
		t.Fatal("partial workload limits accepted")
	}
}

func TestIdentityWorkloadLimitsMustMatchOwner(t *testing.T) {
	c := Server{Users: []Identity{{Owner: "same", Limits: IdentityLimits{8, 32, 4}}, {Owner: "same", Limits: IdentityLimits{8, 32, 2}}}}
	if err := c.ValidateV02(); err == nil {
		t.Fatal("different limits for identities of one owner accepted")
	}
}

func TestWorkerAdmissionDefaultsAndValidation(t *testing.T) {
	c := Server{}
	c.DefaultV02()
	if c.WorkerAdmission != (WorkerAdmission{MaxSessions: 128, MaxStreams: 128}) {
		t.Fatalf("worker admission defaults=%+v", c.WorkerAdmission)
	}
	c.WorkerAdmission.MaxStreams = 0
	if err := c.ValidateV02(); err == nil {
		t.Fatal("partial worker admission limits accepted")
	}
}
