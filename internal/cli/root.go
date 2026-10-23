// Package cli implements the ccx cobra commands.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Set at build time via -ldflags "-X github.com/org/ccx/internal/cli.Version=... -X ...InstallRef=...".
var (
	// Version is the ccx release version.
	Version = "dev"
	// InstallRef is the git ref of scripts/install.sh used in printed install lines.
	InstallRef = "main"
)

// NewRootCmd builds the ccx command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "ccx",
		Short:         "Tailscale-simple server access for CloudConnexa (unofficial)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ccx version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "ccx %s\n", Version)
		},
	}
}

// Execute runs the root command and returns the process exit code.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ccx:", err)
		return 1
	}
	return 0
}
