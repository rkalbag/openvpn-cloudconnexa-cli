// Package cli implements the ccx cobra commands.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

// Set at build time via -ldflags "-X github.com/org/ccx/internal/cli.Version=... -X ...InstallRef=...".
var (
	// Version is the ccx release version.
	Version = "dev"
	// InstallRef is the git ref of scripts/install.sh used in printed install lines.
	InstallRef = "main"
)

// NewRootCmd builds the ccx command tree using the real OS integrations.
func NewRootCmd() *cobra.Command { return newRootCmd(newOSApp()) }

func newRootCmd(a *app) *cobra.Command {
	var debug bool
	root := &cobra.Command{
		Use:           "ccx",
		Short:         "Tailscale-simple server access for CloudConnexa (unofficial)",
		Long:          "ccx adds Linux servers to CloudConnexa with one command on your laptop and one paste on the server.\n\nUnofficial project: not made, endorsed or supported by OpenVPN Inc.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			if debug {
				// Debug logs carry method, path, status and timing only; never credentials or tokens.
				a.logger = slog.New(slog.NewTextHandler(a.errOut, &slog.HandlerOptions{Level: slog.LevelDebug}))
			}
			cmd.SetOut(a.out)
			cmd.SetErr(a.errOut)
		},
	}
	root.PersistentFlags().BoolVar(&debug, "debug", false, "log API requests (no credentials) to stderr")
	root.SetOut(a.out)
	root.SetErr(a.errOut)
	root.AddCommand(
		newLoginCmd(a),
		newAddCmd(a),
		newLsCmd(a),
		newStatusCmd(a),
		newTokenCmd(a),
		newSleepCmd(a),
		newWakeCmd(a),
		newRmCmd(a),
		newSSHCmd(a),
		newLogoutCmd(a),
		newVersionCmd(a),
		newDoctorCmd(a),
	)
	return root
}

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ccx version",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			a.printf("ccx %s\n", Version)
		},
	}
}

// Execute runs ccx and returns the process exit code. Ctrl-C cancels the
// command context; commands that must clean up (add's rollback) still do.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		if !silent(err) {
			fmt.Fprintln(os.Stderr, "ccx:", err)
		}
		var ec exitCoder
		if asExitCoder(err, &ec) {
			return ec.ExitCode()
		}
		return 1
	}
	return 0
}
