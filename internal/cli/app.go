package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"

	"github.com/org/ccx/internal/api"
	"github.com/org/ccx/internal/config"
	"github.com/org/ccx/internal/keychain"
)

// RepoOwner is the GitHub owner of the ccx repo, used in install lines.
// TODO: set to the real org (also update the module path).
var RepoOwner = "org"

// descriptionPrefix marks hosts created by ccx.
const descriptionPrefix = "[ccx]"

// app holds everything commands touch outside the process, so tests can fake it.
type app struct {
	in     *bufio.Reader
	out    io.Writer
	errOut io.Writer

	keys       keychain.Store
	apiBaseURL string // overrides https://<cloud-id>.api.openvpn.com (tests)
	logger     *slog.Logger

	// stdinIsTerminal reports whether prompts can use hidden input.
	stdinIsTerminal bool
	readPassword    func() (string, error)

	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
	lookupHost func(ctx context.Context, host string) ([]string, error)
	// runSSH runs the system ssh with args, wiring stdin and the app's output.
	runSSH func(ctx context.Context, args []string, stdin io.Reader) error
}

func newOSApp() *app {
	stdinFD := int(os.Stdin.Fd()) //nolint:gosec // G115: fd fits in int
	a := &app{
		in:              bufio.NewReader(os.Stdin),
		out:             os.Stdout,
		errOut:          os.Stderr,
		keys:            keychain.OS{},
		logger:          slog.New(slog.DiscardHandler),
		stdinIsTerminal: term.IsTerminal(stdinFD),
		now:             time.Now,
		sleep:           sleepCtx,
		lookupHost:      net.DefaultResolver.LookupHost,
	}
	a.readPassword = func() (string, error) {
		b, err := term.ReadPassword(stdinFD)
		return string(b), err
	}
	a.runSSH = func(ctx context.Context, args []string, stdin io.Reader) error {
		cmd := exec.CommandContext(ctx, "ssh", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, a.out, a.errOut
		if stdin == nil {
			cmd.Stdin = os.Stdin
		}
		return cmd.Run()
	}
	return a
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.out, format, args...) }
func (a *app) warnf(format string, args ...any) {
	fmt.Fprintf(a.errOut, "warning: "+format+"\n", args...)
}

// session is a logged-in context: config plus API client.
type session struct {
	cfg *config.Config
	api *api.Client
}

func (a *app) newClient(cfg *config.Config, secret string) (*api.Client, error) {
	return api.New(api.Config{
		CloudID:      cfg.CloudID,
		BaseURL:      a.apiBaseURL,
		ClientID:     cfg.ClientID,
		ClientSecret: api.Secret(secret),
		Logger:       a.logger,
	})
}

// session loads config and the keychain secret.
func (a *app) session() (*session, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	secret, err := a.keys.Get(cfg.CloudID, cfg.ClientID)
	if err != nil {
		return nil, err
	}
	c, err := a.newClient(cfg, secret)
	if err != nil {
		return nil, err
	}
	return &session{cfg: cfg, api: c}, nil
}

// isCCX reports whether ccx created the host.
func isCCX(h *api.Host) bool { return strings.HasPrefix(h.Description, descriptionPrefix) }

// findHost returns the single host with the given name.
func (s *session) findHost(ctx context.Context, name string) (*api.Host, error) {
	hosts, err := s.api.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	var found []api.Host
	for _, h := range hosts {
		if h.Name == name && h.ID != "" {
			found = append(found, h)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no host named %q (see `ccx ls --all`)", name)
	case 1:
		return &found[0], nil
	}
	ids := make([]string, len(found))
	for i, h := range found {
		ids[i] = h.ID
	}
	return nil, fmt.Errorf("%d hosts are named %q (ids %s); rename or delete the extras in the Admin Portal", len(found), name, strings.Join(ids, ", "))
}

// connector returns the host's single connector, fetching it if the host
// response did not include it.
func (s *session) connector(ctx context.Context, h *api.Host) (*api.Connector, error) {
	conns := h.Connectors
	if len(conns) == 0 {
		var err error
		if conns, err = s.api.ListConnectors(ctx, h.ID); err != nil {
			return nil, err
		}
	}
	var valid []api.Connector
	for _, c := range conns {
		if c.ID != "" {
			valid = append(valid, c)
		}
	}
	switch len(valid) {
	case 0:
		return nil, fmt.Errorf("host %q has no connector", h.Name)
	case 1:
		return &valid[0], nil
	}
	return nil, fmt.Errorf("host %q has %d connectors; ccx manages single-connector hosts only", h.Name, len(valid))
}

// requireCCX refuses hosts ccx did not create unless forced.
func requireCCX(h *api.Host, force bool, verb string) error {
	if isCCX(h) || force {
		return nil
	}
	return fmt.Errorf("host %q was not created by ccx (description does not start with %s); refusing to %s it without --force", h.Name, descriptionPrefix, verb)
}

// prompt asks for a line of input, returning def on empty input.
func (a *app) prompt(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(a.errOut, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(a.errOut, "%s: ", label)
	}
	line, err := a.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s: no input (pass it as a flag for non-interactive use)", strings.ToLower(label))
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// confirm asks a yes/no question; anything but y/yes is no.
func (a *app) confirm(question string) (bool, error) {
	ans, err := a.prompt(question+" [y/N]", "")
	if err != nil {
		return false, err
	}
	ans = strings.ToLower(ans)
	return ans == "y" || ans == "yes", nil
}

// clean makes API-provided text safe to print to a terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// installLine is the command a user pastes on the server.
func installLine(token api.Secret) string {
	return fmt.Sprintf("curl -fsSL %s | sudo bash -s -- %s", installScriptURL(), string(token))
}

func installScriptURL() string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/ccx/%s/scripts/install.sh", RepoOwner, InstallRef)
}

// printInstall prints the install instructions. This and printing in
// `ccx token` are the only places a profile token is shown.
func (a *app) printInstall(name string, token api.Secret) {
	a.printf("Run this on %s (a new token invalidates this one):\n  %s\n", name, installLine(token))
}
