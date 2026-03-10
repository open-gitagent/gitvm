package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/open-gitagent/gitvm/controlplane"
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
	case "start":
		handleStart(args)
	case "node":
		handleNode(args)
	case "vm":
		handleVM(args)
	case "status":
		handleStatus(args)
	case "version":
		fmt.Println("gitvmd v0.1.0")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`gitvmd — gitvm control plane

Usage:
  gitvmd start    [--port 7070] [--node-key KEY]    Start the control plane server
  gitvmd status                                      Show cluster status
  gitvmd node     provision [--provider aws] [--runtime docker] [--instance-type t3.medium]
  gitvmd node     list                               List all nodes
  gitvmd node     terminate <node-id-or-provider-id> Terminate a node
  gitvmd vm       list                               List all sandboxes
  gitvmd version                                     Show version

Environment variables:
  AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY           AWS credentials
  GCP_PROJECT_ID, GCP_ZONE                           GCP credentials
  AZURE_RESOURCE_GROUP, AZURE_SUBSCRIPTION_ID        Azure credentials
  GITVMD_PORT, GITVMD_NODE_KEY, GITVMD_DATA_DIR      Server config`)
}

// ============================================================
// gitvmd start
// ============================================================

func handleStart(args []string) {
	homeDir, _ := os.UserHomeDir()
	defaultDataDir := homeDir + "/.gitvmd"

	port := envInt("GITVMD_PORT", 7070)
	dataDir := envStr("GITVMD_DATA_DIR", defaultDataDir)
	nodeKey := envStr("GITVMD_NODE_KEY", "change-me-in-production")

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			i++
			if i < len(args) {
				port, _ = strconv.Atoi(args[i])
			}
		case "--node-key":
			i++
			if i < len(args) {
				nodeKey = args[i]
			}
		case "--data-dir":
			i++
			if i < len(args) {
				dataDir = args[i]
			}
		}
	}

	dbPath := envStr("GITVMD_DB_PATH", dataDir+"/gitvmd.db")
	cpURL := envStr("GITVMD_URL", fmt.Sprintf("http://localhost:%d", port))

	os.MkdirAll(dataDir, 0o755)

	db, err := controlplane.OpenDB(dbPath)
	if err != nil {
		logger.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	cfg := controlplane.ServerConfig{
		Port:    port,
		DataDir: dataDir,
		DBPath:  dbPath,
		NodeKey: nodeKey,
		CPURL:   cpURL,
	}
	server := controlplane.NewServer(cfg, db, logger)
	registerProviders(server, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	logger.Info("gitvmd starting", "port", port, "db", dbPath)

	if err := server.Start(ctx); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// ============================================================
// gitvmd node provision|list|terminate
// ============================================================

func handleNode(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: gitvmd node <provision|list|terminate>")
		os.Exit(1)
	}

	switch args[0] {
	case "provision":
		handleNodeProvision(args[1:])
	case "list", "ls":
		handleNodeList()
	case "terminate", "rm", "delete":
		if len(args) < 2 {
			fmt.Println("Usage: gitvmd node terminate <node-id>")
			os.Exit(1)
		}
		handleNodeTerminate(args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown node command: %s\n", args[0])
		os.Exit(1)
	}
}

func handleNodeProvision(args []string) {
	provider := "aws"
	runtime := "docker"
	instanceType := ""
	region := ""
	name := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--provider", "-p":
			i++
			if i < len(args) {
				provider = args[i]
			}
		case "--runtime", "-r":
			i++
			if i < len(args) {
				runtime = args[i]
			}
		case "--instance-type", "-t":
			i++
			if i < len(args) {
				instanceType = args[i]
			}
		case "--region":
			i++
			if i < len(args) {
				region = args[i]
			}
		case "--name":
			i++
			if i < len(args) {
				name = args[i]
			}
		}
	}

	body := map[string]string{
		"provider": provider,
		"runtime":  runtime,
	}
	if instanceType != "" {
		body["instanceType"] = instanceType
	}
	if region != "" {
		body["region"] = region
	}
	if name != "" {
		body["name"] = name
	}

	fmt.Printf("Provisioning %s node on %s (runtime: %s)...\n", instanceType, provider, runtime)
	if instanceType == "" {
		fmt.Println("  (using default instance type for runtime)")
	}

	resp, err := apiPost("/v1/nodes/provision", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("  Node:     %s\n", resp["name"])
	fmt.Printf("  ID:       %s\n", resp["nodeId"])
	fmt.Printf("  IP:       %s\n", resp["publicIp"])
	fmt.Printf("  Provider: %s (%s)\n", resp["providerId"], resp["instanceType"])
	fmt.Printf("  Region:   %s\n", resp["region"])
	fmt.Printf("  Status:   %s\n", resp["status"])
	fmt.Println()
	fmt.Println("Node is booting. It will register with the control plane in 2-5 minutes.")
	fmt.Println("Check status: gitvmd node list")
}

func handleNodeList() {
	resp, err := apiGet("/v1/nodes")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	nodes, ok := resp.([]interface{})
	if !ok || len(nodes) == 0 {
		fmt.Println("No nodes registered.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "NAME\tSTATUS\tIP\tPROVIDER\tINSTANCE\tSANDBOXES\tREGION\n")
	for _, n := range nodes {
		node := n.(map[string]interface{})
		running := 0
		max := 0
		if v, ok := node["runningSandboxes"]; ok {
			running = int(v.(float64))
		}
		if v, ok := node["maxSandboxes"]; ok {
			max = int(v.(float64))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			node["name"],
			node["status"],
			node["publicIp"],
			node["provider"],
			node["instanceType"],
			running, max,
			node["region"],
		)
	}
	w.Flush()
}

func handleNodeTerminate(nodeID string) {
	fmt.Printf("Terminating node %s...\n", nodeID)

	// TODO: Add DELETE /v1/nodes/:id endpoint to control plane
	// For now, just inform the user
	fmt.Println("Not yet implemented via CLI. Terminate via cloud console or:")
	fmt.Printf("  aws ec2 terminate-instances --instance-ids %s\n", nodeID)
}

// ============================================================
// gitvmd vm list
// ============================================================

func handleVM(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: gitvmd vm <list>")
		os.Exit(1)
	}

	switch args[0] {
	case "list", "ls":
		handleVMList()
	default:
		fmt.Fprintf(os.Stderr, "unknown vm command: %s\n", args[0])
		os.Exit(1)
	}
}

func handleVMList() {
	// This requires auth (API key). For admin, we query nodes directly.
	resp, err := apiGet("/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	health := resp.(map[string]interface{})
	fmt.Printf("Nodes: %v   Sandboxes: %v\n", health["nodes"], health["vms"])
}

// ============================================================
// gitvmd status
// ============================================================

func handleStatus(args []string) {
	serverURL := cpURL()
	resp, err := http.Get(serverURL + "/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot reach control plane at %s: %v\n", serverURL, err)
		fmt.Println("Is gitvmd running? Start with: gitvmd start")
		os.Exit(1)
	}
	defer resp.Body.Close()

	var health map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&health)

	fmt.Printf("Control Plane: %s\n", serverURL)
	fmt.Printf("Status:        %s\n", health["status"])
	fmt.Printf("Nodes:         %v\n", health["nodes"])
	fmt.Printf("Sandboxes:     %v\n", health["vms"])

	// Also list nodes
	fmt.Println()
	handleNodeList()
}

// ============================================================
// API helpers — talk to the running gitvmd server
// ============================================================

func cpURL() string {
	url := envStr("GITVMD_URL", "")
	if url != "" {
		return url
	}
	port := envStr("GITVMD_PORT", "7070")
	return "http://localhost:" + port
}

func apiGet(path string) (interface{}, error) {
	resp, err := http.Get(cpURL() + path)
	if err != nil {
		return nil, fmt.Errorf("cannot reach control plane: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result, nil
}

func apiPost(path string, body interface{}) (map[string]interface{}, error) {
	data, _ := json.Marshal(body)
	resp, err := http.Post(cpURL()+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cannot reach control plane: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result map[string]interface{}
	json.Unmarshal(respBody, &result)
	return result, nil
}

// ============================================================
// Provider registration (for start command)
// ============================================================

func registerProviders(server *controlplane.Server, logger *slog.Logger) {
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		server.RegisterProvider(&controlplane.AWSProvider{
			AccessKeyID:    os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			DefaultRegion:  envStr("AWS_DEFAULT_REGION", "us-east-1"),
			SecurityGroupID: os.Getenv("AWS_SECURITY_GROUP_ID"),
			SubnetID:       os.Getenv("AWS_SUBNET_ID"),
			KeyName:        os.Getenv("AWS_KEY_NAME"),
			AMI:            os.Getenv("AWS_AMI"),
		})
		logger.Info("registered AWS provider")
	}

	if os.Getenv("GCP_PROJECT_ID") != "" {
		server.RegisterProvider(&controlplane.GCPProvider{
			ProjectID:    os.Getenv("GCP_PROJECT_ID"),
			Zone:         envStr("GCP_ZONE", "us-central1-a"),
			Network:      os.Getenv("GCP_NETWORK"),
			Subnet:       os.Getenv("GCP_SUBNET"),
			MachineImage: envStr("GCP_IMAGE", "projects/ubuntu-os-cloud/global/images/family/ubuntu-2204-lts"),
		})
		logger.Info("registered GCP provider")
	}

	if os.Getenv("AZURE_RESOURCE_GROUP") != "" {
		server.RegisterProvider(&controlplane.AzureProvider{
			SubscriptionID: os.Getenv("AZURE_SUBSCRIPTION_ID"),
			ResourceGroup:  os.Getenv("AZURE_RESOURCE_GROUP"),
			Location:       envStr("AZURE_LOCATION", "eastus"),
			Image:          envStr("AZURE_IMAGE", "Canonical:0001-com-ubuntu-server-jammy:22_04-lts:latest"),
			VNet:           os.Getenv("AZURE_VNET"),
			Subnet:         os.Getenv("AZURE_SUBNET"),
		})
		logger.Info("registered Azure provider")
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
