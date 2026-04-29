package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/crypto"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	proxyPkg "github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "(Sender) Manage proxies",
}

var (
	proxyLongPollSamples int
	proxyLongPollWait    int
)

var proxyAddCmd = &cobra.Command{
	Use:   "add [url]",
	Short: "Add a proxy (e.g., socks5://host:port or http://host:port)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()
		_ = cfg

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb, cfg.ProxyCooldown())

		// Auto-assign priority based on current count
		count, _ := mgr.Count(ctx)
		priority := float64(count + 1)

		if err := mgr.Add(ctx, args[0], priority); err != nil {
			return err
		}

		fmt.Printf("Proxy added: %s (priority: %.0f)\n", args[0], priority)
		return nil
	},
}

var proxyRemoveCmd = &cobra.Command{
	Use:   "remove [url]",
	Short: "Remove a proxy",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb, cfg.ProxyCooldown())

		if err := mgr.Remove(ctx, args[0]); err != nil {
			return err
		}

		fmt.Printf("Proxy removed: %s\n", args[0])
		return nil
	},
}

var proxyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all proxies with health status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb, cfg.ProxyCooldown())

		proxies, err := mgr.List(ctx)
		if err != nil {
			return err
		}

		if len(proxies) == 0 {
			fmt.Println("No proxies configured.")
			fmt.Println("Add one with: relayra proxy add socks5://host:port")
			return nil
		}

		fmt.Printf("Proxies (%d):\n\n", len(proxies))
		for i, p := range proxies {
			status := "HEALTHY"
			if !p.Healthy {
				status = "FAILED"
			}
			fmt.Printf("  %d. %s\n", i+1, p.URL)
			fmt.Printf("     Priority: %.0f | Status: %s | Fails: %d\n", p.Priority, status, p.FailCount)
			if !p.LastChecked.IsZero() {
				fmt.Printf("     Last Checked: %s\n", p.LastChecked.Format("2006-01-02 15:04:05"))
			}
			fmt.Println()
		}
		return nil
	},
}

var proxyTestCmd = &cobra.Command{
	Use:   "test [url]",
	Short: "Test proxy connectivity (tests specific proxy or all if no arg)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb, cfg.ProxyCooldown())

		if len(args) == 1 {
			// Test specific proxy
			fmt.Printf("Testing proxy: %s ...\n", args[0])
			if err := mgr.Test(ctx, args[0]); err != nil {
				fmt.Printf("FAILED: %v\n", err)
				return nil
			}
			fmt.Println("OK")
			return nil
		}

		// Test all proxies
		proxies, err := mgr.List(ctx)
		if err != nil {
			return err
		}

		if len(proxies) == 0 {
			fmt.Println("No proxies configured.")
			return nil
		}

		for _, p := range proxies {
			fmt.Printf("Testing %s ... ", p.URL)
			if err := mgr.Test(ctx, p.URL); err != nil {
				fmt.Printf("FAILED: %v\n", err)
			} else {
				fmt.Println("OK")
			}
		}
		return nil
	},
}

var proxyResetCooldownCmd = &cobra.Command{
	Use:   "reset-cooldown",
	Short: "Reset failure cooldowns for all proxies",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb, cfg.ProxyCooldown())

		count, err := mgr.ResetAllCooldowns(ctx)
		if err != nil {
			return err
		}

		if count == 0 {
			fmt.Println("No proxies were in cooldown.")
		} else {
			fmt.Printf("Reset cooldown for %d proxy(ies).\n", count)
		}
		return nil
	},
}

var proxyTestLongPollCmd = &cobra.Command{
	Use:   "test-longpoll [url]",
	Short: "Measure how long long-poll connections stay open through proxies",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb, cfg.ProxyCooldown())
		listener, err := rdb.GetListenerInfo(ctx)
		if err != nil || listener == nil {
			return fmt.Errorf("no listener paired. Run 'relayra pair connect <token>' first")
		}

		var proxies []string
		if len(args) == 1 {
			proxies = []string{args[0]}
		} else {
			list, err := mgr.List(ctx)
			if err != nil {
				return err
			}
			for _, p := range list {
				proxies = append(proxies, p.URL)
			}
		}

		if len(proxies) == 0 {
			fmt.Println("No proxies configured.")
			return nil
		}

		fmt.Printf("Long-poll reliability test: %d sample(s), %ds requested hold\n\n", proxyLongPollSamples, proxyLongPollWait)
		for _, proxyURL := range proxies {
			avg, successCount, errCount := testLongPollProxy(ctx, cfg, mgr, listener, proxyURL, proxyLongPollWait, proxyLongPollSamples)
			fmt.Printf("%s\n", proxyURL)
			fmt.Printf("  Successful samples: %d/%d\n", successCount, proxyLongPollSamples)
			if successCount > 0 {
				fmt.Printf("  Average hold: %.2fs\n", avg)
			}
			fmt.Printf("  Errors: %d\n\n", errCount)
		}

		return nil
	},
}

func init() {
	proxyCmd.AddCommand(proxyAddCmd)
	proxyCmd.AddCommand(proxyRemoveCmd)
	proxyCmd.AddCommand(proxyListCmd)
	proxyCmd.AddCommand(proxyTestCmd)
	proxyCmd.AddCommand(proxyTestLongPollCmd)
	proxyCmd.AddCommand(proxyResetCooldownCmd)

	proxyTestLongPollCmd.Flags().IntVar(&proxyLongPollSamples, "samples", 3, "Number of long-poll samples to run")
	proxyTestLongPollCmd.Flags().IntVar(&proxyLongPollWait, "wait", 30, "Requested long-poll hold duration in seconds")
}

func loadSenderConfig() (*config.Config, store.Backend, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}

	if cfg.Role != config.RoleSender {
		return nil, nil, fmt.Errorf("proxy management is only available on Sender instances (current role: %s)", cfg.Role)
	}

	logger.SetupStdoutOnly(logger.ParseLevel(cfg.LogLevel))

	rdb, err := store.Open(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("open storage backend: %w", err)
	}

	_ = strings.TrimSpace("") // suppress unused
	_ = slog.Default()
	return cfg, rdb, nil
}

func testLongPollProxy(ctx context.Context, cfg *config.Config, mgr *proxyPkg.Manager, listener *models.Peer, proxyURL string, waitSeconds int, samples int) (float64, int, int) {
	var total float64
	var successCount int
	var errCount int

	transport, err := mgr.TransportForURL(proxyURL)
	if err != nil {
		return 0, 0, samples
	}

	timeoutSeconds := waitSeconds + 15
	if timeoutSeconds < cfg.RequestTimeout {
		timeoutSeconds = cfg.RequestTimeout
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(timeoutSeconds) * time.Second,
	}

	for i := 0; i < samples; i++ {
		payloadUp := models.PollPayloadUp{}
		ciphertext, nonce, timestamp, err := crypto.EncryptJSON(listener.EncryptionKey, &payloadUp)
		if err != nil {
			errCount++
			continue
		}

		pollReq := models.PollRequest{
			PeerID:      listener.ID,
			Nonce:       nonce,
			Timestamp:   timestamp,
			Payload:     ciphertext,
			WaitSeconds: waitSeconds,
		}

		reqBody, _ := json.Marshal(pollReq)
		pollURL := fmt.Sprintf("http://%s/api/v1/poll", listener.Address)
		start := time.Now()
		resp, err := client.Post(pollURL, "application/json", bytes.NewReader(reqBody))
		heldFor := time.Since(start).Seconds()
		if err != nil {
			errCount++
			fmt.Printf("  sample %d: failed after %.2fs (%v)\n", i+1, heldFor, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errCount++
			fmt.Printf("  sample %d: HTTP %d after %.2fs\n", i+1, resp.StatusCode, heldFor)
			continue
		}

		successCount++
		total += heldFor
		fmt.Printf("  sample %d: held %.2fs\n", i+1, heldFor)
	}

	if successCount == 0 {
		return 0, 0, errCount
	}

	return total / float64(successCount), successCount, errCount
}
