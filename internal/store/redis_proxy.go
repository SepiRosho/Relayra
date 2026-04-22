package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	keyProxyList         = "relayra:proxy:list"
	keyProxyStatusPrefix = "relayra:proxy:status:"
)

func (r *Redis) AddProxy(ctx context.Context, proxyURL string, priority float64) error {
	return r.Client.ZAdd(ctx, keyProxyList, redis.Z{
		Score:  priority,
		Member: proxyURL,
	}).Err()
}

func (r *Redis) RemoveProxy(ctx context.Context, proxyURL string) error {
	pipe := r.Client.Pipeline()
	pipe.ZRem(ctx, keyProxyList, proxyURL)
	pipe.Del(ctx, keyProxyStatusPrefix+hashURL(proxyURL))
	_, err := pipe.Exec(ctx)
	return err
}

func (r *Redis) ListProxyRecords(ctx context.Context) ([]ProxyRecord, error) {
	members, err := r.Client.ZRangeWithScores(ctx, keyProxyList, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}

	var proxies []ProxyRecord
	for _, member := range members {
		proxyURL := member.Member.(string)
		status := r.getProxyStatus(ctx, proxyURL)
		proxies = append(proxies, ProxyRecord{
			URL:         proxyURL,
			Priority:    member.Score,
			FailCount:   status.FailCount,
			LastChecked: status.LastChecked,
		})
	}
	return proxies, nil
}

func (r *Redis) MarkProxySuccess(ctx context.Context, proxyURL string) error {
	return r.Client.HSet(ctx, keyProxyStatusPrefix+hashURL(proxyURL), map[string]interface{}{
		"fail_count":   0,
		"last_checked": time.Now().Unix(),
	}).Err()
}

func (r *Redis) MarkProxyFailed(ctx context.Context, proxyURL string) (int, error) {
	statusKey := keyProxyStatusPrefix + hashURL(proxyURL)
	if err := r.Client.HIncrBy(ctx, statusKey, "fail_count", 1).Err(); err != nil {
		return 0, err
	}
	if err := r.Client.HSet(ctx, statusKey, "last_checked", time.Now().Unix()).Err(); err != nil {
		return 0, err
	}
	return r.Client.HGet(ctx, statusKey, "fail_count").Int()
}

func (r *Redis) ResetProxyCooldown(ctx context.Context, proxyURL string) error {
	return r.MarkProxySuccess(ctx, proxyURL)
}

func (r *Redis) ResetAllProxyCooldowns(ctx context.Context) (int, error) {
	proxies, err := r.ListProxyRecords(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, p := range proxies {
		if p.FailCount > 0 {
			if err := r.MarkProxySuccess(ctx, p.URL); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

func (r *Redis) UpdateProxyURL(ctx context.Context, oldURL, newURL string) error {
	score, err := r.Client.ZScore(ctx, keyProxyList, oldURL).Result()
	if err != nil {
		return fmt.Errorf("proxy not found: %w", err)
	}

	pipe := r.Client.Pipeline()
	pipe.ZRem(ctx, keyProxyList, oldURL)
	pipe.Del(ctx, keyProxyStatusPrefix+hashURL(oldURL))
	pipe.ZAdd(ctx, keyProxyList, redis.Z{Score: score, Member: newURL})
	_, err = pipe.Exec(ctx)
	return err
}

func (r *Redis) ProxyCount(ctx context.Context) (int64, error) {
	return r.Client.ZCard(ctx, keyProxyList).Result()
}

type proxyStatus struct {
	FailCount   int
	LastChecked time.Time
}

func (r *Redis) getProxyStatus(ctx context.Context, proxyURL string) proxyStatus {
	statusKey := keyProxyStatusPrefix + hashURL(proxyURL)
	data, err := r.Client.HGetAll(ctx, statusKey).Result()
	if err != nil || len(data) == 0 {
		return proxyStatus{}
	}

	var status proxyStatus
	if fc, err := r.Client.HGet(ctx, statusKey, "fail_count").Int(); err == nil {
		status.FailCount = fc
	}
	if lc, err := r.Client.HGet(ctx, statusKey, "last_checked").Int64(); err == nil {
		status.LastChecked = time.Unix(lc, 0)
	}
	return status
}

func hashURL(u string) string {
	h := fmt.Sprintf("%x", u)
	h = strings.TrimSpace(h)
	if len(h) > 16 {
		h = h[:16]
	}
	return h
}
