package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	agentpkg "github.com/open-gitagent/gitvm/agent"
	"github.com/open-gitagent/gitvm/api"
	"github.com/open-gitagent/gitvm/orchestrator"
	dockerrt "github.com/open-gitagent/gitvm/runtime/docker"
	"github.com/open-gitagent/gitvm/sdk"
	"github.com/open-gitagent/gitvm/template"
)

var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "server":
		handleServer(args)
	case "agent":
		handleAgent(args)
	case "template":
		handleTemplate(args)
	case "vm":
		handleVM(args)
	case "version":
		fmt.Println("gitvm v0.1.0")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`gitvm — Git-native Firecracker microVM platform

Usage:
  gitvm server start [--port 8080] [--data-dir /tmp/gitvm]
  gitvm agent start  [--port 49983]
  gitvm template list
  gitvm template build <name> <dockerfile-dir>
  gitvm vm create [--template base] [--vcpus 1] [--memory 512]
  gitvm vm list
  gitvm vm exec <id> <command>
  gitvm vm stop <id>
  gitvm version`)
}

// --- Server ---

func handleServer(args []string) {
	if len(args) < 1 || args[0] != "start" {
		fmt.Println("Usage: gitvm server start [--port 8080] [--data-dir /tmp/gitvm]")
		os.Exit(1)
	}

	port := 8080
	dataDir := "/tmp/gitvm"
	runtimeType := "docker"
	apiKey := os.Getenv("GITVM_API_KEY")

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--port":
			i++
			if i < len(args) {
				port, _ = strconv.Atoi(args[i])
			}
		case "--data-dir":
			i++
			if i < len(args) {
				dataDir = args[i]
			}
		case "--runtime":
			i++
			if i < len(args) {
				runtimeType = args[i]
			}
		case "--api-key":
			i++
			if i < len(args) {
				apiKey = args[i]
			}
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Default to Docker runtime for local dev
	rt := dockerrt.New(dockerrt.Config{
		DefaultImage: "ubuntu:22.04",
	}, logger)

	_ = runtimeType // TODO: support firecracker via flag
	_ = template.NewCache("") // keep import

	orch := orchestrator.New(orchestrator.Config{
		DataDir:           dataDir,
		MaxVMs:            50,
		DefaultTimeoutSec: 300,
	}, rt, logger)

	// Start cleanup goroutine
	orch.StartCleanup(ctx, 0)

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		logger.Info("shutting down...")
		orch.Shutdown(context.Background())
		os.Exit(0)
	}()

	server := api.NewServer(api.ServerConfig{
		Port:   port,
		APIKey: apiKey,
	}, orch, logger)

	if err := server.ListenAndServe(); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// --- Agent ---

func handleAgent(args []string) {
	if len(args) < 1 || args[0] != "start" {
		fmt.Println("Usage: gitvm agent start [--port 49983]")
		os.Exit(1)
	}

	port := agentpkg.DefaultPort
	for i := 1; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			i++
			port, _ = strconv.Atoi(args[i])
		}
	}

	srv := agentpkg.NewServer(logger)
	if err := srv.ListenAndServe(port); err != nil {
		logger.Error("agent error", "error", err)
		os.Exit(1)
	}
}

// --- Template ---

func handleTemplate(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: gitvm template <list|build>")
		os.Exit(1)
	}

	cache := template.NewCache("")

	switch args[0] {
	case "list":
		templates, err := cache.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(templates) == 0 {
			fmt.Println("No templates found. Build one with: gitvm template build <name> <dir>")
			return
		}
		fmt.Printf("%-20s %-10s %s\n", "NAME", "SIZE", "MODIFIED")
		for _, t := range templates {
			fmt.Printf("%-20s %-10s %s\n", t.Name, fmt.Sprintf("%dMB", t.SizeMB), t.ModTime)
		}

	case "build":
		if len(args) < 3 {
			fmt.Println("Usage: gitvm template build <name> <dockerfile-dir>")
			os.Exit(1)
		}
		builder := template.NewBuilder(cache, logger)
		if err := builder.Build(args[1], args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown template command: %s\n", args[0])
		os.Exit(1)
	}
}

// --- VM ---

func handleVM(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: gitvm vm <create|list|exec|stop>")
		os.Exit(1)
	}

	serverURL := envOrDefault("GITVM_SERVER", "http://localhost:8080")
	apiKey := os.Getenv("GITVM_API_KEY")
	client := sdk.NewClient(serverURL, apiKey)
	ctx := context.Background()

	switch args[0] {
	case "create":
		tmpl := "base"
		vcpus := 1
		memory := 512
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--template":
				i++
				if i < len(args) {
					tmpl = args[i]
				}
			case "--vcpus":
				i++
				if i < len(args) {
					vcpus, _ = strconv.Atoi(args[i])
				}
			case "--memory":
				i++
				if i < len(args) {
					memory, _ = strconv.Atoi(args[i])
				}
			}
		}
		resp, err := client.CreateVM(ctx, map[string]interface{}{
			"template": tmpl,
			"vcpus":    vcpus,
			"memoryMB": memory,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(data))

	case "list":
		resp, err := client.ListVMs(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(data))

	case "exec":
		if len(args) < 3 {
			fmt.Println("Usage: gitvm vm exec <id> <command>")
			os.Exit(1)
		}
		vmID := args[1]
		command := strings.Join(args[2:], " ")
		result, err := client.Exec(ctx, vmID, command, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if result.Stdout != "" {
			fmt.Print(result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprint(os.Stderr, result.Stderr)
		}
		os.Exit(result.ExitCode)

	case "stop":
		if len(args) < 2 {
			fmt.Println("Usage: gitvm vm stop <id>")
			os.Exit(1)
		}
		if err := client.DeleteVM(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("VM %s stopped\n", args[1])

	default:
		fmt.Fprintf(os.Stderr, "unknown vm command: %s\n", args[0])
		os.Exit(1)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
