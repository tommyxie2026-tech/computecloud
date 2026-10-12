package main

import (
	"net/http"
	"testing"
	"time"
)

func TestAgentRuntimeHTTPServerBoundsHeaders(t *testing.T) {
	server := newAgentRuntimeHTTPServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout=%s", server.ReadHeaderTimeout)
	}
	if server.MaxHeaderBytes < 1 || server.MaxHeaderBytes > 16<<10 {
		t.Fatalf("MaxHeaderBytes=%d; want a positive limit no greater than 16 KiB", server.MaxHeaderBytes)
	}
}
