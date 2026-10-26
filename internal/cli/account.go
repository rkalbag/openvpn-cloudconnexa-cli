package cli

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/api"
	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/config"
	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/keychain"
)

func newLogoutCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored credentials (keychain item and config)",
		Long: `Remove the client secret from the OS keychain and delete the ccx config.
Hosts are left as they are; delete them first with ` + "`ccx rm`" + ` if you no longer need them.
To revoke the credential itself, delete it in the Admin Portal.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Load()
			if errors.Is(err, config.ErrNotFound) {
				a.printf("Not logged in.\n")
				return nil
			}
			if err != nil {
				// A broken config still gets removed.
				a.warnf("%v", err)
			} else if err := a.keys.Delete(cfg.CloudID, cfg.ClientID); err != nil && !errors.Is(err, keychain.ErrNotFound) {
				return err
			}
			if err := config.Remove(); err != nil {
				return err
			}
			a.printf("Logged out. To revoke the credential itself, delete it in the Admin Portal (API & Logs → API).\n")
			return nil
		},
	}
}

func newDoctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, keychain, credentials, topology and region",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return a.doctor(cmd.Context()) },
	}
}

func (a *app) doctor(ctx context.Context) error {
	failed := false
	ok := func(format string, args ...any) { a.printf("  ok    "+format+"\n", args...) }
	warn := func(format string, args ...any) { a.printf("  warn  "+format+"\n", args...) }
	fail := func(format string, args ...any) {
		failed = true
		a.printf("  FAIL  "+format+"\n", args...)
	}
	done := func() error {
		if failed {
			return &exitError{code: 1, err: errors.New("doctor found problems")}
		}
		a.printf("All checks passed.\n")
		return nil
	}

	a.printf("ccx %s\n", Version)
	cfg, err := config.Load()
	if err != nil {
		fail("config: %v", err)
		return done()
	}
	p, _ := config.Path()
	ok("config: %s", p)

	secret, err := a.keys.Get(cfg.CloudID, cfg.ClientID)
	if err != nil {
		fail("keychain: %v", err)
		return done()
	}
	ok("keychain: secret for %s", keychain.Account(cfg.CloudID, cfg.ClientID))

	switch age := cfg.CredentialAge(a.now()); {
	case !age.Known:
		warn("credential expiry unknown: re-run `ccx login` and give the lifetime chosen in the portal")
	case age.Left <= 0:
		fail("credential probably expired around %s (estimated): create a new one and run `ccx login`", age.ExpiresAt.Format("2 Jan 2006"))
	case age.Warn:
		warn("credential expires around %s, in %d days (estimated): create a new one soon", age.ExpiresAt.Format("2 Jan 2006"), int(age.Left/(24*time.Hour)))
	default:
		ok("credential expires around %s (estimated)", age.ExpiresAt.Format("2 Jan 2006"))
	}

	client, err := a.newClient(cfg, secret)
	if err != nil {
		fail("API client: %v", err)
		return done()
	}
	if err := client.Authenticate(ctx); err != nil {
		fail("API token from %s.api.openvpn.com: %v", cfg.CloudID, err)
		return done()
	}
	ok("API token from %s.api.openvpn.com", cfg.CloudID)

	regions, err := client.Regions(ctx)
	switch {
	case err != nil:
		fail("regions: %v", err)
	case !slices.ContainsFunc(regions, func(r api.Region) bool { return r.ID == cfg.RegionID }):
		fail("default region %q is not offered any more: re-run `ccx login` to pick another", cfg.RegionID)
	default:
		ok("default region %s", cfg.RegionID)
	}

	switch topo, err := client.Topology(ctx); {
	case err != nil:
		warn("topology: %v (give the credential Settings – Read Only)", err)
	case topo != api.TopologyFullMesh:
		warn("topology is %s: new hosts may not be reachable until an access group grants access", clean(topo))
	default:
		ok("topology %s", topo)
	}

	if hosts, err := client.ListHosts(ctx); err != nil {
		fail("list hosts: %v (give the credential Host – Modify)", err)
	} else {
		n := 0
		for i := range hosts {
			if isCCX(&hosts[i]) {
				n++
			}
		}
		ok("hosts: %d created by ccx, %d in total", n, len(hosts))
	}
	return done()
}
