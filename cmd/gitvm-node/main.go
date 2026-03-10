package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/open-gitagent/gitvm/node"
	"github.com/open-gitagent/gitvm/orchestrator"
	"github.com/open-gitagent/gitvm/runtime"
	dockerrt "github.com/open-gitagent/gitvm/runtime/docker"
	fcrt "github.com/open-gitagent/gitvm/runtime/firecracker"
	"github.com/open-gitagent/gitvm/template"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	port := envInt("GITVM_NODE_PORT", 9090)
	cpURL := envStr("GITVM_CONTROL_PLANE", "")
	nodeKey := envStr("GITVM_NODE_KEY", "")
	nodeName := envStr("GITVM_NODE_NAME", hostname())
	dataDir := envStr("GITVM_DATA_DIR", "/tmp/gitvm")
	runtimeType := envStr("GITVM_RUNTIME", "docker") // "docker" or "firecracker"
	maxSandboxes := envInt("GITVM_MAX_SANDBOXES", 50)

	// Firecracker-specific
	fcBin := envStr("GITVM_FIRECRACKER_BIN", "firecracker")
	hostIface := envStr("GITVM_HOST_INTERFACE", "eth0")

	// Docker-specific
	dockerImage := envStr("GITVM_DOCKER_IMAGE", "ubuntu:22.04")
	agentBinary := envStr("GITVM_AGENT_BINARY", "")

	os.MkdirAll(dataDir, 0o755)

	// Select runtime backend
	var rt runtime.Runtime
	switch runtimeType {
	case "docker":
		rt = dockerrt.New(dockerrt.Config{
			AgentBinaryPath: agentBinary,
			DefaultImage:    dockerImage,
		}, logger)
		logger.Info("using Docker runtime", "image", dockerImage)

	case "firecracker":
		tmplCache := template.NewCache("")
		rt = fcrt.New(fcrt.Config{
			DataDir:        dataDir,
			FirecrackerBin: fcBin,
			HostInterface:  hostIface,
			MaxVMs:         maxSandboxes,
		}, tmplCache, logger)
		logger.Info("using Firecracker runtime", "bin", fcBin)

	default:
		fmt.Fprintf(os.Stderr, "unknown runtime: %s (use 'docker' or 'firecracker')\n", runtimeType)
		os.Exit(1)
	}

	// Orchestrator with chosen runtime
	orch := orchestrator.New(orchestrator.Config{
		DataDir:           dataDir,
		MaxVMs:            maxSandboxes,
		DefaultTimeoutSec: 300,
		FirecrackerBin:    fcBin,
		HostInterface:     hostIface,
	}, rt, logger)

	// Node server
	cfg := node.Config{
		Port:            port,
		ControlPlaneURL: cpURL,
		NodeKey:         nodeKey,
		Name:            nodeName,
		DataDir:         dataDir,
		FirecrackerBin:  fcBin,
		HostInterface:   hostIface,
		MaxSandboxes:    maxSandboxes,
	}
	srv := node.NewServer(cfg, orch, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	logger.Info("gitvm-node starting",
		"name", nodeName,
		"port", port,
		"runtime", runtimeType,
		"controlPlane", cpURL,
		"maxSandboxes", maxSandboxes,
	)

	if err := srv.Start(ctx); err != nil {
		logger.Error("node error", "error", err)
		os.Exit(1)
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "gitvm-node"
	}
	return h
}
