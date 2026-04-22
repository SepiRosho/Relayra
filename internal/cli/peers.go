package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

var peersCmd = &cobra.Command{
	Use:   "peers",
	Short: "List connected peers",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		logger.SetupStdoutOnly(logger.ParseLevel(cfg.LogLevel))
		ctx := logger.WithComponent(context.Background(), "peers")

		rdb, err := store.NewRedis(cfg.RedisURL(), cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			return fmt.Errorf("connect to Redis: %w", err)
		}
		defer rdb.Close()

		if cfg.Role == config.RoleListener {
			peers, err := rdb.ListPeers(ctx)
			if err != nil {
				return fmt.Errorf("list peers: %w", err)
			}

			if len(peers) == 0 {
				fmt.Println("No peers connected.")
				return nil
			}

			fmt.Printf("Connected peers (%d):\n\n", len(peers))
			for _, p := range peers {
				qLen, _ := rdb.QueueLength(ctx, p.ID)
				fmt.Printf("  ID:           %s\n", p.ID)
				fmt.Printf("  Name:         %s\n", p.Name)
				fmt.Printf("  Registered:   %s\n", p.RegisteredAt.Format("2006-01-02 15:04:05"))
				fmt.Printf("  Last Seen:    %s\n", p.LastSeen.Format("2006-01-02 15:04:05"))
				fmt.Printf("  Queue Size:   %d\n", qLen)
				fmt.Println()
			}
		} else {
			// Sender — show Listener info
			listener, err := rdb.GetListenerInfo(ctx)
			if err != nil || listener == nil {
				fmt.Println("No Listener paired. Run 'relayra pair connect <token>' first.")
				return nil
			}

			fmt.Println("Paired Listener:")
			fmt.Printf("  Name:       %s\n", listener.Name)
			fmt.Printf("  Address:    %s\n", listener.Address)
			fmt.Printf("  Paired:     %s\n", listener.RegisteredAt.Format("2006-01-02 15:04:05"))

			pending, _ := rdb.PendingResultsCount(ctx)
			fmt.Printf("  Pending Results: %d\n", pending)
		}

		_ = slog.Default() // suppress unused
		return nil
	},
}
