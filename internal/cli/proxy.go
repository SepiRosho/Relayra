package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	proxyPkg "github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "(Sender) Manage proxies",
}

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
		mgr := proxyPkg.NewManager(rdb)

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
		_, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb)

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
		_, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb)

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
		_, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb)

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
		_, rdb, err := loadSenderConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "proxy")
		mgr := proxyPkg.NewManager(rdb)

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

func init() {
	proxyCmd.AddCommand(proxyAddCmd)
	proxyCmd.AddCommand(proxyRemoveCmd)
	proxyCmd.AddCommand(proxyListCmd)
	proxyCmd.AddCommand(proxyTestCmd)
	proxyCmd.AddCommand(proxyResetCooldownCmd)
}

func loadSenderConfig() (*config.Config, *store.Redis, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}

	if cfg.Role != config.RoleSender {
		return nil, nil, fmt.Errorf("proxy management is only available on Sender instances (current role: %s)", cfg.Role)
	}

	logger.SetupStdoutOnly(logger.ParseLevel(cfg.LogLevel))

	rdb, err := store.NewRedis(cfg.RedisURL(), cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to Redis: %w", err)
	}

	_ = strings.TrimSpace("") // suppress unused
	_ = slog.Default()
	return cfg, rdb, nil
}
