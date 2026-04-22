package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print Relayra version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Relayra %s (built %s)\n", Version, BuildDate)
	},
}
