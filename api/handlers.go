package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/open-gitagent/gitvm/orchestrator"
)

// Handlers holds the HTTP handlers for the API.
type Handlers struct {
	orch *orchestrator.Orchestrator
}

// NewHandlers creates API handlers backed by the given orchestrator.
func NewHandlers(orch *orchestrator.Orchestrator) *Handlers {
	return &Handlers{orch: orch}
}

// CreateVM handles POST /v1/vms.
func (h *Handlers) CreateVM(w http.ResponseWriter, r *http.Request) {
	var req CreateVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	instance, err := h.orch.Create(r.Context(), orchestrator.CreateRequest{
		Template: req.Template,
		VCPUs:    req.VCPUs,
		MemoryMB: req.MemoryMB,
		Timeout:  req.Timeout,
		Metadata: req.Metadata,
		EnvVars:  req.EnvVars,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toVMResponse(instance))
}

// ListVMs handles GET /v1/vms.
func (h *Handlers) ListVMs(w http.ResponseWriter, r *http.Request) {
	instances := h.orch.List()
	resp := make([]VMResponse, len(instances))
	for i, inst := range instances {
		resp[i] = toVMResponse(inst)
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetVM handles GET /v1/vms/{id}.
func (h *Handlers) GetVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	instance := h.orch.Get(id)
	if instance == nil {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}
	writeJSON(w, http.StatusOK, toVMResponse(instance))
}

// DeleteVM handles DELETE /v1/vms/{id}.
func (h *Handlers) DeleteVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.orch.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ExecVM handles POST /v1/vms/{id}/exec — proxies to the VM's agent.
func (h *Handlers) ExecVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	instance := h.orch.Get(id)
	if instance == nil {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}

	// Proxy the request body to the agent
	resp, err := http.Post(instance.AgentURL+"/exec", "application/json", r.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ExecStreamVM handles POST /v1/vms/{id}/exec/stream — proxies SSE from agent.
func (h *Handlers) ExecStreamVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	instance := h.orch.Get(id)
	if instance == nil {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}

	resp, err := http.Post(instance.AgentURL+"/exec/stream", "application/json", r.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		io.Copy(w, resp.Body)
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}
}

// ReadFile handles GET /v1/vms/{id}/files — proxies to agent.
func (h *Handlers) ReadFile(w http.ResponseWriter, r *http.Request) {
	h.proxyGetToAgent(w, r, "/files")
}

// WriteFile handles PUT /v1/vms/{id}/files — proxies to agent.
func (h *Handlers) WriteFile(w http.ResponseWriter, r *http.Request) {
	h.proxyBodyToAgent(w, r, "PUT", "/files")
}

// ListFiles handles GET /v1/vms/{id}/files/list — proxies to agent.
func (h *Handlers) ListFiles(w http.ResponseWriter, r *http.Request) {
	h.proxyGetToAgent(w, r, "/files/list")
}

// DeleteFile handles DELETE /v1/vms/{id}/files — proxies to agent.
func (h *Handlers) DeleteFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	instance := h.orch.Get(id)
	if instance == nil {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}

	agentURL := instance.AgentURL + "/files?path=" + r.URL.Query().Get("path")
	req, _ := http.NewRequest("DELETE", agentURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// Mkdir handles POST /v1/vms/{id}/files/mkdir — proxies to agent.
func (h *Handlers) Mkdir(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	instance := h.orch.Get(id)
	if instance == nil {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}

	agentURL := instance.AgentURL + "/files/mkdir?path=" + r.URL.Query().Get("path")
	resp, err := http.Post(agentURL, "application/json", nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// Health handles GET /health.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status: "ok",
		VMs:    len(h.orch.List()),
	})
}

// --- Proxy helpers ---

func (h *Handlers) proxyGetToAgent(w http.ResponseWriter, r *http.Request, agentPath string) {
	id := r.PathValue("id")
	instance := h.orch.Get(id)
	if instance == nil {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}

	agentURL := instance.AgentURL + agentPath + "?" + r.URL.RawQuery
	resp, err := http.Get(agentURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		for _, vv := range v {
			w.Header().Set(k, vv)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (h *Handlers) proxyBodyToAgent(w http.ResponseWriter, r *http.Request, method string, agentPath string) {
	id := r.PathValue("id")
	instance := h.orch.Get(id)
	if instance == nil {
		writeError(w, http.StatusNotFound, "VM not found")
		return
	}

	agentURL := instance.AgentURL + agentPath + "?" + r.URL.RawQuery
	req, _ := http.NewRequest(method, agentURL, r.Body)
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// --- Helpers ---

func toVMResponse(inst *orchestrator.VMInstance) VMResponse {
	return VMResponse{
		ID:        inst.ID,
		State:     inst.State,
		Template:  inst.Template,
		VCPUs:     inst.VCPUs,
		MemoryMB:  inst.MemoryMB,
		CreatedAt: inst.CreatedAt.Format(time.RFC3339),
		ExpiresAt: inst.ExpiresAt.Format(time.RFC3339),
		Metadata:  inst.Metadata,
		AgentURL:  inst.AgentURL,
		GuestIP:   inst.GuestIP,
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
