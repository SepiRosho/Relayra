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
	Short: "Delete all Relayra data from Redis",
	Long:  "Removes all relayra:* keys from Redis. Peers, proxies, queued requests, and results will be permanently deleted.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !resetForce {
			fmt.Println("This will permanently delete ALL Relayra data from Redis:")
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

		redisAddr := fmt.Sprintf("%s:%d", cfg.RedisAddr, cfg.RedisPort)
		rdb, err := store.NewRedis(redisAddr, cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			return fmt.Errorf("connect to Redis at %s: %w", redisAddr, err)
		}
		defer rdb.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		deleted, err := rdb.FlushAll(ctx)
		if err != nil {
			return fmt.Errorf("flush Redis data: %w", err)
		}

		fmt.Printf("Deleted %d Relayra keys from Redis.\n", deleted)
		return nil
	},
}

func init() {
	resetCmd.Flags().BoolVarP(&resetForce, "force", "f", false, "Skip confirmation prompt")
}
