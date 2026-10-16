package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

type authFilePayload struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AuthManager manages WebUI authentication sessions, bcrypt credentials,
// and first-run password setup with file-deletion reset support.
type AuthManager struct {
	cfg           *config.Config
	authFilePath  string
	mu            sync.RWMutex
	username      string
	passwordHash  string
	setupRequired bool
	sessions      map[string]sessionEntry
}

// NewAuthManager initializes AuthManager and loads or detects credentials.
func NewAuthManager(cfg *config.Config, configPath string) *AuthManager {
	authPath := ""
	if configPath != "" {
		authPath = filepath.Join(filepath.Dir(configPath), "auth.json")
	}

	a := &AuthManager{
		cfg:          cfg,
		authFilePath: authPath,
		sessions:     make(map[string]sessionEntry),
	}

	a.initCredentials()
	return a
}

func (a *AuthManager) initCredentials() {
	if !a.cfg.Security.AuthEnabled {
		a.setupRequired = false
		return
	}

	// 1. Try loading from auth.json
	if a.authFilePath != "" {
		if data, err := os.ReadFile(a.authFilePath); err == nil {
			var payload authFilePayload
			if err := json.Unmarshal(data, &payload); err == nil && isBcryptHash(payload.PasswordHash) {
				a.username = payload.Username
				a.passwordHash = payload.PasswordHash
				a.setupRequired = false
				slog.Info("loaded WebUI admin credentials from auth file", "path", a.authFilePath)
				return
			}
		}
	}

	// 2. Check if password_hash exists in config.yaml
	if a.cfg.Security.PasswordHash != "" {
		a.username = a.cfg.Security.Username
		if a.username == "" {
			a.username = "admin"
		}
		hash := a.cfg.Security.PasswordHash
		if !isBcryptHash(hash) {
			// Upgrade plaintext in config
			if newHash, err := bcrypt.GenerateFromPassword([]byte(hash), 12); err == nil {
				hash = string(newHash)
			}
		}
		a.passwordHash = hash
		a.setupRequired = false

		// Persist into auth.json
		if a.authFilePath != "" {
			_ = a.saveAuthFile(a.username, a.passwordHash)
		}
		return
	}

	// 3. No password exists anywhere -> First-Run Setup required!
	a.setupRequired = true
	slog.Warn("No WebUI admin credentials configured. Initial First-Run setup required via dashboard.")
}

// SetupRequired returns true if initial administrator setup must be completed.
func (a *AuthManager) SetupRequired() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Security.AuthEnabled && a.setupRequired
}

// SetAdminCredentials hashes password with bcrypt, saves to auth.json, and clears setupRequired.
func (a *AuthManager) SetAdminCredentials(username, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username cannot be empty")
	}
	if len(password) < 6 {
		return errors.New("password must be at least 6 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	a.username = username
	a.passwordHash = string(hash)
	a.setupRequired = false

	if a.authFilePath != "" {
		if err := a.saveAuthFile(username, a.passwordHash); err != nil {
			slog.Error("failed to save auth file", "path", a.authFilePath, "err", err)
			return err
		}
		slog.Info("WebUI administrator credentials saved. (To reset password, delete this file and restart)",
			"path", a.authFilePath,
		)
	}

	return nil
}

func (a *AuthManager) saveAuthFile(username, passwordHash string) error {
	payload := authFilePayload{
		Username:     username,
		PasswordHash: passwordHash,
		UpdatedAt:    time.Now(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(a.authFilePath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, "auth-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return err
	}
	_ = tmpFile.Sync()
	_ = tmpFile.Close()

	_ = os.Chmod(tmpName, 0600)
	return os.Rename(tmpName, a.authFilePath)
}

// VerifyCredentials checks username and password against the stored bcrypt hash.
func (a *AuthManager) VerifyCredentials(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.cfg.Security.AuthEnabled {
		return true
	}
	if a.setupRequired {
		return false
	}

	if username != a.username {
		return false
	}

	err := bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(password))
	return err == nil
}

// CreateSession generates a secure random 32-byte hex token and records it with an expiration.
func (a *AuthManager) CreateSession(username string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

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
		timeoutMin = 1440
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
	if token == "" || a.setupRequired {
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

		if a.SetupRequired() {
			http.Error(w, `{"error":"setup_required"}`, http.StatusForbidden)
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
