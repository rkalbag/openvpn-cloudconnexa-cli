package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/org/ccx/internal/api"
)

// nameRe is a DNS label: lowercase letters, digits, hyphens, 1–63 chars,
// not starting or ending with a hyphen.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// pushTargetRe guards the ssh destination against option injection.
var pushTargetRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._:\[\]-]+$|^[A-Za-z0-9.:\[\]][A-Za-z0-9._:\[\]-]*$`)

// rollbackTimeout bounds the cleanup after a failed add.
const rollbackTimeout = 30 * time.Second

type addOpts struct {
	region   string
	domain   string
	services []string
	push     string
}

func newAddCmd(a *app) *cobra.Command {
	var o addOpts
	cmd := &cobra.Command{
		Use:   "add <name> [service...]",
		Short: "Add a Linux server and print its one-line install command",
		Long: `Add a Linux server: creates a CloudConnexa host with one connector and one IP
service, then prints the command to paste on the server.

Services limit which traffic reaches the host (default: all traffic):
  ccx add web1                     all traffic
  ccx add web1 ssh http https      only SSH, HTTP and HTTPS
  ccx add db1 ssh tcp:5432         SSH and TCP 5432
Predefined services: ` + strings.ToLower(strings.Join(api.NamedServiceTypes, ", ")) + `.
Also tcp:<port>, tcp:<lo>-<hi>, udp:<port>, icmp and any.

The host uses a CloudConnexa seat from now until ` + "`ccx sleep` or `ccx rm`.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.add(cmd.Context(), args[0], append(args[1:], o.services...), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.region, "region", "", "region ID for the connector (default from `ccx login`)")
	f.StringVar(&o.domain, "domain", "", "domain name for the host (default <name>.<suffix>)")
	f.StringArrayVar(&o.services, "service", nil, "service to allow (repeatable; same syntax as the positional services)")
	f.StringVar(&o.push, "push", "", "install over SSH on user@host instead of printing the command (needs passwordless sudo)")
	return cmd
}

func validateDomain(d string) error {
	if len(d) > 253 {
		return fmt.Errorf("invalid domain %q: too long", d)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return fmt.Errorf("invalid domain %q: needs at least two labels, e.g. web1.internal", d)
	}
	for _, l := range labels {
		if !nameRe.MatchString(l) {
			return fmt.Errorf("invalid domain %q: labels must be lowercase letters, digits and hyphens", d)
		}
	}
	return nil
}

func (a *app) add(ctx context.Context, name string, serviceArgs []string, o addOpts) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: use 1-63 lowercase letters, digits and hyphens (not at the start or end)", name)
	}
	services, err := parseServices(serviceArgs)
	if err != nil {
		return err
	}
	if o.push != "" && !pushTargetRe.MatchString(o.push) {
		return fmt.Errorf("invalid --push target %q: expected user@host", o.push)
	}
	s, err := a.session()
	if err != nil {
		return err
	}
	domain := strings.ToLower(strings.TrimSuffix(o.domain, "."))
	if domain == "" {
		domain = name + "." + s.cfg.DomainSuffix
	}
	if err := validateDomain(domain); err != nil {
		return err
	}
	region := o.region
	if region == "" {
		region = s.cfg.RegionID
	}

	// Refuse duplicates: a second host with the same name would waste a seat
	// and make name lookups ambiguous.
	hosts, err := s.api.ListHosts(ctx)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h.Name == name {
			return fmt.Errorf("a host named %q already exists; use `ccx token %s` for a new install line, or `ccx rm %s` first", name, name, name)
		}
		if domain != "" && strings.EqualFold(h.Domain, domain) {
			return fmt.Errorf("host %q already uses the domain %s", clean(h.Name), domain)
		}
	}

	req := api.HostCreateRequest{
		Name:           name,
		Description:    descriptionPrefix + " created by ccx " + Version,
		InternetAccess: api.InternetSplitTunnelOn,
		Domain:         domain,
		Connectors:     []api.HostConnectorRequest{{Name: name + "-connector", VPNRegionID: region}},
	}
	host, err := s.api.CreateHost(ctx, req)
	if err != nil {
		// The create may have succeeded server-side even though we saw an error
		// (timeout, 5xx). Look for it so no seat is left behind.
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Status >= 500 {
			a.cleanupUnconfirmedCreate(ctx, s, name, req.Description)
		}
		return fmt.Errorf("create host: %w", err)
	}

	// From here on, any failure deletes the host.
	token, err := a.finishAdd(ctx, s, host, services, o)
	if err != nil {
		return a.rollback(ctx, s, host, err)
	}

	if o.push == "" {
		a.printInstall(name, token)
	}
	a.printf("Once online: `ccx status %s`, then `ssh user@%s`\n", name, domain)
	return nil
}

// finishAdd runs the steps after host creation and returns the profile token.
func (a *app) finishAdd(ctx context.Context, s *session, host *api.Host, services api.ServiceConfig, o addOpts) (api.Secret, error) {
	conn, err := s.connector(ctx, host)
	if err != nil {
		return "", err
	}
	if _, err := s.api.CreateIPService(ctx, host.ID, api.IPServiceRequest{
		Name:        host.Name + "-services",
		Description: descriptionPrefix + " " + describeServices(services),
		Config:      services,
	}); err != nil {
		return "", fmt.Errorf("create IP service: %w", err)
	}
	token, err := s.api.ProfileToken(ctx, conn.ID)
	if err != nil {
		return "", fmt.Errorf("create connector profile token: %w", err)
	}
	if o.push != "" {
		if err := a.push(ctx, o.push, token); err != nil {
			return "", fmt.Errorf("install over SSH on %s: %w", o.push, err)
		}
	}
	return token, nil
}

// push runs install.sh on the target over SSH. The token goes over the SSH
// session's stdin, never on a command line.
func (a *app) push(ctx context.Context, target string, token api.Secret) error {
	remote := fmt.Sprintf(`sudo -n bash -c 'f=$(mktemp) && curl -fsSL %s -o "$f" && bash "$f" -; rc=$?; rm -f "$f"; exit $rc'`, installScriptURL())
	fmt.Fprintf(a.errOut, "Installing on %s over SSH...\n", target)
	err := a.runSSH(ctx, []string{"-o", "BatchMode=yes", "--", target, remote}, strings.NewReader(string(token)+"\n"))
	if err != nil {
		return fmt.Errorf("%w (--push needs key-based SSH and passwordless sudo; otherwise run `ccx add` without --push and paste the line)", err)
	}
	return nil
}

// rollback deletes a host created in this run and reports both errors.
func (a *app) rollback(ctx context.Context, s *session, host *api.Host, cause error) error {
	fmt.Fprintf(a.errOut, "Error: %v\nRolling back: deleting host %s...\n", cause, host.Name)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := s.api.DeleteHost(rctx, host.ID); err != nil {
		return fmt.Errorf("%w; ROLLBACK FAILED, host %s (id %s) still uses a seat: delete it with `ccx rm %s --yes` (%w)",
			cause, host.Name, host.ID, host.Name, err)
	}
	return fmt.Errorf("%w (host %s was deleted; nothing was left behind)", cause, host.Name)
}

// cleanupUnconfirmedCreate deletes a host this run may have created even
// though CreateHost reported an error.
func (a *app) cleanupUnconfirmedCreate(ctx context.Context, s *session, name, description string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	hosts, err := s.api.ListHosts(rctx)
	if err != nil {
		a.warnf("could not check whether host %s was created anyway (%v); check `ccx ls`", name, err)
		return
	}
	for _, h := range hosts {
		if h.Name == name && h.Description == description {
			if err := s.api.DeleteHost(rctx, h.ID); err != nil {
				a.warnf("host %s was created despite the error and could not be deleted (%v): run `ccx rm %s --yes`", name, err, name)
			} else {
				fmt.Fprintf(a.errOut, "Host %s was created despite the error; deleted it.\n", name)
			}
		}
	}
}
