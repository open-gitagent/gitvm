package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
)

const DefaultPort = 49983

// Defaults holds mutable agent state set via /init.
type Defaults struct {
	mu      sync.RWMutex
	EnvVars map[string]string
	User    string
	WorkDir string
	Token   string
}

func (d *Defaults) SetFrom(req InitRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if req.EnvVars != nil {
		for k, v := range req.EnvVars {
			d.EnvVars[k] = v
		}
	}
	if req.User != "" {
		d.User = req.User
	}
	if req.WorkDir != "" {
		d.WorkDir = req.WorkDir
	}
	if req.AccessToken != "" {
		d.Token = req.AccessToken
	}
}

func (d *Defaults) Snapshot() Defaults {
	d.mu.RLock()
	defer d.mu.RUnlock()
	envCopy := make(map[string]string, len(d.EnvVars))
	for k, v := range d.EnvVars {
		envCopy[k] = v
	}
	return Defaults{
		EnvVars: envCopy,
		User:    d.User,
		WorkDir: d.WorkDir,
		Token:   d.Token,
	}
}

// Server is the gitvm guest agent HTTP server.
type Server struct {
	defaults *Defaults
	logger   *slog.Logger
}

// NewServer creates a new agent server.
func NewServer(logger *slog.Logger) *Server {
	return &Server{
		defaults: &Defaults{
			EnvVars: make(map[string]string),
			User:    "root",
			WorkDir: "/home/user",
		},
		logger: logger,
	}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /init", s.handleInit)
	mux.HandleFunc("POST /exec", s.handleExec)
	mux.HandleFunc("POST /exec/stream", s.handleExecStream)
	mux.HandleFunc("GET /files", s.handleReadFile)
	mux.HandleFunc("PUT /files", s.handleWriteFile)
	mux.HandleFunc("GET /files/list", s.handleListFiles)
	mux.HandleFunc("DELETE /files", s.handleDeleteFile)
	mux.HandleFunc("POST /files/mkdir", s.handleMkdir)
	mux.HandleFunc("GET /health", s.handleHealth)

	return mux
}

// ListenAndServe starts the agent on the given port.
func (s *Server) ListenAndServe(port int) error {
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	s.logger.Info("gitvm-agent starting", "addr", addr)
	return http.ListenAndServe(addr, s.Handler())
}

// --- Handlers ---

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	var req InitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	s.defaults.SetFrom(req)
	s.logger.Info("initialized", "user", s.defaults.User, "workDir", s.defaults.WorkDir)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	snap := s.defaults.Snapshot()
	result, err := RunCommand(req, &snap)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleExecStream(w http.ResponseWriter, r *http.Request) {
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	snap := s.defaults.Snapshot()
	exitCode, err := StreamCommand(req, &snap, func(stream string, data string) {
		event := map[string]string{"stream": stream, "data": data}
		jsonData, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	})

	if err != nil {
		event := map[string]interface{}{"stream": "error", "data": err.Error()}
		jsonData, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
		return
	}

	event := map[string]interface{}{"stream": "exit", "exitCode": exitCode}
	jsonData, _ := json.Marshal(event)
	fmt.Fprintf(w, "data: %s\n\n", jsonData)
	flusher.Flush()
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	data, err := ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data)
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}

	if err := WriteFile(path, data); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	entries, err := ListDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	if err := RemovePath(path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	if err := MakeDir(path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "agent": "gitvm-agent"})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
