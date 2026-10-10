package config

import "testing"

func TestConversationJobsDefaultOff(t *testing.T) {
	var c Server
	c.DefaultV02()
	if c.ConversationJobs.Enabled || c.ConversationJobs.LoopbackListen != "" {
		t.Fatalf("conversation endpoint unexpectedly enabled: %#v", c.ConversationJobs)
	}
}

func TestConversationPlaintextAddressMustBeLiteralLoopback(t *testing.T) {
	c := Server{Jobs: Jobs{Enabled: true}, ConversationJobs: ConversationJobs{Enabled: true, LoopbackListen: "0.0.0.0:8081", MaxRequestBytes: 1024}}
	if err := c.ValidateV02(); err == nil {
		t.Fatal("expected non-loopback listener to be rejected")
	}
}
