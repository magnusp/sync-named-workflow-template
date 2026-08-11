package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Populated at build time via GoReleaser ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "sync-named-workflow-template",
	Short: "sync-named-workflow-template CLI",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("sync-named-workflow-template: use --version to view build information.")
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built at: %s)", Version, Commit, Date)
}
