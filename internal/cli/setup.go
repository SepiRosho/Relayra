package cli

import (
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Run or re-run the configuration wizard",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSetupWizard()
	},
}

// runSetupWizard launches the TUI setup wizard.
// Implemented in tui package, called from here.
func runSetupWizard() error {
	return runTUISetup()
}
