package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// ServerConfig holds control plane configuration.
type ServerConfig struct {
	Port     int
	DataDir  string
	DBPath   string
	NodeKey  string // shared secret for node authentication
	CPURL    string // this control plane's public URL (for user-data scripts)
}

// Server is the gitvm control plane.
type Server struct {
	config    ServerConfig
	db        *DB
	sessions  *SessionStore
	providers *CloudProviderRegistry
	scaler    *Scaler
	logger    *slog.Logger
}

// NewServer creates the control plane server.
func NewServer(cfg ServerConfig, db *DB, logger *slog.Logger) *Server {
	providers := NewCloudProviderRegistry()
	sessions := NewSessionStore()
	s := &Server{
		config:    cfg,
		db:        db,
		sessions:  sessions,
		providers: providers,
		logger:    logger,
	}
	s.scaler = NewScaler(db, providers, cfg.CPURL, cfg.NodeKey, logger)
	return s
}

// RegisterProvider adds a cloud provider for auto-scaling.
func (s *Server) RegisterProvider(p CloudProvider) {
	s.providers.Register(p)
}

// Handler returns the HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- Public ---
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /auth/register", s.handleRegister)
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)

	// --- SDK API (requires API key) ---
	mux.HandleFunc("POST /v1/sandboxes", s.handleCreateSandbox)
	mux.HandleFunc("GET /v1/sandboxes", s.handleListSandboxes)
	mux.HandleFunc("GET /v1/sandboxes/{id}", s.handleGetSandbox)
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", s.handleDeleteSandbox)
	mux.HandleFunc("POST /v1/sandboxes/{id}/pause", s.handlePauseSandbox)
	mux.HandleFunc("POST /v1/sandboxes/{id}/resume", s.handleResumeSandbox)
	mux.HandleFunc("POST /v1/sandboxes/{id}/exec", s.handleExecSandbox)
	mux.HandleFunc("POST /v1/sandboxes/{id}/exec/stream", s.handleExecStreamSandbox)
	mux.HandleFunc("GET /v1/sandboxes/{id}/files", s.handleReadFile)
	mux.HandleFunc("PUT /v1/sandboxes/{id}/files", s.handleWriteFile)
	mux.HandleFunc("GET /v1/sandboxes/{id}/files/list", s.handleListFiles)

	// --- Admin API (requires session) ---
	mux.HandleFunc("GET /v1/nodes", s.handleListNodes)

	// --- Internal API (requires node key) ---
	mux.HandleFunc("POST /internal/nodes/register", s.handleNodeRegister)
	mux.HandleFunc("POST /internal/nodes/{id}/heartbeat", s.handleNodeHeartbeat)

	// Apply middleware
	var handler http.Handler = mux
	handler = s.AuthMiddleware(handler)
	handler = corsMiddleware(handler)
	handler = loggingMiddleware(s.logger)(handler)

	return handler
}

// Start starts the control plane server and background services.
func (s *Server) Start(ctx context.Context) error {
	// Start auto-scaler
	s.scaler.Start(ctx, 0)

	addr := fmt.Sprintf("0.0.0.0:%d", s.config.Port)
	s.logger.Info("gitvm control plane starting", "addr", addr)
	return http.ListenAndServe(addr, s.Handler())
}

// --- Middleware ---

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, X-Node-Key")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger.Info("request", "method", r.Method, "path", r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
}

// --- Context helpers ---

type ctxKey string

const (
	ctxKeyProject ctxKey = "project"
	ctxKeyUser    ctxKey = "user"
)

func withProject(ctx context.Context, p *Project) context.Context {
	return context.WithValue(ctx, ctxKeyProject, p)
}

func projectFromContext(ctx context.Context) *Project {
	if p, ok := ctx.Value(ctxKeyProject).(*Project); ok {
		return p
	}
	return nil
}

func withUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKeyUser, u)
}

// --- JSON helpers ---

func writeOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Code: code, Message: message})
}

func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func jsonReader(data []byte) *bytes.Reader {
	return bytes.NewReader(data)
}

func extractIP(address string) string {
	// Extract IP from "http://1.2.3.4:9090"
	addr := strings.TrimPrefix(address, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	if idx := strings.Index(addr, ":"); idx > 0 {
		return addr[:idx]
	}
	return addr
}
