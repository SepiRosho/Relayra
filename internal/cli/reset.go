package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

var resetForce bool

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Delete all Relayra data from the configured storage backend",
	Long:  "Removes Relayra data from the configured storage backend. Peers, proxies, queued requests, and results will be permanently deleted.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !resetForce {
			fmt.Printf("This will permanently delete ALL Relayra data from %s:\n", cfgStorageName())
			fmt.Println("  - Registered peers")
			fmt.Println("  - Proxy list and status")
			fmt.Println("  - Pending requests and results")
			fmt.Println("  - Pairing tokens")
			fmt.Println("  - Listener info")
			fmt.Println()
			fmt.Print("Continue? [y/N] ")

			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				fmt.Println("Cancelled.")
				return nil
			}
		}

		cfg, err := config.Load()
		if err != nil {
			// If no config exists, try default Redis
			cfg = config.DefaultConfig()
		}

		rdb, err := store.Open(cfg)
		if err != nil {
			return fmt.Errorf("open storage backend: %w", err)
		}
		defer rdb.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		deleted, err := rdb.FlushAll(ctx)
		if err != nil {
			return fmt.Errorf("flush Relayra data: %w", err)
		}

		fmt.Printf("Deleted %d Relayra records from %s.\n", deleted, cfg.StorageBackend)
		return nil
	},
}

func init() {
	resetCmd.Flags().BoolVarP(&resetForce, "force", "f", false, "Skip confirmation prompt")
}

func cfgStorageName() string {
	cfg, err := config.Load()
	if err != nil || cfg.StorageBackend == "" {
		return "storage"
	}
	return cfg.StorageBackend
}
