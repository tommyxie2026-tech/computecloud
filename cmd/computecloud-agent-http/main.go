package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/agenthttp"
)

func main() {
	var socket string
	var cfg agenthttp.Config
	flag.StringVar(&socket, "socket", "/run/computecloud-agent/runtime.sock", "Unix HTTP socket")
	flag.StringVar(&cfg.TokenFile, "token-file", "/run/computecloud-agent/token", "bearer token file")
	flag.StringVar(&cfg.StateDir, "state-dir", "/var/lib/computecloud-agent/runs", "durable run identity directory")
	flag.StringVar(&cfg.WorkspaceRoot, "workspace-root", "/workspace", "mounted workspace root")
	flag.StringVar(&cfg.DockerExecutable, "docker", "/usr/bin/docker", "Docker CLI executable")
	flag.StringVar(&cfg.RuntimeImage, "runtime-image", "", "pinned Agent runtime OCI image reference with sha256 digest")
	flag.BoolVar(&cfg.PublicStrict, "public-strict", false, "require operator-provided isolated egress network and seccomp profile")
	flag.StringVar(&cfg.EgressNetwork, "egress-network", "bridge", "operator-created Docker network for Attempt containers")
	flag.StringVar(&cfg.SeccompProfile, "seccomp-profile", "", "operator-managed OCI seccomp profile")
	flag.Int64Var(&cfg.MemoryBytes, "memory-bytes", 2<<30, "Attempt container memory limit")
	flag.Int64Var(&cfg.NanoCPUs, "nano-cpus", 2_000_000_000, "Attempt container CPU quota in nano CPUs")
	flag.IntVar(&cfg.PidsLimit, "pids-limit", 256, "Attempt container PID limit")
	flag.StringVar(&cfg.CodexExecutable, "codex", "/usr/local/bin/codex", "Codex CLI executable")
	flag.StringVar(&cfg.CodexVersion, "codex-version", "0.160.1", "required Codex CLI version")
	flag.StringVar(&cfg.ClaudeExecutable, "claude", "/usr/local/bin/claude", "Claude Code executable")
	flag.StringVar(&cfg.ClaudeVersion, "claude-version", "2.1.292", "required Claude Code version")
	flag.Parse()
	if !filepath.IsAbs(socket) {
		log.Fatal("socket path must be absolute")
	}
	server, err := agenthttp.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := server.ValidateVersions(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		log.Fatal(err)
	}
	httpServer := newAgentRuntimeHTTPServer(server)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newAgentRuntimeHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
}
