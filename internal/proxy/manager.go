package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/store"
	"golang.org/x/net/proxy"
)

const (
	maxFailCount     = 3
	cooldownDuration = 5 * time.Minute
)

// Manager handles proxy storage, health checking, and rotation.
type Manager struct {
	rdb store.Backend
}

// NewManager creates a new proxy Manager.
func NewManager(rdb store.Backend) *Manager {
	return &Manager{rdb: rdb}
}

// Add adds a proxy URL with a priority score (lower = higher priority).
func (m *Manager) Add(ctx context.Context, proxyURL string, priority float64) error {
	ctx = logger.WithComponent(ctx, "proxy")

	// Validate URL
	if _, err := url.Parse(proxyURL); err != nil {
		return fmt.Errorf("invalid proxy URL '%s': %w", proxyURL, err)
	}

	if err := m.rdb.AddProxy(ctx, proxyURL, priority); err != nil {
		slog.ErrorContext(ctx, "failed to add proxy", "url", proxyURL, "error", err)
		return fmt.Errorf("add proxy: %w", err)
	}

	slog.InfoContext(ctx, "proxy added", "url", proxyURL, "priority", priority)
	return nil
}

// Remove removes a proxy URL.
func (m *Manager) Remove(ctx context.Context, proxyURL string) error {
	ctx = logger.WithComponent(ctx, "proxy")

	if err := m.rdb.RemoveProxy(ctx, proxyURL); err != nil {
		return fmt.Errorf("remove proxy: %w", err)
	}

	slog.InfoContext(ctx, "proxy removed", "url", proxyURL)
	return nil
}

// List returns all proxies ordered by priority.
func (m *Manager) List(ctx context.Context) ([]ProxyInfo, error) {
	ctx = logger.WithComponent(ctx, "proxy")

	records, err := m.rdb.ListProxyRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}

	var proxies []ProxyInfo
	for _, record := range records {
		proxies = append(proxies, ProxyInfo{
			URL:         record.URL,
			Priority:    record.Priority,
			FailCount:   record.FailCount,
			LastChecked: record.LastChecked,
			Healthy:     record.FailCount < maxFailCount,
		})
	}

	return proxies, nil
}

// ProxyInfo holds proxy details for display.
type ProxyInfo struct {
	URL         string    `json:"url"`
	Priority    float64   `json:"priority"`
	FailCount   int       `json:"fail_count"`
	LastChecked time.Time `json:"last_checked"`
	Healthy     bool      `json:"healthy"`
}

// GetTransport returns an http.Transport configured with the best available proxy.
// It tries proxies in priority order, skipping those in cooldown.
func (m *Manager) GetTransport(ctx context.Context) (http.RoundTripper, string, error) {
	ctx = logger.WithComponent(ctx, "proxy")

	proxies, err := m.List(ctx)
	if err != nil {
		return nil, "", err
	}

	if len(proxies) == 0 {
		return nil, "", fmt.Errorf("no proxies configured")
	}

	for _, p := range proxies {
		if !p.Healthy {
			// Check if cooldown expired
			if time.Since(p.LastChecked) < cooldownDuration {
				slog.DebugContext(ctx, "proxy in cooldown", "url", p.URL, "fail_count", p.FailCount)
				continue
			}
			// Cooldown expired, try again
			slog.InfoContext(ctx, "proxy cooldown expired, retrying", "url", p.URL)
		}

		transport, err := m.createTransport(p.URL)
		if err != nil {
			slog.WarnContext(ctx, "failed to create transport for proxy", "url", p.URL, "error", err)
			m.markFailed(ctx, p.URL)
			continue
		}

		slog.InfoContext(ctx, "proxy selected", "url", p.URL, "priority", p.Priority)
		return transport, p.URL, nil
	}

	return nil, "", fmt.Errorf("all proxies exhausted or in cooldown")
}

// MarkSuccess resets the fail count for a proxy.
func (m *Manager) MarkSuccess(ctx context.Context, proxyURL string) {
	ctx = logger.WithComponent(ctx, "proxy")
	if err := m.rdb.MarkProxySuccess(ctx, proxyURL); err != nil {
		slog.WarnContext(ctx, "failed to mark proxy healthy", "url", proxyURL, "error", err)
		return
	}
	slog.DebugContext(ctx, "proxy marked healthy", "url", proxyURL)
}

// MarkFailed increments the fail count for a proxy.
func (m *Manager) MarkFailed(ctx context.Context, proxyURL string) {
	m.markFailed(ctx, proxyURL)
}

func (m *Manager) markFailed(ctx context.Context, proxyURL string) {
	ctx = logger.WithComponent(ctx, "proxy")
	failCount, err := m.rdb.MarkProxyFailed(ctx, proxyURL)
	if err != nil {
		slog.WarnContext(ctx, "failed to mark proxy failed", "url", proxyURL, "error", err)
		return
	}
	slog.WarnContext(ctx, "proxy marked failed", "url", proxyURL, "fail_count", failCount)
}

// Test tests connectivity through a specific proxy URL.
func (m *Manager) Test(ctx context.Context, proxyURL string) error {
	ctx = logger.WithComponent(ctx, "proxy")
	slog.InfoContext(ctx, "testing proxy", "url", proxyURL)

	transport, err := m.createTransport(proxyURL)
	if err != nil {
		return fmt.Errorf("create transport: %w", err)
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	// Try to reach a known endpoint
	resp, err := client.Get("http://httpbin.org/ip")
	if err != nil {
		slog.ErrorContext(ctx, "proxy test failed", "url", proxyURL, "error", err)
		return fmt.Errorf("proxy test: %w", err)
	}
	resp.Body.Close()

	slog.InfoContext(ctx, "proxy test successful", "url", proxyURL, "status", resp.StatusCode)
	return nil
}

// ResetAllCooldowns resets the failure count for all proxies.
func (m *Manager) ResetAllCooldowns(ctx context.Context) (int, error) {
	ctx = logger.WithComponent(ctx, "proxy")

	proxies, err := m.List(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, p := range proxies {
		if p.FailCount > 0 {
			count++
		}
	}
	count, err = m.rdb.ResetAllProxyCooldowns(ctx)
	if err != nil {
		return 0, err
	}
	slog.InfoContext(ctx, "reset all proxy cooldowns", "reset_count", count)
	return count, nil
}

// ResetCooldown resets the failure count for a specific proxy.
func (m *Manager) ResetCooldown(ctx context.Context, proxyURL string) error {
	ctx = logger.WithComponent(ctx, "proxy")
	m.MarkSuccess(ctx, proxyURL)
	slog.InfoContext(ctx, "proxy cooldown reset", "url", proxyURL)
	return nil
}

// UpdateURL replaces a proxy URL while preserving its priority.
func (m *Manager) UpdateURL(ctx context.Context, oldURL, newURL string) error {
	ctx = logger.WithComponent(ctx, "proxy")
	if err := m.rdb.UpdateProxyURL(ctx, oldURL, newURL); err != nil {
		return fmt.Errorf("update proxy URL: %w", err)
	}

	slog.InfoContext(ctx, "proxy URL updated", "old", oldURL, "new", newURL)
	return nil
}

// Count returns the number of configured proxies.
func (m *Manager) Count(ctx context.Context) (int64, error) {
	return m.rdb.ProxyCount(ctx)
}

// TransportForURL builds an HTTP transport for a specific configured proxy URL.
func (m *Manager) TransportForURL(proxyURL string) (http.RoundTripper, error) {
	return m.createTransport(proxyURL)
}

func (m *Manager) createTransport(proxyURL string) (http.RoundTripper, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}

	switch parsed.Scheme {
	case "http", "https":
		return &http.Transport{
			Proxy: http.ProxyURL(parsed),
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		}, nil

	case "socks5", "socks5h":
		auth := &proxy.Auth{}
		if parsed.User != nil {
			auth.User = parsed.User.Username()
			auth.Password, _ = parsed.User.Password()
		} else {
			auth = nil
		}

		dialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
		}

		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS5 dialer does not support context")
		}

		return &http.Transport{
			DialContext: contextDialer.DialContext,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}
}
