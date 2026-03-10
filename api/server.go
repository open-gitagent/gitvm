package api

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/open-gitagent/gitvm/orchestrator"
)

// ServerConfig holds API server configuration.
type ServerConfig struct {
	Port   int
	APIKey string
}

// Server is the gitvm REST API server.
type Server struct {
	config   ServerConfig
	handlers *Handlers
	logger   *slog.Logger
}

// NewServer creates a new API server.
func NewServer(cfg ServerConfig, orch *orchestrator.Orchestrator, logger *slog.Logger) *Server {
	return &Server{
		config:   cfg,
		handlers: NewHandlers(orch),
		logger:   logger,
	}
}

// Handler returns the HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	h := s.handlers

	// VM lifecycle
	mux.HandleFunc("POST /v1/vms", h.CreateVM)
	mux.HandleFunc("GET /v1/vms", h.ListVMs)
	mux.HandleFunc("GET /v1/vms/{id}", h.GetVM)
	mux.HandleFunc("DELETE /v1/vms/{id}", h.DeleteVM)

	// Command execution (proxied to agent)
	mux.HandleFunc("POST /v1/vms/{id}/exec", h.ExecVM)
	mux.HandleFunc("POST /v1/vms/{id}/exec/stream", h.ExecStreamVM)

	// Filesystem (proxied to agent)
	mux.HandleFunc("GET /v1/vms/{id}/files", h.ReadFile)
	mux.HandleFunc("PUT /v1/vms/{id}/files", h.WriteFile)
	mux.HandleFunc("GET /v1/vms/{id}/files/list", h.ListFiles)
	mux.HandleFunc("DELETE /v1/vms/{id}/files", h.DeleteFile)
	mux.HandleFunc("POST /v1/vms/{id}/files/mkdir", h.Mkdir)

	// Health
	mux.HandleFunc("GET /health", h.Health)

	// Apply middleware
	var handler http.Handler = mux
	handler = CORS(handler)
	handler = APIKeyAuth(s.config.APIKey)(handler)
	handler = Logging(s.logger)(handler)

	return handler
}

// ListenAndServe starts the API server.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("0.0.0.0:%d", s.config.Port)
	s.logger.Info("gitvm API server starting", "addr", addr)
	return http.ListenAndServe(addr, s.Handler())
}
