package cli

import (
	"fmt"
	"os"

	"github.com/relayra/relayra/internal/config"
	"github.com/spf13/cobra"
)

var (
	// Version is set via ldflags at build time.
	Version   = "0.1.1"
	BuildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "relayra",
	Short: "Relayra — Restricted Server Relay System",
	Long:  "Bridge communication between unrestricted and restricted servers using HTTP polling through proxies.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !config.Exists() {
			// No .env found — launch first-time setup wizard
			return runSetupWizard()
		}
		// .env exists — launch TUI panel
		return runTUI()
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(serviceCmd)
	rootCmd.AddCommand(pairCmd)
	rootCmd.AddCommand(peersCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(tokenCmd)
	rootCmd.AddCommand(versionCmd)
}

// Execute is the main entry point for the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
