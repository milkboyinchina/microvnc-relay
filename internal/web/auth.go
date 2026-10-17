package web

import (
	"crypto/rand"
	"crypto/subtle"
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

const (
	sessionCookieName = "uvnc_session"
	maxSessions       = 64
	setupTokenBytes   = 32
)

// authBcryptCost is the bcrypt work factor used for new password hashes.
// Tests lower it to keep the race-detector suite fast; production keeps 12.
var authBcryptCost = 12

var (
	// ErrAlreadyConfigured is returned when first-run setup is attempted after
	// auth.json already exists (or after setup has completed).
	ErrAlreadyConfigured = errors.New("administrator credentials already configured")

	// ErrInvalidSetupToken is returned when the one-time setup token is missing
	// or does not match the token printed to stdout/journal at first start.
	ErrInvalidSetupToken = errors.New("invalid or expired setup token")

	// ErrBcryptBusy is returned when another bcrypt operation is in flight.
	// The caller should reject the request immediately rather than queueing a
	// goroutine on a single-core ARMv6 system.
	ErrBcryptBusy = errors.New("credential verification is busy")

	// ErrAuthFileBroken is returned when auth.json exists but cannot be parsed.
	// Setup is hard-disabled in this state; the operator must remove the file
	// and restart to reset credentials.
	ErrAuthFileBroken = errors.New("auth.json exists but is invalid")
)

type authState int

const (
	authStateReady authState = iota
	authStateSetupRequired
	authStateBroken
)

type sessionEntry struct {
	token     string
	username  string
	createdAt time.Time
	expiresAt time.Time
}

type authFilePayload struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AuthManager manages WebUI authentication sessions, bcrypt credentials, and
// token-gated first-run setup.
//
// Security properties:
//   - No default credentials are shipped.
//   - First-run setup requires a one-time token printed to stdout/journal.
//   - auth.json is created with O_EXCL, so a concurrent setup cannot win.
//   - Once auth.json exists, setup is hard-disabled, even if the file is corrupt.
//   - Only one bcrypt hash/compare runs at a time; concurrent attempts get 429.
type AuthManager struct {
	cfg          *config.Config
	authFilePath string

	mu            sync.RWMutex
	state         authState
	username      string
	passwordHash  string
	setupToken    string
	sessions  map[string]sessionEntry
	setupMu   sync.Mutex
	bcryptSem chan struct{}
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
		bcryptSem:    make(chan struct{}, 1),
	}
	a.initCredentials()
	return a
}

func (a *AuthManager) initCredentials() {
	if !a.cfg.Security.AuthEnabled {
		a.state = authStateReady
		return
	}

	// 1. Prefer auth.json. If it exists but is invalid, refuse setup: a
	// corrupt file must not re-open the takeover window. The operator resets
	// by deleting the file and restarting.
	if a.authFilePath != "" {
		data, err := os.ReadFile(a.authFilePath)
		if err == nil {
			var payload authFilePayload
			if json.Unmarshal(data, &payload) == nil && payload.Username != "" && isBcryptHash(payload.PasswordHash) {
				a.username = payload.Username
				a.passwordHash = payload.PasswordHash
				a.state = authStateReady
				slog.Info("loaded WebUI admin credentials from auth file", "path", a.authFilePath)
				return
			}
			a.state = authStateBroken
			slog.Error("auth.json exists but is invalid; first-run setup is hard-disabled",
				"path", a.authFilePath,
				"remedy", "stop the service, delete auth.json, start the service, and use the newly printed setup token",
			)
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			a.state = authStateBroken
			slog.Error("cannot read auth.json; first-run setup is hard-disabled",
				"path", a.authFilePath,
				"err", err,
				"remedy", "fix permissions or delete auth.json and restart",
			)
			return
		}
	}

	// 2. Backward compatibility: load a hash from config.yaml, upgrade
	// plaintext, and persist it to auth.json with O_EXCL.
	if a.cfg.Security.PasswordHash != "" {
		username := a.cfg.Security.Username
		if username == "" {
			username = "admin"
		}
		hash := a.cfg.Security.PasswordHash
		if !isBcryptHash(hash) {
			if newHash, err := bcrypt.GenerateFromPassword([]byte(hash), authBcryptCost); err == nil {
				hash = string(newHash)
			}
		}
		a.username = username
		a.passwordHash = hash
		a.state = authStateReady

		if a.authFilePath != "" {
			if err := a.writeAuthFileExclusive(username, hash); err != nil && !errors.Is(err, ErrAlreadyConfigured) {
				slog.Error("failed to persist config credentials to auth.json", "path", a.authFilePath, "err", err)
			}
		}
		slog.Info("loaded WebUI admin credentials from config", "username", username)
		return
	}

	// 3. No credentials anywhere: generate a one-time setup token. The token is
	// printed to stdout/journal only, never served over HTTP and never written
	// to disk.
	token, err := generateSetupToken()
	if err != nil {
		a.state = authStateBroken
		slog.Error("failed to generate first-run setup token", "err", err)
		return
	}
	a.setupToken = token
	a.state = authStateSetupRequired
	slog.Warn("FIRST-RUN SETUP REQUIRED: a one-time token is required to create the administrator account",
		"setup_token", token,
		"instructions", "read this line from journalctl -u microvnc-relay or docker logs <container>",
	)
}

func generateSetupToken() (string, error) {
	buf := make([]byte, setupTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// SetupRequired returns true if initial administrator setup must be completed.
func (a *AuthManager) SetupRequired() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Security.AuthEnabled && a.state == authStateSetupRequired
}

// AuthBroken returns true when auth.json exists but is unusable.
func (a *AuthManager) AuthBroken() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Security.AuthEnabled && a.state == authStateBroken
}

// ValidateSetupToken reports whether token matches the current one-time token.
func (a *AuthManager) ValidateSetupToken(token string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state != authStateSetupRequired || a.setupToken == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a.setupToken), []byte(token)) == 1
}

// SetAdminCredentials completes first-run setup. It requires the one-time
// setup token, hashes the password outside the state lock, and creates
// auth.json with O_EXCL so concurrent setups cannot overwrite each other.
func (a *AuthManager) SetAdminCredentials(username, password, setupToken string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username cannot be empty")
	}
	if len(password) < 6 {
		return errors.New("password must be at least 6 characters")
	}
	if !a.ValidateSetupToken(setupToken) {
		return ErrInvalidSetupToken
	}

	// Hash outside the state lock. One bcrypt operation at a time; a concurrent
	// request is rejected immediately rather than pinned to a goroutine.
	if err := a.acquireBcrypt(); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), authBcryptCost)
	a.releaseBcrypt()
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	a.setupMu.Lock()
	defer a.setupMu.Unlock()

	// Re-check under the setup lock: the token may have been consumed or the
	// file may have appeared while we were hashing.
	a.mu.RLock()
	state := a.state
	expectedToken := a.setupToken
	a.mu.RUnlock()
	if state != authStateSetupRequired {
		return ErrAlreadyConfigured
	}
	if expectedToken == "" || subtle.ConstantTimeCompare([]byte(expectedToken), []byte(setupToken)) != 1 {
		return ErrInvalidSetupToken
	}

	if err := a.writeAuthFileExclusive(username, string(hash)); err != nil {
		return err
	}

	a.mu.Lock()
	a.username = username
	a.passwordHash = string(hash)
	a.state = authStateReady
	a.setupToken = ""
	a.mu.Unlock()

	slog.Info("WebUI administrator credentials saved",
		"path", a.authFilePath,
		"reset", "stop the service, delete auth.json, start the service, and use the newly printed setup token",
	)
	return nil
}

// writeAuthFileExclusive creates auth.json atomically and fails if it already
// exists. It never overwrites an existing credential file.
func (a *AuthManager) writeAuthFileExclusive(username, passwordHash string) error {
	if a.authFilePath == "" {
		return errors.New("auth file path is not configured")
	}

	payload := authFilePayload{
		Username:     username,
		PasswordHash: passwordHash,
		UpdatedAt:    time.Now(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth payload: %w", err)
	}

	dir := filepath.Dir(a.authFilePath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}

	f, err := os.OpenFile(a.authFilePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrAlreadyConfigured
		}
		return fmt.Errorf("create auth file exclusively: %w", err)
	}

	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(a.authFilePath)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write auth file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync auth file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close auth file: %w", err)
	}

	removeOnError = false
	return nil
}

// VerifyCredentials checks username and password against the stored bcrypt
// hash. The state lock is released before the (expensive) bcrypt compare so
// session validation is never blocked for a full bcrypt round.
func (a *AuthManager) VerifyCredentials(username, password string) (bool, error) {
	if !a.cfg.Security.AuthEnabled {
		return true, nil
	}

	a.mu.RLock()
	state := a.state
	storedUser := a.username
	storedHash := a.passwordHash
	a.mu.RUnlock()

	switch state {
	case authStateSetupRequired:
		return false, nil
	case authStateBroken:
		return false, ErrAuthFileBroken
	}

	if username != storedUser || storedHash == "" {
		return false, nil
	}

	if err := a.acquireBcrypt(); err != nil {
		return false, err
	}
	defer a.releaseBcrypt()

	err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))
	return err == nil, nil
}

func (a *AuthManager) acquireBcrypt() error {
	select {
	case a.bcryptSem <- struct{}{}:
		return nil
	default:
		return ErrBcryptBusy
	}
}

func (a *AuthManager) releaseBcrypt() {
	select {
	case <-a.bcryptSem:
	default:
	}
}

// CreateSession generates a secure random 32-byte hex token and records it with
// an expiration. The active session map is capped; the oldest session is
// evicted when full.
func (a *AuthManager) CreateSession(username string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := hex.EncodeToString(buf)

	timeoutMin := a.cfg.Security.SessionTimeoutMinutes
	if timeoutMin <= 0 {
		timeoutMin = 1440
	}
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	for tok, s := range a.sessions {
		if now.After(s.expiresAt) {
			delete(a.sessions, tok)
		}
	}

	if len(a.sessions) >= maxSessions {
		var oldestToken string
		var oldestCreated time.Time
		for tok, s := range a.sessions {
			if oldestToken == "" || s.createdAt.Before(oldestCreated) {
				oldestToken = tok
				oldestCreated = s.createdAt
			}
		}
		if oldestToken != "" {
			delete(a.sessions, oldestToken)
		}
	}

	a.sessions[token] = sessionEntry{
		token:     token,
		username:  username,
		createdAt: now,
		expiresAt: now.Add(time.Duration(timeoutMin) * time.Minute),
	}

	return token, nil
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
	state := a.state
	s, ok := a.sessions[token]
	a.mu.RUnlock()

	if state != authStateReady || !ok {
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
		if a.AuthBroken() {
			http.Error(w, `{"error":"auth_file_broken","remedy":"delete auth.json and restart"}`, http.StatusServiceUnavailable)
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
		Secure:   a.cfg.Security.SecureCookies,
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
		Secure:   a.cfg.Security.SecureCookies,
		MaxAge:   -1,
	})
}

func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$")
}
