package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/open-gitagent/gitvm/agent"
	"github.com/open-gitagent/gitvm/runtime"
)

// DockerRuntime creates sandboxes as Docker containers.
// Each container runs the gitvm-agent binary on port 49983.
type DockerRuntime struct {
	// AgentBinaryPath is the path to the gitvm-agent binary on the host.
	// If empty, the container image must include it.
	AgentBinaryPath string

	// DefaultImage is the Docker image to use if no template is specified.
	DefaultImage string

	// Network is the Docker network to use (default: "bridge").
	Network string

	logger *slog.Logger
}

// Config for DockerRuntime.
type Config struct {
	AgentBinaryPath string
	DefaultImage    string
	Network         string
}

// New creates a new DockerRuntime.
func New(cfg Config, logger *slog.Logger) *DockerRuntime {
	if cfg.DefaultImage == "" {
		cfg.DefaultImage = "ubuntu:22.04"
	}
	if cfg.Network == "" {
		cfg.Network = "bridge"
	}
	return &DockerRuntime{
		AgentBinaryPath: cfg.AgentBinaryPath,
		DefaultImage:    cfg.DefaultImage,
		Network:         cfg.Network,
		logger:          logger,
	}
}

func (d *DockerRuntime) Name() string { return "docker" }

func (d *DockerRuntime) Create(ctx context.Context, opts runtime.CreateOpts) (*runtime.Sandbox, error) {
	image := opts.Template
	if image == "" {
		image = d.DefaultImage
	}

	containerName := "gitvm-" + opts.ID
	agentPort := fmt.Sprintf("%d", agent.DefaultPort)

	// Build docker run command
	args := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", opts.ID,
		"--network", d.Network,
		"-p", agentPort, // expose agent port
		"--label", "gitvm=sandbox",
		"--label", "gitvm.id=" + opts.ID,
	}

	// Resource limits
	if opts.VCPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%d", opts.VCPUs))
	}
	if opts.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", opts.MemoryMB))
	}

	// Environment variables
	for k, v := range opts.EnvVars {
		args = append(args, "-e", k+"="+v)
	}

	// Mount agent binary if provided
	if d.AgentBinaryPath != "" {
		args = append(args, "-v", d.AgentBinaryPath+":/usr/local/bin/gitvm:ro")
	}

	// Image and command: start the agent
	args = append(args, image,
		"/bin/sh", "-c",
		fmt.Sprintf("/usr/local/bin/gitvm agent start --port %d", agent.DefaultPort),
	)

	d.logger.Info("creating docker sandbox", "id", opts.ID, "image", image)

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, string(out))
	}

	// Get the agent URL. On Docker Desktop (macOS/Windows), container IPs
	// are not routable from the host, so we use the published port instead.
	agentURL, ip, err := d.resolveAgentURL(ctx, containerName)
	if err != nil {
		exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()
		return nil, fmt.Errorf("resolve agent URL: %w", err)
	}

	// Wait for agent to be healthy
	if err := d.waitForAgent(agentURL); err != nil {
		d.logger.Warn("agent not ready, sandbox may need manual agent start", "id", opts.ID, "error", err)
	}

	return &runtime.Sandbox{
		ID:       opts.ID,
		State:    "running",
		Runtime:  "docker",
		IP:       ip,
		AgentURL: agentURL,
	}, nil
}

func (d *DockerRuntime) Start(ctx context.Context, id string) error {
	return exec.CommandContext(ctx, "docker", "start", "gitvm-"+id).Run()
}

func (d *DockerRuntime) Stop(ctx context.Context, id string) error {
	return exec.CommandContext(ctx, "docker", "stop", "gitvm-"+id).Run()
}

func (d *DockerRuntime) Destroy(ctx context.Context, id string) error {
	return exec.CommandContext(ctx, "docker", "rm", "-f", "gitvm-"+id).Run()
}

func (d *DockerRuntime) Pause(ctx context.Context, id string) error {
	return exec.CommandContext(ctx, "docker", "pause", "gitvm-"+id).Run()
}

func (d *DockerRuntime) Resume(ctx context.Context, id string) error {
	return exec.CommandContext(ctx, "docker", "unpause", "gitvm-"+id).Run()
}

func (d *DockerRuntime) Get(ctx context.Context, id string) (*runtime.Sandbox, error) {
	containerName := "gitvm-" + id
	out, err := exec.CommandContext(ctx, "docker", "inspect", containerName, "--format", "{{.State.Status}}").Output()
	if err != nil {
		return nil, fmt.Errorf("sandbox %s not found", id)
	}

	state := strings.TrimSpace(string(out))
	dockerToGitvm := map[string]string{
		"running": "running",
		"paused":  "paused",
		"exited":  "stopped",
		"created": "creating",
	}
	gitvmState := dockerToGitvm[state]
	if gitvmState == "" {
		gitvmState = state
	}

	agentURL, ip, _ := d.resolveAgentURL(ctx, containerName)

	return &runtime.Sandbox{
		ID:       id,
		State:    gitvmState,
		Runtime:  "docker",
		IP:       ip,
		AgentURL: agentURL,
	}, nil
}

func (d *DockerRuntime) List(ctx context.Context) ([]*runtime.Sandbox, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=gitvm=sandbox",
		"--format", "{{.Names}}\t{{.State}}\t{{.ID}}",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}

	var sandboxes []*runtime.Sandbox
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		state := parts[1]

		id := strings.TrimPrefix(name, "gitvm-")
		agentURL, ip, _ := d.resolveAgentURL(ctx, name)

		sandboxes = append(sandboxes, &runtime.Sandbox{
			ID:       id,
			State:    state,
			Runtime:  "docker",
			IP:       ip,
			AgentURL: agentURL,
		})
	}
	return sandboxes, nil
}

func (d *DockerRuntime) AgentURL(ctx context.Context, id string) (string, error) {
	agentURL, _, err := d.resolveAgentURL(ctx, "gitvm-"+id)
	return agentURL, err
}

// --- Snapshots ---

// Snapshot commits the container's current state as a Docker image.
// The container is paused during commit to ensure consistency, then resumed.
func (d *DockerRuntime) Snapshot(ctx context.Context, id string, snapshotID string) error {
	containerName := "gitvm-" + id
	imageName := "gitvm-snap-" + snapshotID

	// Pause container for consistent snapshot
	_ = exec.CommandContext(ctx, "docker", "pause", containerName).Run()
	defer exec.CommandContext(ctx, "docker", "unpause", containerName).Run()

	// Commit container state as a new image
	out, err := exec.CommandContext(ctx, "docker", "commit",
		"--message", fmt.Sprintf("snapshot of %s", id),
		containerName, imageName,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker commit: %w: %s", err, string(out))
	}

	d.logger.Info("snapshot created", "sandbox", id, "snapshot", snapshotID, "image", imageName)
	return nil
}

// Restore creates a new sandbox from a snapshot image.
// The new container runs the agent and has all filesystem state from the snapshot.
func (d *DockerRuntime) Restore(ctx context.Context, snapshotID string, newID string) (*runtime.Sandbox, error) {
	imageName := "gitvm-snap-" + snapshotID
	containerName := "gitvm-" + newID
	agentPort := fmt.Sprintf("%d", agent.DefaultPort)

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", newID,
		"--network", d.Network,
		"-p", agentPort,
		"--label", "gitvm=sandbox",
		"--label", "gitvm.id=" + newID,
		"--label", "gitvm.snapshot=" + snapshotID,
	}

	// Mount agent binary (snapshot image may have stale binary)
	if d.AgentBinaryPath != "" {
		args = append(args, "-v", d.AgentBinaryPath+":/usr/local/bin/gitvm:ro")
	}

	args = append(args, imageName,
		"/bin/sh", "-c",
		fmt.Sprintf("/usr/local/bin/gitvm agent start --port %d", agent.DefaultPort),
	)

	d.logger.Info("restoring from snapshot", "snapshot", snapshotID, "newId", newID)

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run from snapshot: %w: %s", err, string(out))
	}

	agentURL, ip, err := d.resolveAgentURL(ctx, containerName)
	if err != nil {
		exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()
		return nil, fmt.Errorf("resolve agent URL: %w", err)
	}

	if err := d.waitForAgent(agentURL); err != nil {
		d.logger.Warn("agent not ready after restore", "id", newID, "error", err)
	}

	return &runtime.Sandbox{
		ID:       newID,
		State:    "running",
		Runtime:  "docker",
		IP:       ip,
		AgentURL: agentURL,
	}, nil
}

// DeleteSnapshot removes a snapshot image.
func (d *DockerRuntime) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	imageName := "gitvm-snap-" + snapshotID
	out, err := exec.CommandContext(ctx, "docker", "rmi", imageName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker rmi: %w: %s", err, string(out))
	}
	d.logger.Info("snapshot deleted", "snapshot", snapshotID)
	return nil
}

// ListSnapshots returns all snapshots for a given sandbox ID.
func (d *DockerRuntime) ListSnapshots(ctx context.Context, id string) ([]runtime.SnapshotInfo, error) {
	// List all gitvm snapshot images
	out, err := exec.CommandContext(ctx, "docker", "images",
		"--filter", "reference=gitvm-snap-*",
		"--format", "{{.Repository}}\t{{.CreatedAt}}\t{{.Size}}",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("docker images: %w", err)
	}

	var snapshots []runtime.SnapshotInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 1 {
			continue
		}
		snapID := strings.TrimPrefix(parts[0], "gitvm-snap-")

		// Check if this snapshot belongs to the requested sandbox by inspecting labels
		labelOut, _ := exec.CommandContext(ctx, "docker", "inspect", parts[0]+":latest",
			"--format", "{{index .Config.Labels \"gitvm.id\"}}",
		).Output()
		// docker commit preserves the original container labels
		originID := strings.TrimSpace(string(labelOut))

		// If id filter is provided and doesn't match, skip
		if id != "" && originID != "" && originID != id {
			continue
		}

		info := runtime.SnapshotInfo{
			ID:        snapID,
			SandboxID: originID,
		}
		if len(parts) >= 2 {
			info.CreatedAt = parts[1]
		}
		snapshots = append(snapshots, info)
	}
	return snapshots, nil
}

// --- Helpers ---

// resolveAgentURL returns the agent URL reachable from the host.
// On Docker Desktop (macOS/Windows), container IPs are not routable,
// so we use the published host port via localhost instead.
func (d *DockerRuntime) resolveAgentURL(ctx context.Context, containerName string) (agentURL, ip string, err error) {
	// First try published port (works on Docker Desktop for Mac/Windows)
	hostPort, portErr := d.getHostPort(ctx, containerName)
	if portErr == nil && hostPort != "" {
		ip = "127.0.0.1"
		agentURL = fmt.Sprintf("http://127.0.0.1:%s", hostPort)
		return agentURL, ip, nil
	}

	// Fallback to container IP (works on Linux where IPs are routable)
	ip, err = d.getContainerIP(ctx, containerName)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve agent URL: no published port and no container IP: %w", err)
	}
	agentURL = fmt.Sprintf("http://%s:%d", ip, agent.DefaultPort)
	return agentURL, ip, nil
}

// getHostPort returns the host port mapped to the agent port inside the container.
func (d *DockerRuntime) getHostPort(ctx context.Context, containerName string) (string, error) {
	portSpec := fmt.Sprintf("%d/tcp", agent.DefaultPort)
	out, err := exec.CommandContext(ctx, "docker", "port", containerName, portSpec).Output()
	if err != nil {
		return "", err
	}
	// Output is like "0.0.0.0:51830" or "[::]:51830"
	line := strings.TrimSpace(string(out))
	// Take the first line if multiple
	if idx := strings.Index(line, "\n"); idx >= 0 {
		line = line[:idx]
	}
	// Extract port after the last colon
	if idx := strings.LastIndex(line, ":"); idx >= 0 {
		return line[idx+1:], nil
	}
	return "", fmt.Errorf("unexpected docker port output: %s", line)
}

func (d *DockerRuntime) getContainerIP(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", name,
		"--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
	).Output()
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("no IP for container %s", name)
	}
	return ip, nil
}

func (d *DockerRuntime) waitForAgent(agentURL string) error {
	healthURL := agentURL + "/health"
	for i := 0; i < 30; i++ {
		resp, err := http.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("agent at %s did not become healthy after 15s", agentURL)
}

// BuildAgentImage builds a Docker image with gitvm-agent baked in.
// This is a helper for when you don't want to mount the binary.
func BuildAgentImage(ctx context.Context, agentBinaryPath, imageName string) error {
	dockerfile := fmt.Sprintf(`FROM ubuntu:22.04
RUN apt-get update && apt-get install -y git curl && rm -rf /var/lib/apt/lists/*
COPY gitvm-agent /usr/local/bin/gitvm-agent
RUN chmod +x /usr/local/bin/gitvm-agent
CMD ["gitvm-agent", "start", "--port", "49983"]
`)
	_ = dockerfile
	_ = json.Marshal // ensure import used

	// For now, we use the volume mount approach
	return fmt.Errorf("not implemented: use volume mount or pre-built image instead")
}
