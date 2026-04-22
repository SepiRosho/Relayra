package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

const systemdUnit = `[Unit]
Description=Relayra - Restricted Server Relay System
After=network.target redis-server.service
Wants=redis-server.service

[Service]
Type=simple
ExecStart=/usr/local/bin/relayra run
WorkingDirectory=/opt/relayra
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=relayra

[Install]
WantedBy=multi-user.target
`

var serviceCmd = &cobra.Command{
	Use:   "service [install|uninstall|start|stop|restart|status]",
	Short: "Manage Relayra systemd service",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		action := strings.ToLower(args[0])
		switch action {
		case "install":
			return serviceInstall()
		case "uninstall":
			return serviceUninstall()
		case "start", "stop", "restart", "status":
			return serviceCtl(action)
		default:
			return fmt.Errorf("unknown action: %s (use install|uninstall|start|stop|restart|status)", action)
		}
	},
}

func serviceInstall() error {
	unitPath := "/etc/systemd/system/relayra.service"
	if err := os.WriteFile(unitPath, []byte(systemdUnit), 0644); err != nil {
		return fmt.Errorf("write systemd unit file: %w\nTry running with sudo.", err)
	}
	fmt.Printf("Systemd unit installed at %s\n", unitPath)

	// Reload systemd
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s: %w", string(out), err)
	}
	fmt.Println("Systemd daemon reloaded")

	// Enable on boot
	if out, err := exec.Command("systemctl", "enable", "relayra").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %s: %w", string(out), err)
	}
	fmt.Println("Relayra service enabled (starts on boot)")
	fmt.Println("Run 'relayra service start' to start now")

	// Also ensure the log directory exists
	logDir := "/opt/relayra/logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create log dir %s: %v\n", logDir, err)
	}

	// Ensure the symlink exists
	binSrc := "/opt/relayra/relayra"
	binDst := "/usr/local/bin/relayra"
	if _, err := os.Stat(binSrc); err == nil {
		os.Remove(binDst) // Remove old symlink if exists
		if err := os.Symlink(binSrc, binDst); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not create symlink %s -> %s: %v\n", binDst, binSrc, err)
		}
	}

	_ = filepath.Clean(binDst) // Avoid unused import
	return nil
}

func serviceCtl(action string) error {
	out, err := exec.Command("systemctl", action, "relayra").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s relayra: %s: %w", action, string(out), err)
	}
	if action == "status" {
		fmt.Print(string(out))
	} else {
		fmt.Printf("Relayra service %s successful\n", action)
	}
	return nil
}

func serviceUninstall() error {
	// Stop service if running
	_ = exec.Command("systemctl", "stop", "relayra").Run()
	_ = exec.Command("systemctl", "disable", "relayra").Run()

	// Flush Relayra data from Redis
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		cfg = config.DefaultConfig()
	}
	redisAddr := fmt.Sprintf("%s:%d", cfg.RedisAddr, cfg.RedisPort)
	rdb, err := store.NewRedis(redisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not connect to Redis at %s: %v\n", redisAddr, err)
		fmt.Fprintln(os.Stderr, "  Relayra keys were NOT removed. Run 'redis-cli KEYS \"relayra:*\"' to check.")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		deleted, flushErr := rdb.FlushAll(ctx)
		cancel()
		rdb.Close()
		if flushErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to flush Redis data: %v\n", flushErr)
		} else {
			fmt.Printf("Deleted %d Relayra keys from Redis.\n", deleted)
		}
	}

	// Remove systemd unit
	unitPath := "/etc/systemd/system/relayra.service"
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w\nTry running with sudo.", err)
	}

	// Reload systemd
	_ = exec.Command("systemctl", "daemon-reload").Run()

	// Remove symlink
	os.Remove("/usr/local/bin/relayra")

	// Remove install directory
	if err := os.RemoveAll("/opt/relayra"); err != nil {
		return fmt.Errorf("remove /opt/relayra: %w", err)
	}

	fmt.Println("Relayra uninstalled successfully.")
	return nil
}
