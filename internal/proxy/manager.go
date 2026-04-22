package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/store"
	"golang.org/x/net/proxy"
)

const (
	keyProxyList         = "relayra:proxy:list"
	keyProxyStatusPrefix = "relayra:proxy:status:"
	maxFailCount         = 3
	cooldownDuration     = 5 * time.Minute
)

// Manager handles proxy storage, health checking, and rotation.
type Manager struct {
	rdb *store.Redis
}

// NewManager creates a new proxy Manager.
func NewManager(rdb *store.Redis) *Manager {
	return &Manager{rdb: rdb}
}

// Add adds a proxy URL with a priority score (lower = higher priority).
func (m *Manager) Add(ctx context.Context, proxyURL string, priority float64) error {
	ctx = logger.WithComponent(ctx, "proxy")

	// Validate URL
	if _, err := url.Parse(proxyURL); err != nil {
		return fmt.Errorf("invalid proxy URL '%s': %w", proxyURL, err)
	}

	if err := m.rdb.Client.ZAdd(ctx, keyProxyList, redis.Z{
		Score:  priority,
		Member: proxyURL,
	}).Err(); err != nil {
		slog.ErrorContext(ctx, "failed to add proxy", "url", proxyURL, "error", err)
		return fmt.Errorf("add proxy: %w", err)
	}

	slog.InfoContext(ctx, "proxy added", "url", proxyURL, "priority", priority)
	return nil
}

// Remove removes a proxy URL.
func (m *Manager) Remove(ctx context.Context, proxyURL string) error {
	ctx = logger.WithComponent(ctx, "proxy")

	if err := m.rdb.Client.ZRem(ctx, keyProxyList, proxyURL).Err(); err != nil {
		return fmt.Errorf("remove proxy: %w", err)
	}
	// Clean up status
	m.rdb.Client.Del(ctx, keyProxyStatusPrefix+hashURL(proxyURL))

	slog.InfoContext(ctx, "proxy removed", "url", proxyURL)
	return nil
}

// List returns all proxies ordered by priority.
func (m *Manager) List(ctx context.Context) ([]ProxyInfo, error) {
	ctx = logger.WithComponent(ctx, "proxy")

	members, err := m.rdb.Client.ZRangeWithScores(ctx, keyProxyList, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}

	var proxies []ProxyInfo
	for _, member := range members {
		proxyURL := member.Member.(string)
		status := m.getStatus(ctx, proxyURL)
		proxies = append(proxies, ProxyInfo{
			URL:         proxyURL,
			Priority:    member.Score,
			FailCount:   status.FailCount,
			LastChecked: status.LastChecked,
			Healthy:     status.FailCount < maxFailCount,
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
	statusKey := keyProxyStatusPrefix + hashURL(proxyURL)
	m.rdb.Client.HSet(ctx, statusKey, map[string]interface{}{
		"fail_count":   0,
		"last_checked": time.Now().Unix(),
	})
	slog.DebugContext(ctx, "proxy marked healthy", "url", proxyURL)
}

// MarkFailed increments the fail count for a proxy.
func (m *Manager) MarkFailed(ctx context.Context, proxyURL string) {
	m.markFailed(ctx, proxyURL)
}

func (m *Manager) markFailed(ctx context.Context, proxyURL string) {
	ctx = logger.WithComponent(ctx, "proxy")
	statusKey := keyProxyStatusPrefix + hashURL(proxyURL)

	m.rdb.Client.HIncrBy(ctx, statusKey, "fail_count", 1)
	m.rdb.Client.HSet(ctx, statusKey, "last_checked", time.Now().Unix())

	failCount, _ := m.rdb.Client.HGet(ctx, statusKey, "fail_count").Int()
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
			m.MarkSuccess(ctx, p.URL)
			count++
		}
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

	// Get old priority
	score, err := m.rdb.Client.ZScore(ctx, keyProxyList, oldURL).Result()
	if err != nil {
		return fmt.Errorf("proxy not found: %w", err)
	}

	// Remove old, add new
	pipe := m.rdb.Client.Pipeline()
	pipe.ZRem(ctx, keyProxyList, oldURL)
	pipe.Del(ctx, keyProxyStatusPrefix+hashURL(oldURL))
	pipe.ZAdd(ctx, keyProxyList, redis.Z{Score: score, Member: newURL})
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("update proxy URL: %w", err)
	}

	slog.InfoContext(ctx, "proxy URL updated", "old", oldURL, "new", newURL)
	return nil
}

// Count returns the number of configured proxies.
func (m *Manager) Count(ctx context.Context) (int64, error) {
	return m.rdb.Client.ZCard(ctx, keyProxyList).Result()
}

type proxyStatus struct {
	FailCount   int
	LastChecked time.Time
}

func (m *Manager) getStatus(ctx context.Context, proxyURL string) proxyStatus {
	statusKey := keyProxyStatusPrefix + hashURL(proxyURL)
	data, err := m.rdb.Client.HGetAll(ctx, statusKey).Result()
	if err != nil || len(data) == 0 {
		return proxyStatus{}
	}

	var status proxyStatus
	if fc, err := m.rdb.Client.HGet(ctx, statusKey, "fail_count").Int(); err == nil {
		status.FailCount = fc
	}
	if lc, err := m.rdb.Client.HGet(ctx, statusKey, "last_checked").Int64(); err == nil {
		status.LastChecked = time.Unix(lc, 0)
	}
	return status
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

func hashURL(u string) string {
	// Simple hash for Redis key - use first 16 chars of URL hash
	h := fmt.Sprintf("%x", u)
	if len(h) > 16 {
		h = h[:16]
	}
	return h
}
