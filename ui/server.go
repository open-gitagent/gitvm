package ui

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

// Config for the UI server.
type Config struct {
	Port            int
	ControlPlaneURL string // API base URL for frontend to call
}

// Server serves the gitvm dashboard.
type Server struct {
	config    Config
	templates *template.Template
	logger    *slog.Logger
}

// NewServer creates a new UI server.
func NewServer(cfg Config, logger *slog.Logger) *Server {
	tmpl := template.Must(template.ParseFS(templateFiles, "templates/*.html"))
	return &Server{
		config:    cfg,
		templates: tmpl,
		logger:    logger,
	}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static files
	mux.Handle("GET /static/", http.FileServer(http.FS(staticFiles)))

	// Pages
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /sandboxes", s.handleSandboxes)
	mux.HandleFunc("GET /nodes", s.handleNodes)
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("GET /login", s.handleLoginPage)

	return mux
}

// ListenAndServe starts the UI server.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("0.0.0.0:%d", s.config.Port)
	s.logger.Info("gitvm UI starting", "addr", addr)
	return http.ListenAndServe(addr, s.Handler())
}

// --- Page Handlers ---

type pageData struct {
	Title           string
	ControlPlaneURL string
	Page            string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, "layout", pageData{
		Title:           "Dashboard",
		ControlPlaneURL: s.config.ControlPlaneURL,
		Page:            "dashboard",
	})
}

func (s *Server) handleSandboxes(w http.ResponseWriter, r *http.Request) {
	s.render(w, "layout", pageData{
		Title:           "Sandboxes",
		ControlPlaneURL: s.config.ControlPlaneURL,
		Page:            "sandboxes",
	})
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	s.render(w, "layout", pageData{
		Title:           "Nodes",
		ControlPlaneURL: s.config.ControlPlaneURL,
		Page:            "nodes",
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, "layout", pageData{
		Title:           "Settings",
		ControlPlaneURL: s.config.ControlPlaneURL,
		Page:            "settings",
	})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", pageData{
		Title:           "Login",
		ControlPlaneURL: s.config.ControlPlaneURL,
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("template error", "name", name, "error", err)
		http.Error(w, "Internal Server Error", 500)
	}
}
