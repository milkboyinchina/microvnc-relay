package web

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func useFastBcrypt(t *testing.T) {
	t.Helper()
	old := authBcryptCost
	authBcryptCost = bcrypt.MinCost
	t.Cleanup(func() { authBcryptCost = old })
}

func newTestAuthManager(t *testing.T) (*AuthManager, string) {
	t.Helper()

	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Security.AuthEnabled = true
	cfg.Security.PasswordHash = ""
	cfg.Server.WebBind = "127.0.0.1:0"

	configPath := filepath.Join(dir, "config.yaml")
	return NewAuthManager(cfg, configPath), filepath.Join(dir, "auth.json")
}

func setupTokenForTest(t *testing.T, a *AuthManager) string {
	t.Helper()
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.setupToken == "" {
		t.Fatal("expected a non-empty setup token")
	}
	return a.setupToken
}

func TestAuthSetupRequiresTokenAndCreatesExclusiveFile(t *testing.T) {
	useFastBcrypt(t)
	a, authPath := newTestAuthManager(t)

	if !a.SetupRequired() {
		t.Fatal("expected setup_required on first run")
	}
	if a.AuthBroken() {
		t.Fatal("did not expect auth_broken on first run")
	}

	token := setupTokenForTest(t, a)
	if err := a.SetAdminCredentials("admin", "password123", "wrong-token"); !errors.Is(err, ErrInvalidSetupToken) {
		t.Fatalf("expected ErrInvalidSetupToken, got %v", err)
	}
	if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auth.json must not exist after a failed setup, err=%v", err)
	}

	if err := a.SetAdminCredentials("admin", "password123", token); err != nil {
		t.Fatalf("SetAdminCredentials: %v", err)
	}
	if a.SetupRequired() {
		t.Fatal("setup_required must be false after setup")
	}
	if a.ValidateSetupToken(token) {
		t.Fatal("setup token must be consumed after successful setup")
	}

	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat auth.json: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("auth.json permissions are too broad: %o", info.Mode().Perm())
	}

	if ok, err := a.VerifyCredentials("admin", "password123"); err != nil || !ok {
		t.Fatalf("expected valid credentials, ok=%v err=%v", ok, err)
	}
	if ok, err := a.VerifyCredentials("admin", "wrong"); err != nil || ok {
		t.Fatalf("expected invalid credentials, ok=%v err=%v", ok, err)
	}

	// A second setup must fail even with the old token.
	if err := a.SetAdminCredentials("other", "password456", token); !errors.Is(err, ErrInvalidSetupToken) && !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("expected setup to be refused, got %v", err)
	}

	// A fresh manager reading the same file must be ready.
	a2 := NewAuthManager(a.cfg, filepath.Join(filepath.Dir(authPath), "config.yaml"))
	if a2.SetupRequired() {
		t.Fatal("setup must not be required when auth.json is valid")
	}
	if ok, err := a2.VerifyCredentials("admin", "password123"); err != nil || !ok {
		t.Fatalf("fresh manager rejected valid credentials, ok=%v err=%v", ok, err)
	}
}

func TestAuthPlaintextConfigHashIsUpgraded(t *testing.T) {
	useFastBcrypt(t)
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Security.AuthEnabled = true
	cfg.Security.Username = "admin"
	cfg.Security.PasswordHash = "secret123" // legacy plaintext config value
	authPath := filepath.Join(dir, "auth.json")

	a := NewAuthManager(cfg, filepath.Join(dir, "config.yaml"))
	if a.SetupRequired() {
		t.Fatal("plaintext config credentials must not require first-run setup")
	}
	if ok, err := a.VerifyCredentials("admin", "secret123"); err != nil || !ok {
		t.Fatalf("plaintext upgrade credentials are not usable, ok=%v err=%v", ok, err)
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("expected auth.json after plaintext upgrade: %v", err)
	}
	if !bytes.Contains(data, []byte("$2")) {
		t.Fatalf("expected a bcrypt hash in auth.json, got %s", data)
	}
}

func TestAuthCorruptFileHardDisablesSetup(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("write corrupt auth.json: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Security.AuthEnabled = true
	cfg.Security.PasswordHash = ""
	a := NewAuthManager(cfg, filepath.Join(dir, "config.yaml"))

	if a.SetupRequired() {
		t.Fatal("setup must be hard-disabled when auth.json exists but is corrupt")
	}
	if !a.AuthBroken() {
		t.Fatal("expected auth_broken for corrupt auth.json")
	}
	if ok, err := a.VerifyCredentials("admin", "anything"); !errors.Is(err, ErrAuthFileBroken) || ok {
		t.Fatalf("expected ErrAuthFileBroken, ok=%v err=%v", ok, err)
	}
}

func TestAuthConcurrentSetupHasSingleWinner(t *testing.T) {
	useFastBcrypt(t)
	a, authPath := newTestAuthManager(t)
	token := setupTokenForTest(t, a)

	const attempts = 8
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- a.SetAdminCredentials("admin", "password123", token)
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one setup winner, got %d", successes)
	}

	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat auth.json: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("auth.json must not be empty")
	}
	if ok, err := a.VerifyCredentials("admin", "password123"); err != nil || !ok {
		t.Fatalf("winner credentials are not usable, ok=%v err=%v", ok, err)
	}
}

func TestAuthBcryptBusyIsRejected(t *testing.T) {
	useFastBcrypt(t)
	a, _ := newTestAuthManager(t)
	token := setupTokenForTest(t, a)
	if err := a.SetAdminCredentials("admin", "password123", token); err != nil {
		t.Fatalf("SetAdminCredentials: %v", err)
	}

	// Simulate another bcrypt operation in flight.
	a.bcryptSem <- struct{}{}
	if ok, err := a.VerifyCredentials("admin", "password123"); ok || !errors.Is(err, ErrBcryptBusy) {
		t.Fatalf("expected ErrBcryptBusy, ok=%v err=%v", ok, err)
	}
	<-a.bcryptSem

	if ok, err := a.VerifyCredentials("admin", "password123"); err != nil || !ok {
		t.Fatalf("expected valid credentials after release, ok=%v err=%v", ok, err)
	}
}

func TestAuthSessionCapEvictsOldest(t *testing.T) {
	useFastBcrypt(t)
	a, _ := newTestAuthManager(t)
	token := setupTokenForTest(t, a)
	if err := a.SetAdminCredentials("admin", "password123", token); err != nil {
		t.Fatalf("SetAdminCredentials: %v", err)
	}

	first, err := a.CreateSession("admin")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < maxSessions; i++ {
		time.Sleep(time.Millisecond)
		if _, err := a.CreateSession("admin"); err != nil {
			t.Fatalf("CreateSession %d: %v", i, err)
		}
	}

	if a.ValidateSession(first) {
		t.Fatal("oldest session should have been evicted at the cap")
	}
}
