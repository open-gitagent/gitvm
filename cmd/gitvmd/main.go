package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/open-gitagent/gitvm/controlplane"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Defaults (env vars override, CLI flags override both)
	homeDir, _ := os.UserHomeDir()
	defaultDataDir := homeDir + "/.gitvmd"

	port := envInt("GITVMD_PORT", 8080)
	dataDir := envStr("GITVMD_DATA_DIR", defaultDataDir)
	nodeKey := envStr("GITVMD_NODE_KEY", "change-me-in-production")

	// Parse CLI flags: gitvmd start [--port N] [--node-key K] [--data-dir D]
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "start":
			// no-op, just skip
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

	// Ensure data dir exists
	os.MkdirAll(dataDir, 0o755)

	// Open database
	db, err := controlplane.OpenDB(dbPath)
	if err != nil {
		logger.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Create server
	cfg := controlplane.ServerConfig{
		Port:    port,
		DataDir: dataDir,
		DBPath:  dbPath,
		NodeKey: nodeKey,
		CPURL:   cpURL,
	}
	server := controlplane.NewServer(cfg, db, logger)

	// Register cloud providers from env vars
	registerProviders(server, logger)

	// Start with graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	logger.Info("gitvmd starting",
		"port", port,
		"db", dbPath,
	)

	if err := server.Start(ctx); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

func registerProviders(server *controlplane.Server, logger *slog.Logger) {
	// AWS
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		server.RegisterProvider(&controlplane.AWSProvider{
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			DefaultRegion:   envStr("AWS_DEFAULT_REGION", "us-east-1"),
			SecurityGroupID: os.Getenv("AWS_SECURITY_GROUP_ID"),
			SubnetID:        os.Getenv("AWS_SUBNET_ID"),
			KeyName:         os.Getenv("AWS_KEY_NAME"),
			AMI:             os.Getenv("AWS_AMI"),
		})
		logger.Info("registered AWS provider")
	}

	// GCP
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

	// Azure
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
