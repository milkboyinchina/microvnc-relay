package logger

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

func TestInMemoryLoggerZeroDisk(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "should-not-exist.log")

	var stderrBuf bytes.Buffer
	cfg := config.LoggingConfig{
		DiskLogging:         false, // Default: zero disk writes
		LogLevel:            "info",
		LogFile:             logFile,
		InMemoryBufferLines: 3,
	}

	l, err := New(cfg, &stderrBuf)
	if err != nil {
		t.Fatalf("New logger failed: %v", err)
	}
	defer l.Close()

	slogger := slog.New(l)
	slogger.Debug("debug message should be filtered")
	slogger.Info("first message", "port", 5900)
	slogger.Warn("second message")
	slogger.Error("third message")
	slogger.Info("fourth message") // Overwrites "first message" in capacity-3 ring buffer

	// Verify disk file was NOT created when DiskLogging is false
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Errorf("expected no disk log file when DiskLogging=false, but file exists")
	}

	entries := l.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries in ring buffer, got %d", len(entries))
	}

	if entries[0].Message != "second message" || entries[2].Message != "fourth message" {
		t.Errorf("unexpected ring buffer entries: %+v", entries)
	}

	if !strings.Contains(stderrBuf.String(), "port=5900") {
		t.Errorf("expected stderr output to contain 'port=5900', got: %s", stderrBuf.String())
	}

	l.Reset()
	if len(l.Entries()) != 0 {
		t.Errorf("expected 0 entries after Reset, got %d", len(l.Entries()))
	}
}

func TestDiskLoggingWhenExplicitlyEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "app.log")

	var stderrBuf bytes.Buffer
	cfg := config.LoggingConfig{
		DiskLogging:         true,
		LogLevel:            "debug",
		LogFile:             logFile,
		MaxSizeMB:           1,
		MaxBackups:          1,
		InMemoryBufferLines: 10,
	}

	l, err := New(cfg, &stderrBuf)
	if err != nil {
		t.Fatalf("New logger failed: %v", err)
	}

	slogger := slog.New(l)
	slogger.Debug("disk test line", "mode", 2)

	if err := l.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read disk log file: %v", err)
	}
	if !strings.Contains(string(data), "disk test line") {
		t.Errorf("disk log missing expected content, got: %s", string(data))
	}
}
