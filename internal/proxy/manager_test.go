package proxy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/relayra/relayra/internal/store"
)

func newTestBackend(t *testing.T) store.Backend {
	t.Helper()

	s, err := store.NewSQLite(filepath.Join(t.TempDir(), "relayra-proxy.db"))
	if err != nil {
		t.Fatalf("NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestManagerCooldownIsConfigurable(t *testing.T) {
	rdb := newTestBackend(t)
	ctx := context.Background()
	manager := NewManager(rdb, time.Second)

	if err := manager.Add(ctx, "http://127.0.0.1:8080", 1); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	for i := 0; i < 3; i++ {
		manager.MarkFailed(ctx, "http://127.0.0.1:8080")
	}

	if _, _, err := manager.GetTransport(ctx); err == nil {
		t.Fatalf("GetTransport() error = nil, want cooldown exclusion after 3 failures")
	}

	time.Sleep(1100 * time.Millisecond)

	transport, selected, err := manager.GetTransport(ctx)
	if err != nil {
		t.Fatalf("GetTransport() after cooldown error = %v", err)
	}
	if transport == nil || selected != "http://127.0.0.1:8080" {
		t.Fatalf("GetTransport() = (%v, %q), want active proxy after cooldown", transport, selected)
	}
}
