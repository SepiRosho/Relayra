package cli

import (
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/store"
	"github.com/relayra/relayra/internal/tui"
)

// runTUI loads the configuration and launches the main TUI panel.
func runTUI() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	rdb, err := store.NewRedis(cfg.RedisURL(), cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return err
	}
	defer rdb.Close()
	return tui.RunTUI(cfg, rdb)
}

// runTUISetup launches the setup wizard TUI.
func runTUISetup() error {
	return tui.RunSetupWizard()
}
