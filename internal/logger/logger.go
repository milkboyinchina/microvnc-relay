package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"github.com/milkboyinchina/microvnc-relay/internal/telemetry"
)

// LogEntry represents a single structured log record stored in the in-memory ring buffer.
type LogEntry struct {
	Timestamp int64  `json:"timestamp"`
	Time      string `json:"time"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Formatted string `json:"formatted"`
}

// Logger manages structured logging to stderr, an in-memory fixed-capacity ring buffer,
// and an optional size-rotated disk file (disabled by default to protect SD/eMMC flash).
type Logger struct {
	ring       *telemetry.RingBuffer[LogEntry]
	minLevel   slog.Level
	stderrOut  io.Writer
	diskWriter *rotatingFileWriter
	attrs      []slog.Attr
	groups     []string
	mu         sync.Mutex
}

// New initializes a Logger from LoggingConfig and sets it as the default slog logger.
func New(cfg config.LoggingConfig, stderrOut io.Writer) (*Logger, error) {
	if stderrOut == nil {
		stderrOut = os.Stderr
	}

	capacity := cfg.InMemoryBufferLines
	if capacity <= 0 {
		capacity = 500
	}

	l := &Logger{
		ring:      telemetry.NewRingBuffer[LogEntry](capacity),
		minLevel:  parseLevel(cfg.LogLevel),
		stderrOut: stderrOut,
	}

	if cfg.DiskLogging && strings.TrimSpace(cfg.LogFile) != "" {
		rw, err := newRotatingFileWriter(cfg.LogFile, cfg.MaxSizeMB, cfg.MaxBackups)
		if err != nil {
			return nil, fmt.Errorf("init disk logger: %w", err)
		}
		l.diskWriter = rw
	}

	return l, nil
}

// SetAsDefault installs this logger as the global slog default logger.
func (l *Logger) SetAsDefault() {
	slog.SetDefault(slog.New(l))
}

// Enabled implements slog.Handler.
func (l *Logger) Enabled(_ context.Context, level slog.Level) bool {
	return level >= l.minLevel
}

// Handle implements slog.Handler.
func (l *Logger) Handle(_ context.Context, r slog.Record) error {
	var buf bytes.Buffer
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	timeStr := ts.Format("2006-01-02 15:04:05.000")

	buf.WriteString(timeStr)
	buf.WriteString(" [")
	buf.WriteString(r.Level.String())
	buf.WriteString("] ")
	buf.WriteString(r.Message)

	// Format pre-attached handler attributes
	for _, a := range l.attrs {
		formatAttr(&buf, l.groups, a)
	}

	// Format record attributes
	r.Attrs(func(a slog.Attr) bool {
		formatAttr(&buf, l.groups, a)
		return true
	} )

	formattedLine := buf.String()
	buf.WriteByte('\n')
	rawBytes := buf.Bytes()

	// 1. Push into fixed-size O(1) in-memory ring buffer
	l.ring.Push(LogEntry{
		Timestamp: ts.UnixMilli(),
		Time:      timeStr,
		Level:     r.Level.String(),
		Message:   r.Message,
		Formatted: formattedLine,
	})

	// 2. Write to stderr and optional disk file under lock
	l.mu.Lock()
	_, _ = l.stderrOut.Write(rawBytes)
	if l.diskWriter != nil {
		_, _ = l.diskWriter.Write(rawBytes)
	}
	l.mu.Unlock()

	return nil
}

// WithAttrs implements slog.Handler.
func (l *Logger) WithAttrs(attrs []slog.Attr) slog.Handler {
	clonedAttrs := make([]slog.Attr, len(l.attrs)+len(attrs))
	copy(clonedAttrs, l.attrs)
	copy(clonedAttrs[len(l.attrs):], attrs)

	return &Logger{
		ring:       l.ring,
		minLevel:   l.minLevel,
		stderrOut:  l.stderrOut,
		diskWriter: l.diskWriter,
		attrs:      clonedAttrs,
		groups:     l.groups,
	}
}

// WithGroup implements slog.Handler.
func (l *Logger) WithGroup(name string) slog.Handler {
	if name == "" {
		return l
	}
	clonedGroups := make([]string, len(l.groups)+1)
	copy(clonedGroups, l.groups)
	clonedGroups[len(l.groups)] = name

	return &Logger{
		ring:       l.ring,
		minLevel:   l.minLevel,
		stderrOut:  l.stderrOut,
		diskWriter: l.diskWriter,
		attrs:      l.attrs,
		groups:     clonedGroups,
	}
}

// Entries returns a chronological snapshot of recent logs from the in-memory ring buffer.
func (l *Logger) Entries() []LogEntry {
	return l.ring.Snapshot()
}

// Reset clears all entries in the in-memory log ring buffer.
func (l *Logger) Reset() {
	l.ring.Reset()
}

// Close cleanly closes the underlying disk log file if disk logging was enabled.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.diskWriter != nil {
		return l.diskWriter.Close()
	}
	return nil
}

func formatAttr(buf *bytes.Buffer, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	buf.WriteByte(' ')
	for _, g := range groups {
		buf.WriteString(g)
		buf.WriteByte('.')
	}
	buf.WriteString(a.Key)
	buf.WriteByte('=')
	buf.WriteString(fmt.Sprint(a.Value.Any()))
}

func parseLevel(lvl string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// rotatingFileWriter implements a simple size-bounded log file rotator.
type rotatingFileWriter struct {
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	currBytes  int64
}

func newRotatingFileWriter(path string, maxSizeMB int, maxBackups int) (*rotatingFileWriter, error) {
	if maxSizeMB <= 0 {
		maxSizeMB = 5
	}
	if maxBackups < 0 {
		maxBackups = 2
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}

	rw := &rotatingFileWriter{
		path:       path,
		maxBytes:   int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
	}

	if err := rw.openExistingOrNew(); err != nil {
		return nil, err
	}
	return rw, nil
}

func (rw *rotatingFileWriter) openExistingOrNew() error {
	f, err := os.OpenFile(rw.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	rw.file = f
	rw.currBytes = info.Size()
	return nil
}

func (rw *rotatingFileWriter) Write(p []byte) (int, error) {
	if rw.file == nil {
		return 0, os.ErrClosed
	}
	if rw.currBytes+int64(len(p)) > rw.maxBytes {
		_ = rw.rotate()
	}
	n, err := rw.file.Write(p)
	rw.currBytes += int64(n)
	return n, err
}

func (rw *rotatingFileWriter) rotate() error {
	if rw.file != nil {
		_ = rw.file.Close()
		rw.file = nil
	}

	// Shift backups: .2 -> .3, .1 -> .2, base -> .1
	for i := rw.maxBackups - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", rw.path, i)
		newPath := fmt.Sprintf("%s.%d", rw.path, i+1)
		_ = os.Rename(oldPath, newPath)
	}
	if rw.maxBackups > 0 {
		_ = os.Rename(rw.path, rw.path+".1")
	} else {
		_ = os.Remove(rw.path)
	}

	return rw.openExistingOrNew()
}

func (rw *rotatingFileWriter) Close() error {
	if rw.file != nil {
		err := rw.file.Close()
		rw.file = nil
		return err
	}
	return nil
}
