package config

import (
	"strings"
	"testing"
)

func TestWorkerMTLSConfigurationValidation(t *testing.T) {
	worker := Identity{WorkerID: "w1", ClientCertSHA256: []string{strings.Repeat("a", 64)}}
	c := Server{TLS: TLS{WorkerMTLSMode: "required", ClientCAFile: "/ca.pem"}, Workers: []Identity{worker}}
	c.DefaultV02()
	if err := c.ValidateV02(); err != nil {
		t.Fatalf("valid mTLS config rejected: %v", err)
	}
	c.Workers[0].ClientCertSHA256 = nil
	if err := c.ValidateV02(); err == nil {
		t.Fatal("missing Worker certificate fingerprint accepted")
	}
	c.TLS.WorkerMTLSMode = "optional"
	if err := c.ValidateV02(); err != nil {
		t.Fatalf("optional migration mode rejected: %v", err)
	}
	c.Workers[0].ClientCertSHA256 = []string{"not-a-fingerprint"}
	if err := c.ValidateV02(); err == nil {
		t.Fatal("malformed Worker certificate fingerprint accepted")
	}
	c.TLS.WorkerMTLSMode = "unknown"
	if err := c.ValidateV02(); err == nil {
		t.Fatal("unknown Worker mTLS mode accepted")
	}
}

func TestWorkerClientCertificatePairValidation(t *testing.T) {
	w := Worker{ID: "w1", DataDir: "/tmp/w1", Address: "127.0.0.1:7443", Slots: 1, Runtimes: map[string]Runtime{"r": {}}, TLS: TLS{CertFile: "/tmp/client.crt"}}
	if err := w.Validate(); err == nil {
		t.Fatal("client certificate without key accepted")
	}
}
