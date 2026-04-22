package cli

import (
	"fmt"
	"log/slog"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/poller"
	"github.com/relayra/relayra/internal/server"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run Relayra service in the foreground",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		// Initialize logger with file output
		if err := logger.Setup(logger.ParseLevel(cfg.LogLevel), cfg.LogDir, cfg.LogMaxDays); err != nil {
			return fmt.Errorf("setup logger: %w", err)
		}
		defer logger.Shutdown()

		// Startup diagnostic block
		slog.Info("Relayra starting",
			"version", Version,
			"role", cfg.Role,
			"instance", cfg.InstanceName,
			"machine_id", cfg.MachineID,
			"listen", cfg.ListenAddress(),
			"redis", cfg.RedisURL(),
			"log_level", cfg.LogLevel,
		)

		// Connect to Redis
		rdb, err := store.NewRedis(cfg.RedisURL(), cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			return fmt.Errorf("connect to Redis: %w", err)
		}
		defer rdb.Close()

		switch cfg.Role {
		case config.RoleListener:
			return server.Run(cmd.Context(), cfg, rdb)
		case config.RoleSender:
			return poller.Run(cmd.Context(), cfg, rdb)
		default:
			return fmt.Errorf("unknown role: %s", cfg.Role)
		}
	},
}
