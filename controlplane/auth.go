package controlplane

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AuthMiddleware validates API keys (for SDK) and session tokens (for UI).
func (s *Server) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for public endpoints
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Node key auth (for internal + admin endpoints)
		nodeKey := r.Header.Get("X-Node-Key")
		if nodeKey != "" && nodeKey == s.config.NodeKey {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			if nodeKey == "" || nodeKey != s.config.NodeKey {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid node key")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// API key auth (for SDK: X-API-Key header)
		apiKey := r.Header.Get("X-API-Key")
		if apiKey != "" {
			project, err := s.db.GetProjectByAPIKey(apiKey)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
				return
			}
			r = r.WithContext(withProject(r.Context(), project))
			next.ServeHTTP(w, r)
			return
		}

		// Session auth (for UI: cookie)
		cookie, err := r.Cookie("session")
		if err == nil && cookie.Value != "" {
			userID, ok := s.sessions.Get(cookie.Value)
			if ok {
				user, err := s.db.GetUser(userID)
				if err == nil {
					r = r.WithContext(withUser(r.Context(), user))
					next.ServeHTTP(w, r)
					return
				}
			}
		}

		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
	})
}

func isPublicPath(path string) bool {
	public := []string{"/health", "/auth/login", "/auth/register", "/auth/logout"}
	for _, p := range public {
		if path == p {
			return true
		}
	}
	return false
}

// SessionStore is a simple in-memory session store.
type SessionStore struct {
	sessions map[string]string // token -> userID
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]string)}
}

func (s *SessionStore) Create(userID string) string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	s.sessions[token] = userID
	return token
}

func (s *SessionStore) Get(token string) (string, bool) {
	uid, ok := s.sessions[token]
	return uid, ok
}

func (s *SessionStore) Delete(token string) {
	delete(s.sessions, token)
}

// --- Auth Handlers ---

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "email and password required")
		return
	}

	// Check if user exists
	if _, err := s.db.GetUserByEmail(req.Email); err == nil {
		writeErr(w, http.StatusConflict, "conflict", "email already registered")
		return
	}

	userID := uuid.New().String()
	hash := hashPassword(req.Password)
	if err := s.db.CreateUser(userID, req.Email, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Auto-create a default project
	projectID := uuid.New().String()
	apiKey, err := s.db.CreateProject(projectID, "Default", userID)
	if err != nil {
		s.logger.Error("failed to create default project", "error", err)
	}

	// Auto-login
	token := s.sessions.Create(userID)
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30,
	})

	writeOK(w, map[string]string{
		"userId":    userID,
		"projectId": projectID,
		"apiKey":    apiKey,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	user, err := s.db.GetUserByEmail(req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}

	if hashPassword(req.Password) != user.PasswordHash {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	token := s.sessions.Create(user.ID)
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30,
	})

	writeOK(w, map[string]string{"userId": user.ID})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session"); err == nil {
		s.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
	writeOK(w, map[string]string{"status": "ok"})
}

func hashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}
