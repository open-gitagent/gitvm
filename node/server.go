package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/open-gitagent/gitvm/orchestrator"
)

// Config for the node server.
type Config struct {
	Port            int
	ControlPlaneURL string
	NodeKey         string
	Name            string
	DataDir         string
	FirecrackerBin  string
	HostInterface   string
	MaxSandboxes    int
}

// Server is the node-level API server.
type Server struct {
	config Config
	orch   *orchestrator.Orchestrator
	nodeID string
	logger *slog.Logger
}

// NewServer creates a node server.
func NewServer(cfg Config, orch *orchestrator.Orchestrator, logger *slog.Logger) *Server {
	return &Server{
		config: cfg,
		orch:   orch,
		logger: logger,
	}
}

// Start starts the node: registers with control plane, starts API, begins heartbeat.
func (s *Server) Start(ctx context.Context) error {
	// Register with control plane
	if s.config.ControlPlaneURL != "" {
		if err := s.register(ctx); err != nil {
			s.logger.Warn("failed to register with control plane", "error", err)
		}
		go s.heartbeatLoop(ctx)
	}

	// Start cleanup
	s.orch.StartCleanup(ctx, 0)

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		s.logger.Info("node shutting down...")
		s.orch.Shutdown(context.Background())
	}()

	addr := fmt.Sprintf("0.0.0.0:%d", s.config.Port)
	s.logger.Info("gitvm-node starting", "addr", addr, "name", s.config.Name)
	return http.ListenAndServe(addr, s.Handler())
}

// Handler returns the HTTP handler with all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Sandbox lifecycle
	mux.HandleFunc("POST /sandboxes", s.handleCreate)
	mux.HandleFunc("GET /sandboxes", s.handleList)
	mux.HandleFunc("GET /sandboxes/{id}", s.handleGet)
	mux.HandleFunc("DELETE /sandboxes/{id}", s.handleDelete)
	mux.HandleFunc("POST /sandboxes/{id}/pause", s.handlePause)
	mux.HandleFunc("POST /sandboxes/{id}/resume", s.handleResume)

	// Snapshots
	mux.HandleFunc("POST /sandboxes/{id}/snapshot", s.handleSnapshot)
	mux.HandleFunc("POST /snapshots/{snapshotId}/restore", s.handleRestore)
	mux.HandleFunc("DELETE /snapshots/{snapshotId}", s.handleDeleteSnapshot)
	mux.HandleFunc("GET /snapshots", s.handleListSnapshots)

	// Proxy to guest agent
	mux.HandleFunc("POST /sandboxes/{id}/exec", s.handleExec)
	mux.HandleFunc("POST /sandboxes/{id}/exec/stream", s.handleExecStream)
	mux.HandleFunc("GET /sandboxes/{id}/files", s.handleReadFile)
	mux.HandleFunc("PUT /sandboxes/{id}/files", s.handleWriteFile)
	mux.HandleFunc("GET /sandboxes/{id}/files/list", s.handleListFiles)

	// Health
	mux.HandleFunc("GET /health", s.handleHealth)

	return mux
}

// --- Sandbox Lifecycle ---

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SandboxID string            `json:"sandboxId"`
		Template  string            `json:"template"`
		VCPUs     int               `json:"vcpus"`
		MemoryMB  int               `json:"memoryMB"`
		Timeout   int               `json:"timeout"`
		EnvVars   map[string]string `json:"envVars"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	instance, err := s.orch.Create(r.Context(), orchestrator.CreateRequest{
		Template: req.Template,
		VCPUs:    req.VCPUs,
		MemoryMB: req.MemoryMB,
		Timeout:  req.Timeout,
		EnvVars:  req.EnvVars,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"sandboxId": instance.ID,
		"status":    instance.State,
		"hostIp":    instance.GuestIP,
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	instances := s.orch.List()
	resp := make([]map[string]interface{}, len(instances))
	for i, inst := range instances {
		resp[i] = map[string]interface{}{
			"sandboxId": inst.ID,
			"status":    inst.State,
			"template":  inst.Template,
			"vcpus":     inst.VCPUs,
			"memoryMB":  inst.MemoryMB,
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sandboxes": resp})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst := s.orch.Get(id)
	if inst == nil {
		writeErr(w, http.StatusNotFound, "sandbox not found: "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sandboxId": inst.ID,
		"status":    inst.State,
		"template":  inst.Template,
	})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.orch.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst := s.orch.Get(id)
	if inst == nil {
		writeErr(w, http.StatusNotFound, "sandbox not found: "+id)
		return
	}
	if inst.State != "running" {
		writeErr(w, http.StatusConflict, fmt.Sprintf("sandbox %s is not running (status: %s)", id, inst.State))
		return
	}
	inst.State = "paused"
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst := s.orch.Get(id)
	if inst == nil {
		writeErr(w, http.StatusNotFound, "sandbox not found: "+id)
		return
	}
	if inst.State != "paused" {
		writeErr(w, http.StatusConflict, fmt.Sprintf("sandbox %s is not paused (status: %s)", id, inst.State))
		return
	}
	inst.State = "running"
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

// --- Snapshots ---

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		SnapshotID string `json:"snapshotId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SnapshotID == "" {
		req.SnapshotID = fmt.Sprintf("snap-%s-%d", id, time.Now().Unix())
	}
	if err := s.orch.Snapshot(r.Context(), id, req.SnapshotID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"snapshotId": req.SnapshotID,
		"sandboxId":  id,
		"status":     "created",
	})
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	snapshotID := r.PathValue("snapshotId")
	var req struct {
		Timeout int `json:"timeout"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	instance, err := s.orch.Restore(r.Context(), snapshotID, req.Timeout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"sandboxId":  instance.ID,
		"snapshotId": snapshotID,
		"status":     instance.State,
		"hostIp":     instance.GuestIP,
	})
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshotID := r.PathValue("snapshotId")
	if err := s.orch.DeleteSnapshot(r.Context(), snapshotID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.URL.Query().Get("sandboxId")
	snapshots, err := s.orch.ListSnapshots(r.Context(), sandboxID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"snapshots": snapshots})
}

// --- Proxy to Guest Agent ---

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	s.proxyToAgent(w, r, "POST", "/exec")
}

func (s *Server) handleExecStream(w http.ResponseWriter, r *http.Request) {
	s.proxyToAgent(w, r, "POST", "/exec/stream")
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	s.proxyToAgent(w, r, "GET", "/files")
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	s.proxyToAgent(w, r, "PUT", "/files")
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	s.proxyToAgent(w, r, "GET", "/files/list")
}

func (s *Server) proxyToAgent(w http.ResponseWriter, r *http.Request, method, path string) {
	id := r.PathValue("id")
	inst := s.orch.Get(id)
	if inst == nil {
		writeErr(w, http.StatusNotFound, "sandbox not found: "+id)
		return
	}

	if inst.State != "running" {
		writeErr(w, http.StatusConflict, fmt.Sprintf("exec: sandbox %s is not running (status: %s)", id, inst.State))
		return
	}

	targetURL := inst.AgentURL + path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	proxyReq, _ := http.NewRequestWithContext(r.Context(), method, targetURL, r.Body)
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// --- Health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "ok",
		"name":       s.config.Name,
		"sandboxes":  len(s.orch.List()),
		"maxSandboxes": s.config.MaxSandboxes,
	})
}

// --- Control Plane Communication ---

func (s *Server) register(ctx context.Context) error {
	body := map[string]interface{}{
		"name":         s.config.Name,
		"address":      fmt.Sprintf("http://0.0.0.0:%d", s.config.Port), // will be replaced by public IP
		"maxSandboxes": s.config.MaxSandboxes,
		"nodeKey":      s.config.NodeKey,
	}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, "POST", s.config.ControlPlaneURL+"/internal/nodes/register", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Key", s.config.NodeKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	s.nodeID = result["nodeId"]
	s.logger.Info("registered with control plane", "nodeId", s.nodeID)
	return nil
}

func (s *Server) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sendHeartbeat(ctx)
		}
	}
}

func (s *Server) sendHeartbeat(ctx context.Context) {
	if s.nodeID == "" {
		return
	}
	body := map[string]interface{}{
		"runningSandboxes": len(s.orch.List()),
	}
	data, _ := json.Marshal(body)
	url := fmt.Sprintf("%s/internal/nodes/%s/heartbeat", s.config.ControlPlaneURL, s.nodeID)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Key", s.config.NodeKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.logger.Warn("heartbeat failed", "error", err)
		return
	}
	resp.Body.Close()
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
