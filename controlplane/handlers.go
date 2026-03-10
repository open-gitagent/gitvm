package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// --- Sandbox Handlers (SDK-facing API) ---

func (s *Server) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	project := projectFromContext(r.Context())
	if project == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no project context")
		return
	}

	var req CreateSandboxRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// Pick a node with available capacity (least loaded first)
	nodes, err := s.db.ListOnlineNodes()
	if err != nil || len(nodes) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "no_capacity", "no nodes available")
		return
	}

	var targetNode *Node
	for i := range nodes {
		if nodes[i].Running < nodes[i].MaxSandboxes {
			targetNode = &nodes[i]
			break
		}
	}
	if targetNode == nil {
		writeErr(w, http.StatusServiceUnavailable, "no_capacity", "all nodes at capacity")
		return
	}

	// Forward create request to the node
	sandboxID := "sb-" + uuid.New().String()[:8]
	nodeReq := map[string]interface{}{
		"sandboxId": sandboxID,
		"template":  req.Template,
		"vcpus":     req.VCPUs,
		"memoryMB":  req.MemoryMB,
		"timeout":   req.Timeout,
		"envVars":   req.EnvVars,
	}

	nodeResp, err := s.forwardToNode(r.Context(), targetNode, "POST", "/sandboxes", nodeReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "node_error", "failed to create sandbox: "+err.Error())
		return
	}

	// Record in DB
	timeout := 300
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	vcpus := 1
	if req.VCPUs > 0 {
		vcpus = req.VCPUs
	}
	memoryMB := 512
	if req.MemoryMB > 0 {
		memoryMB = req.MemoryMB
	}
	tmpl := "base"
	if req.Template != "" {
		tmpl = req.Template
	}

	now := time.Now().UTC()
	sandbox := &Sandbox{
		ID:         sandboxID,
		ProjectID:  project.ID,
		NodeID:     targetNode.ID,
		Template:   tmpl,
		Status:     "running",
		VCPUs:      vcpus,
		MemoryMB:   memoryMB,
		HostIP:     targetNode.PublicIP,
		TimeoutSec: timeout,
		Metadata:   req.Metadata,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Duration(timeout) * time.Second),
	}
	if err := s.db.CreateSandbox(sandbox); err != nil {
		s.logger.Error("failed to record sandbox", "error", err)
	}

	// Update node running count
	s.db.UpdateNodeHeartbeat(targetNode.ID, targetNode.Running+1)

	_ = nodeResp // we already forwarded the response data
	writeOK(w, CreateSandboxResponse{
		SandboxID: sandboxID,
		Status:    "running",
		HostIP:    targetNode.PublicIP,
	})
}

func (s *Server) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	project := projectFromContext(r.Context())
	if project == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no project context")
		return
	}

	sandboxes, err := s.db.ListSandboxes(project.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if sandboxes == nil {
		sandboxes = []Sandbox{}
	}
	writeOK(w, SandboxListResponse{Sandboxes: sandboxes})
}

func (s *Server) handleGetSandbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sandbox, err := s.db.GetSandbox(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "not_found", "sandbox not found: "+id)
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	writeOK(w, sandbox)
}

func (s *Server) handleDeleteSandbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sandbox, err := s.db.GetSandbox(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "not_found", "sandbox not found: "+id)
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}

	node, err := s.db.GetNode(sandbox.NodeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "node not found for sandbox")
		return
	}

	// Forward delete to node
	_, err = s.forwardToNode(r.Context(), node, "DELETE", "/sandboxes/"+id, nil)
	if err != nil {
		s.logger.Warn("failed to delete sandbox on node", "sandbox", id, "error", err)
	}

	s.db.DeleteSandbox(id)
	writeOK(w, map[string]string{"status": "deleted"})
}

func (s *Server) handlePauseSandbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sandbox, err := s.db.GetSandbox(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "not_found", "sandbox not found: "+id)
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}

	node, err := s.db.GetNode(sandbox.NodeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "node not found")
		return
	}

	_, err = s.forwardToNode(r.Context(), node, "POST", "/sandboxes/"+id+"/pause", nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "node_error", err.Error())
		return
	}

	s.db.UpdateSandboxStatus(id, "paused")
	writeOK(w, map[string]string{"status": "paused"})
}

func (s *Server) handleResumeSandbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sandbox, err := s.db.GetSandbox(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "not_found", "sandbox not found: "+id)
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}

	node, err := s.db.GetNode(sandbox.NodeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "node not found")
		return
	}

	_, err = s.forwardToNode(r.Context(), node, "POST", "/sandboxes/"+id+"/resume", nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "node_error", err.Error())
		return
	}

	s.db.UpdateSandboxStatus(id, "running")
	writeOK(w, map[string]string{"status": "running"})
}

// handleExecSandbox proxies exec requests to the node
func (s *Server) handleExecSandbox(w http.ResponseWriter, r *http.Request) {
	s.proxySandboxToNode(w, r, "POST", "/exec")
}

func (s *Server) handleExecStreamSandbox(w http.ResponseWriter, r *http.Request) {
	s.proxySandboxToNode(w, r, "POST", "/exec/stream")
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	s.proxySandboxToNode(w, r, "GET", "/files")
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	s.proxySandboxToNode(w, r, "PUT", "/files")
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	s.proxySandboxToNode(w, r, "GET", "/files/list")
}

// --- Node Handlers (internal API for nodes) ---

func (s *Server) handleNodeRegister(w http.ResponseWriter, r *http.Request) {
	var req NodeRegisterRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	now := time.Now().UTC()
	node := &Node{
		ID:           uuid.New().String(),
		Name:         req.Name,
		Address:      req.Address,
		PublicIP:     extractIP(req.Address),
		Provider:     "custom",
		Status:       "online",
		MaxSandboxes: req.MaxSandboxes,
		Region:       req.Region,
		LastSeen:     now,
		CreatedAt:    now,
	}
	if err := s.db.UpsertNode(node); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	s.logger.Info("node registered", "name", req.Name, "address", req.Address)
	writeOK(w, map[string]string{"nodeId": node.ID, "status": "registered"})
}

func (s *Server) handleNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	var req NodeHeartbeatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	if err := s.db.UpdateNodeHeartbeat(nodeID, req.RunningSandboxes); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.db.ListNodes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if nodes == nil {
		nodes = []Node{}
	}
	writeOK(w, nodes)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	total, used, _ := s.db.TotalCapacity()
	nodeCount := 0
	if nodes, err := s.db.ListNodes(); err == nil {
		nodeCount = len(nodes)
	}
	writeOK(w, HealthResponse{
		Status: "ok",
		Nodes:  nodeCount,
		VMs:    used,
	})
	_ = total
}

// --- Proxy Helpers ---

func (s *Server) proxySandboxToNode(w http.ResponseWriter, r *http.Request, method, subpath string) {
	id := r.PathValue("id")
	sandbox, err := s.db.GetSandbox(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "not_found", "sandbox not found: "+id)
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}

	node, err := s.db.GetNode(sandbox.NodeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "node not found for sandbox")
		return
	}

	// Build target URL: node/sandboxes/{id}/{subpath}?query
	targetURL := fmt.Sprintf("%s/sandboxes/%s%s", node.Address, id, subpath)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	proxyReq, _ := http.NewRequestWithContext(r.Context(), method, targetURL, r.Body)
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "node_unreachable", "node unreachable: "+err.Error())
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

func (s *Server) forwardToNode(ctx context.Context, node *Node, method, path string, body interface{}) (map[string]interface{}, error) {
	targetURL := node.Address + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = jsonReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("node %s unreachable: %w", node.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("node returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result, nil
}
