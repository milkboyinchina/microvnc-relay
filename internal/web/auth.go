package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "uvnc_session"

type sessionEntry struct {
	token     string
	username  string
	expiresAt time.Time
}

// AuthManager manages WebUI authentication sessions and bcrypt password checks.
type AuthManager struct {
	cfg      *config.Config
	mu       sync.RWMutex
	sessions map[string]sessionEntry
}

// NewAuthManager initializes AuthManager.
func NewAuthManager(cfg *config.Config) *AuthManager {
	return &AuthManager{
		cfg:      cfg,
		sessions: make(map[string]sessionEntry),
	}
}

// VerifyCredentials checks the username and password against cfg.Security.
// If the configured password is not yet bcrypt-hashed (e.g. user entered plaintext),
// it transparently compares and upgrades the stored hash in the config.
func (a *AuthManager) VerifyCredentials(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.cfg.Security.AuthEnabled {
		return true
	}

	if username != a.cfg.Security.Username {
		return false
	}

	storedHash := a.cfg.Security.PasswordHash
	if isBcryptHash(storedHash) {
		err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))
		return err == nil
	}

	// Plaintext fallback and auto-upgrade to bcrypt
	if storedHash == password {
		if newHash, err := bcrypt.GenerateFromPassword([]byte(password), 12); err == nil {
			a.cfg.Security.PasswordHash = string(newHash)
		}
		return true
	}

	return false
}

// CreateSession generates a secure random 32-byte hex token and records it with an expiration.
func (a *AuthManager) CreateSession(username string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Prune expired sessions first
	now := time.Now()
	for tok, s := range a.sessions {
		if now.After(s.expiresAt) {
			delete(a.sessions, tok)
		}
	}

	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)

	timeoutMin := a.cfg.Security.SessionTimeoutMinutes
	if timeoutMin <= 0 {
		timeoutMin = 1440 // 24 hours default
	}

	a.sessions[token] = sessionEntry{
		token:     token,
		username:  username,
		expiresAt: now.Add(time.Duration(timeoutMin) * time.Minute),
	}

	return token
}

// ValidateSession verifies if the session token is valid and active.
func (a *AuthManager) ValidateSession(token string) bool {
	if !a.cfg.Security.AuthEnabled {
		return true
	}
	if token == "" {
		return false
	}

	a.mu.RLock()
	s, ok := a.sessions[token]
	a.mu.RUnlock()

	if !ok {
		return false
	}

	if time.Now().After(s.expiresAt) {
		a.mu.Lock()
		delete(a.sessions, token)
		a.mu.Unlock()
		return false
	}

	return true
}

// RevokeSession deletes an active session.
func (a *AuthManager) RevokeSession(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

// RequireAuth wraps an http.Handler with session cookie authentication.
func (a *AuthManager) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.cfg.Security.AuthEnabled {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !a.ValidateSession(cookie.Value) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SetSessionCookie writes the HTTP-only session cookie to the response.
func (a *AuthManager) SetSessionCookie(w http.ResponseWriter, token string) {
	timeoutMin := a.cfg.Security.SessionTimeoutMinutes
	if timeoutMin <= 0 {
		timeoutMin = 1440
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   timeoutMin * 60,
	})
}

// ClearSessionCookie clears the session cookie.
func (a *AuthManager) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$")
}
