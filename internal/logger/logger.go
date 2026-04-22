package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type contextKey string

const (
	keyComponent contextKey = "component"
	keyRequestID contextKey = "request_id"
	keyPeerID    contextKey = "peer_id"
	keyPollCycle contextKey = "poll_cycle"
	keyAttempt   contextKey = "attempt"
)

// rotatingFileWriter writes to a log file with daily rotation.
type rotatingFileWriter struct {
	mu      sync.Mutex
	dir     string
	current *os.File
	today   string
	maxDays int
}

func newRotatingFileWriter(dir string, maxDays int) (*rotatingFileWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	w := &rotatingFileWriter{dir: dir, maxDays: maxDays}
	if err := w.rotate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != w.today {
		if err := w.rotate(); err != nil {
			// If rotation fails, try to keep writing to current file
			if w.current != nil {
				return w.current.Write(p)
			}
			return 0, err
		}
	}
	return w.current.Write(p)
}

func (w *rotatingFileWriter) rotate() error {
	if w.current != nil {
		w.current.Close()
	}

	w.today = time.Now().Format("2006-01-02")
	path := filepath.Join(w.dir, fmt.Sprintf("relayra-%s.log", w.today))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", path, err)
	}
	w.current = f

	// Cleanup old log files
	go w.cleanup()
	return nil
}

func (w *rotatingFileWriter) cleanup() {
	cutoff := time.Now().AddDate(0, 0, -w.maxDays)
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "relayra-") || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		// Parse date from filename: relayra-2006-01-02.log
		dateStr := strings.TrimPrefix(e.Name(), "relayra-")
		dateStr = strings.TrimSuffix(dateStr, ".log")
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			os.Remove(filepath.Join(w.dir, e.Name()))
		}
	}
}

func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current != nil {
		return w.current.Close()
	}
	return nil
}

// contextHandler extracts correlation fields from context and adds them to log records.
type contextHandler struct {
	inner slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if v, ok := ctx.Value(keyComponent).(string); ok {
		r.AddAttrs(slog.String("component", v))
	}
	if v, ok := ctx.Value(keyRequestID).(string); ok {
		r.AddAttrs(slog.String("request_id", v))
	}
	if v, ok := ctx.Value(keyPeerID).(string); ok {
		r.AddAttrs(slog.String("peer_id", v))
	}
	if v, ok := ctx.Value(keyPollCycle).(int64); ok {
		r.AddAttrs(slog.Int64("poll_cycle", v))
	}
	if v, ok := ctx.Value(keyAttempt).(int); ok {
		r.AddAttrs(slog.Int("attempt", v))
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}

// multiHandler fans out log records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			if err := handler.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

// ParseLevel converts a string level name to slog.Level.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
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

var (
	fileWriter *rotatingFileWriter
)

// Setup initializes the global slog logger with both stdout (text) and file (JSON) outputs.
// Call this once at startup. logDir can be empty to skip file logging.
func Setup(level slog.Level, logDir string, maxDays int) error {
	var handlers []slog.Handler

	// Stdout: human-readable text
	stdoutHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	handlers = append(handlers, stdoutHandler)

	// File: JSON for machine parsing
	if logDir != "" {
		var err error
		fileWriter, err = newRotatingFileWriter(logDir, maxDays)
		if err != nil {
			// Log warning to stdout but don't fail startup
			slog.Warn("failed to initialize file logging, continuing with stdout only", "error", err, "log_dir", logDir)
		} else {
			fileHandler := slog.NewJSONHandler(fileWriter, &slog.HandlerOptions{
				Level: level,
			})
			handlers = append(handlers, fileHandler)
		}
	}

	multi := &multiHandler{handlers: handlers}
	ctx := &contextHandler{inner: multi}
	slog.SetDefault(slog.New(ctx))

	return nil
}

// SetupStdoutOnly initializes a stdout-only logger (for CLI commands that don't need file logging).
func SetupStdoutOnly(level slog.Level) {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	ctx := &contextHandler{inner: handler}
	slog.SetDefault(slog.New(ctx))
}

// Shutdown closes the file writer. Call on graceful shutdown.
func Shutdown() {
	if fileWriter != nil {
		fileWriter.Close()
	}
}

// LogFilePath returns the current log file path, or empty if file logging is not active.
func LogFilePath() string {
	if fileWriter == nil {
		return ""
	}
	fileWriter.mu.Lock()
	defer fileWriter.mu.Unlock()
	if fileWriter.current != nil {
		return fileWriter.current.Name()
	}
	return ""
}

// LogDir returns the directory being used for log files.
func LogDir() string {
	if fileWriter == nil {
		return ""
	}
	return fileWriter.dir
}

// FileWriter returns the underlying io.Writer for the log file (for tests or custom use).
func FileWriter() io.Writer {
	if fileWriter == nil {
		return nil
	}
	return fileWriter
}

// Context helpers — attach correlation fields to context for structured logging.

func WithComponent(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, keyComponent, name)
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

func WithPeerID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyPeerID, id)
}

func WithPollCycle(ctx context.Context, cycle int64) context.Context {
	return context.WithValue(ctx, keyPollCycle, cycle)
}

func WithAttempt(ctx context.Context, attempt int) context.Context {
	return context.WithValue(ctx, keyAttempt, attempt)
}
