package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/org/ccx/internal/api"
)

// statusPollInterval is how often `ccx status --wait` re-reads the connector.
const statusPollInterval = 5 * time.Second

// dnsTimeout bounds the name lookup in `ccx ssh`.
const dnsTimeout = 3 * time.Second

// exitError carries a process exit code. With a nil err, nothing is printed
// (the child process, e.g. ssh, already reported the problem).
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}
func (e *exitError) Unwrap() error { return e.err }
func (e *exitError) ExitCode() int { return e.code }

// exitCoder is an error that sets the process exit code.
type exitCoder interface {
	error
	ExitCode() int
}

func asExitCoder(err error, target *exitCoder) bool { return errors.As(err, target) }

// silent reports whether Execute should skip printing err.
func silent(err error) bool {
	var e *exitError
	return errors.As(err, &e) && e.err == nil
}

// hostRow is one line of `ccx ls`, also its JSON shape.
type hostRow struct {
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	Status      string `json:"status"`
	IPv4        string `json:"ipv4"`
	Region      string `json:"region"`
	CCX         bool   `json:"ccx"`
	HostID      string `json:"hostId"`
	ConnectorID string `json:"connectorId"`
}

func newLsCmd(a *app) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List hosts created by ccx",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.ls(cmd.Context(), all, asJSON)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list every host, not only those created by ccx")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (a *app) ls(ctx context.Context, all, asJSON bool) error {
	s, err := a.session()
	if err != nil {
		return err
	}
	hosts, err := s.api.ListHosts(ctx)
	if err != nil {
		return err
	}
	// Host responses normally embed their connectors; fetch them in one list
	// call if any host came back without.
	var byHost map[string][]api.Connector
	for _, h := range hosts {
		if len(h.Connectors) == 0 && (all || isCCX(&h)) {
			conns, err := s.api.ListConnectors(ctx, "")
			if err != nil {
				return err
			}
			byHost = map[string][]api.Connector{}
			for _, c := range conns {
				byHost[c.NetworkItemID] = append(byHost[c.NetworkItemID], c)
			}
			break
		}
	}

	rows := []hostRow{}
	for _, h := range hosts {
		if h.ID == "" || (!all && !isCCX(&h)) {
			continue
		}
		r := hostRow{Name: clean(h.Name), Domain: clean(h.Domain), CCX: isCCX(&h), HostID: clean(h.ID)}
		conns := h.Connectors
		if len(conns) == 0 {
			conns = byHost[h.ID]
		}
		if len(conns) > 0 {
			// Multi-connector hosts (made outside ccx) show the first.
			c := conns[0]
			r.Status, r.IPv4, r.Region, r.ConnectorID = clean(c.ConnectionStatus), clean(c.IPv4Address), clean(c.VPNRegionID), clean(c.ID)
		}
		rows = append(rows, r)
	}

	if asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		if all {
			a.printf("No hosts.\n")
		} else {
			a.printf("No ccx hosts. Add one with `ccx add <name>` (or see every host with `ccx ls --all`).\n")
		}
		return nil
	}
	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDOMAIN\tSTATUS\tTUNNEL IPV4\tREGION")
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	for _, r := range rows {
		name := r.Name
		if all && !r.CCX {
			name += " *"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, dash(r.Domain), dash(r.Status), dash(r.IPv4), dash(r.Region))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if all {
		a.printf("* not created by ccx\n")
	}
	return nil
}

func newStatusCmd(a *app) *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Show whether a host's connector is online",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.status(cmd.Context(), args[0], wait)
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 0, "poll every 5s until ONLINE or this long has passed, e.g. 120s")
	return cmd
}

func (a *app) status(ctx context.Context, name string, wait time.Duration) error {
	if wait < 0 {
		return errors.New("--wait must not be negative")
	}
	s, err := a.session()
	if err != nil {
		return err
	}
	h, err := s.findHost(ctx, name)
	if err != nil {
		return err
	}
	c, err := s.connector(ctx, h)
	if err != nil {
		return err
	}
	deadline := a.now().Add(wait)
	waiting := false
	for {
		c, err = s.api.GetConnector(ctx, c.ID)
		if err != nil {
			return err
		}
		if c.ConnectionStatus == api.StatusOnline || !a.now().Before(deadline) {
			break
		}
		if !waiting {
			fmt.Fprintf(a.errOut, "Waiting up to %s for %s to come online...\n", wait, name)
			waiting = true
		}
		if err := a.sleep(ctx, min(statusPollInterval, deadline.Sub(a.now()))); err != nil {
			return err
		}
	}

	st := clean(c.ConnectionStatus)
	if st == "" {
		st = "UNKNOWN"
	}
	a.printf("%s: %s\n", clean(h.Name), st)
	a.printf("  domain:      %s\n", clean(h.Domain))
	if c.IPv4Address != "" {
		a.printf("  tunnel IPv4: %s\n", clean(c.IPv4Address))
	}
	a.printf("  region:      %s\n", clean(c.VPNRegionID))
	if c.ConnectionStatus == api.StatusOnline {
		return nil
	}
	if wait > 0 {
		return &exitError{code: 2, err: fmt.Errorf("%s did not come online within %s; check `journalctl -u openvpn3-session@*` on the server", name, wait)}
	}
	return &exitError{code: 2}
}

func newTokenCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "token <name>",
		Short: "Print a fresh install command for a host (invalidates the previous token)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, h, c, err := a.ccxHostConnector(ctx, args[0], force, "issue a token for")
			if err != nil {
				return err
			}
			tok, err := s.api.ProfileToken(ctx, c.ID)
			if err != nil {
				return fmt.Errorf("create connector profile token: %w", err)
			}
			a.printInstall(clean(h.Name), tok)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "allow hosts not created by ccx")
	return cmd
}

// ccxHostConnector looks up a host by name, refuses non-ccx hosts unless
// forced, and returns its connector.
func (a *app) ccxHostConnector(ctx context.Context, name string, force bool, verb string) (*session, *api.Host, *api.Connector, error) {
	s, err := a.session()
	if err != nil {
		return nil, nil, nil, err
	}
	h, err := s.findHost(ctx, name)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := requireCCX(h, force, verb); err != nil {
		return nil, nil, nil, err
	}
	c, err := s.connector(ctx, h)
	if err != nil {
		return nil, nil, nil, err
	}
	return s, h, c, nil
}

func newSleepCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "sleep <name>",
		Short: "Suspend a host's connector: frees its seat and stops connectivity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, h, c, err := a.ccxHostConnector(ctx, args[0], force, "suspend")
			if err != nil {
				return err
			}
			if err := s.api.SuspendConnector(ctx, c.ID); err != nil {
				return err
			}
			a.printf("Suspended %s: its seat is free and it is unreachable until `ccx wake %s`.\n", clean(h.Name), clean(h.Name))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "allow hosts not created by ccx")
	return cmd
}

func newWakeCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "wake <name>",
		Short: "Re-activate a suspended connector (uses a seat again)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, h, c, err := a.ccxHostConnector(ctx, args[0], force, "activate")
			if err != nil {
				return err
			}
			if err := s.api.ActivateConnector(ctx, c.ID); err != nil {
				return err
			}
			a.printf("Activated %s: it uses a seat again and reconnects shortly (`ccx status %s --wait 2m`).\n", clean(h.Name), clean(h.Name))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "allow hosts not created by ccx")
	return cmd
}

func newRmCmd(a *app) *cobra.Command {
	var yes, force bool
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a host, its connector and its profiles (frees the seat)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.session()
			if err != nil {
				return err
			}
			h, err := s.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			if err := requireCCX(h, force, "delete"); err != nil {
				return err
			}
			if !yes {
				ok, err := a.confirm(fmt.Sprintf("Delete host %s (%s)? The server loses access immediately", clean(h.Name), clean(h.Domain)))
				if err != nil {
					return fmt.Errorf("%w; pass --yes to skip the confirmation", err)
				}
				if !ok {
					return errors.New("aborted")
				}
			}
			if err := s.api.DeleteHost(ctx, h.ID); err != nil {
				return err
			}
			a.printf("Deleted %s; its connector seat is free.\n", clean(h.Name))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "allow hosts not created by ccx")
	return cmd
}

// sshUserRe is a conservative login name.
var sshUserRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)

func newSSHCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "ssh [user@]<name> [-- ssh-args...]",
		Short: "SSH to a host by name, falling back to its tunnel IP",
		Long: `SSH to a host by its domain name. If the name does not resolve (the laptop is
not connected with OpenVPN Connect, or DNS is not set up yet), ccx uses the
connector's tunnel IPv4 instead.

Arguments after -- go to ssh after the destination: options, then a command.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dash := cmd.ArgsLenAtDash(); dash > 1 || (dash < 0 && len(args) > 1) {
				return errors.New("put ssh arguments after --, e.g. ccx ssh web1 -- -p 2222 uptime")
			}
			return a.ssh(cmd.Context(), args[0], args[1:])
		},
	}
}

func (a *app) ssh(ctx context.Context, target string, extra []string) error {
	user, name, hasUser := strings.Cut(target, "@")
	if !hasUser {
		user, name = "", target
	} else if !sshUserRe.MatchString(user) {
		return fmt.Errorf("invalid user %q", user)
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid host name %q", name)
	}
	s, err := a.session()
	if err != nil {
		return err
	}
	h, err := s.findHost(ctx, name)
	if err != nil {
		return err
	}
	dest := strings.ToLower(h.Domain)
	if validateDomain(dest) != nil {
		dest = ""
	}
	if dest != "" {
		lctx, cancel := context.WithTimeout(ctx, dnsTimeout)
		_, err := a.lookupHost(lctx, dest)
		cancel()
		if err != nil {
			dest = ""
		}
	}
	if dest == "" {
		c, err := s.connector(ctx, h)
		if err != nil {
			return err
		}
		ip := c.IPv4Address
		if !isIPv4(ip) {
			return fmt.Errorf("%s does not resolve and the connector has no tunnel IPv4; connect with OpenVPN Connect and check `ccx status %s`", clean(h.Domain), name)
		}
		a.warnf("%s does not resolve; using tunnel IP %s (is OpenVPN Connect connected?)", clean(h.Domain), ip)
		dest = ip
	}
	if user != "" {
		dest = user + "@" + dest
	}
	err = a.runSSH(ctx, append([]string{dest}, extra...), nil)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &exitError{code: ee.ExitCode()}
	}
	if err != nil {
		return fmt.Errorf("run ssh: %w", err)
	}
	return nil
}

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 || strings.Trim(p, "0123456789") != "" {
			return false
		}
		n := 0
		for _, d := range p {
			n = n*10 + int(d-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}
