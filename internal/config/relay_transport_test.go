package config

import (
	"path/filepath"
	"testing"
)

func TestRelayTransportExplicitOptIn(t *testing.T) {
	innerTLS := TLS{CertFile: "/tmp/server.pem", KeyFile: "/tmp/server.key"}
	if err := (RelayTransport{}).Validate(true, TLS{InsecureLoopback: true}); err != nil {
		t.Fatalf("direct default: %v", err)
	}
	valid := RelayTransport{Mode: "direct_then_relay", RelayAddress: "relay.example:7445", IssuerURL: "https://relay.example:7446", TokenFile: filepath.Join(t.TempDir(), "token"), CAFile: filepath.Join(t.TempDir(), "ca.pem")}
	if err := valid.Validate(true, innerTLS); err != nil {
		t.Fatal(err)
	}
	if err := valid.Validate(false, TLS{CAFile: "/tmp/server.pem"}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RelayTransport){
		"unknown mode":        func(c *RelayTransport) { c.Mode = "relay" },
		"plain issuer":        func(c *RelayTransport) { c.IssuerURL = "http://relay.example:7446" },
		"other host":          func(c *RelayTransport) { c.IssuerURL = "https://other.example:7446" },
		"invalid port":        func(c *RelayTransport) { c.RelayAddress = "relay.example:99999" },
		"relative credential": func(c *RelayTransport) { c.TokenFile = "token" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if err := bad.Validate(false, TLS{CAFile: "/tmp/server.pem"}); err == nil {
				t.Fatal("invalid Relay config accepted")
			}
		})
	}
	if err := valid.Validate(false, TLS{InsecureLoopback: true}); err == nil {
		t.Fatal("Relay accepted plaintext inner transport")
	}
	if err := (RelayTransport{Mode: "direct", RelayAddress: valid.RelayAddress}).Validate(true, innerTLS); err == nil {
		t.Fatal("direct mode accepted Relay fields")
	}
}
