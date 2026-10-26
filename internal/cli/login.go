package cli

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/org/ccx/internal/api"
	"github.com/org/ccx/internal/config"
)

// v1.json patterns for OAuth credentials.
var (
	clientIDRe     = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	clientSecretRe = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

type loginOpts struct {
	cloudID, clientID, suffix, region, lifetime string
	secretStdin                                 bool
}

func newLoginCmd(a *app) *cobra.Command {
	var o loginOpts
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store CloudConnexa API credentials (secret goes to the OS keychain)",
		Long: `Store CloudConnexa API credentials.

Create them in the Admin Portal under API & Logs → API → Create Credentials with
Host – Modify, Connector Profile – Modify, Region – Read Only and Settings – Read Only.
The client secret is stored only in the OS keychain.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.login(cmd, o) },
	}
	f := cmd.Flags()
	f.StringVar(&o.cloudID, "cloud-id", "", "CloudConnexa Cloud ID (the <id> in <id>.openvpn.com)")
	f.StringVar(&o.clientID, "client-id", "", "API client ID")
	f.StringVar(&o.suffix, "suffix", "", "domain suffix for new hosts (default "+config.DefaultDomainSuffix+")")
	f.StringVar(&o.region, "region", "", "default region ID for new connectors")
	f.StringVar(&o.lifetime, "lifetime", "", "credential lifetime chosen in the portal: 2w, 1m, 6m or 1y")
	f.BoolVar(&o.secretStdin, "secret-stdin", false, "read the client secret from the first line of stdin")
	return cmd
}

func (a *app) login(cmd *cobra.Command, o loginOpts) error {
	ctx := cmd.Context()
	// With --secret-stdin the secret is the first stdin line, before any prompt.
	var secret string
	if o.secretStdin {
		line, err := a.in.ReadString('\n')
		if err != nil && line == "" {
			return errors.New("--secret-stdin: no secret on stdin")
		}
		secret = strings.TrimSpace(line)
	}

	prev, _ := config.Load() // previous login, if any, for defaults and cleanup
	def := func(flag, fallback string) string {
		if flag != "" {
			return flag
		}
		return fallback
	}
	var prevCloud, prevClient, prevSuffix, prevLifetime string
	if prev != nil {
		prevCloud, prevClient, prevSuffix, prevLifetime = prev.CloudID, prev.ClientID, prev.DomainSuffix, string(prev.CredentialLifetime)
	}

	cloudID := o.cloudID
	if cloudID == "" {
		var err error
		if cloudID, err = a.prompt("Cloud ID", prevCloud); err != nil {
			return err
		}
	}
	cloudID = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cloudID), ".openvpn.com"))
	if _, err := api.BaseURLForCloudID(cloudID); err != nil {
		return err
	}

	clientID := o.clientID
	if clientID == "" {
		var err error
		if clientID, err = a.prompt("Client ID", prevClient); err != nil {
			return err
		}
	}
	if !clientIDRe.MatchString(clientID) {
		return errors.New("invalid client ID: expected letters, digits, '.' and '-'")
	}

	if !o.secretStdin {
		if !a.stdinIsTerminal {
			return errors.New("stdin is not a terminal: use --secret-stdin to pass the client secret")
		}
		fmt.Fprint(a.errOut, "Client secret (input hidden): ")
		s, err := a.readPassword()
		fmt.Fprintln(a.errOut)
		if err != nil {
			return fmt.Errorf("read client secret: %w", err)
		}
		secret = strings.TrimSpace(s)
	}
	if !clientSecretRe.MatchString(secret) {
		// Never echo the value.
		return errors.New("invalid client secret: expected only letters and digits (copy it again from the portal)")
	}

	lifetimeIn := o.lifetime
	if lifetimeIn == "" {
		var err error
		if lifetimeIn, err = a.prompt("Credential lifetime chosen in the portal (2w, 1m, 6m, 1y)", def(prevLifetime, string(config.Lifetime1Year))); err != nil {
			return err
		}
	}
	lifetime, err := config.ParseLifetime(lifetimeIn)
	if err != nil {
		return err
	}

	suffix := o.suffix
	if suffix == "" {
		if suffix, err = a.prompt("Domain suffix for new hosts", def(prevSuffix, config.DefaultDomainSuffix)); err != nil {
			return err
		}
	}
	suffix = strings.Trim(strings.ToLower(strings.TrimSpace(suffix)), ".")
	if err := config.ValidateDomainSuffix(suffix); err != nil {
		return err
	}

	cfg := &config.Config{CloudID: cloudID, ClientID: clientID, DomainSuffix: suffix, CredentialLifetime: lifetime}
	client, err := a.newClient(cfg, secret)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.errOut, "Checking credentials...")
	if err := client.Authenticate(ctx); err != nil {
		switch {
		case errors.Is(err, api.ErrUnauthorized):
			return errors.New("credentials rejected: check the Cloud ID, client ID and secret, and that the credential has not expired")
		case errors.Is(err, api.ErrForbidden):
			return errors.New("access denied: check the plan is Essential or higher (Starter has no API) and the credential's permissions")
		}
		return err
	}

	regions, err := client.Regions(ctx)
	if err != nil {
		if errors.Is(err, api.ErrForbidden) {
			return errors.New("cannot list regions: give the credential Region – Read Only")
		}
		return err
	}
	if len(regions) == 0 {
		return errors.New("the API returned no regions")
	}
	sortRegions(regions)
	prevRegion := ""
	if prev != nil {
		prevRegion = prev.RegionID
	}
	region, err := a.chooseRegion(regions, o.region, prevRegion)
	if err != nil {
		return err
	}
	cfg.RegionID = region.ID

	topo, err := client.Topology(ctx)
	switch {
	case err != nil:
		a.warnf("could not read the WPC topology (%v); give the credential Settings – Read Only", err)
	case topo != api.TopologyFullMesh:
		a.warnf("topology is %s: new hosts may not be reachable until an access group grants access", clean(topo))
	}

	cfg.CredentialCreatedAt = a.now().UTC().Truncate(1e9)
	if err := a.keys.Set(cloudID, clientID, secret); err != nil {
		return fmt.Errorf("%w (ccx stores the secret only in the OS keychain)", err)
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if prev != nil && (prev.CloudID != cloudID || prev.ClientID != clientID) {
		if err := a.keys.Delete(prev.CloudID, prev.ClientID); err != nil {
			a.warnf("could not remove the previous keychain item: %v", err)
		}
	}

	p, _ := config.Path()
	a.printf("Logged in to %s.api.openvpn.com\n", cloudID)
	a.printf("  client ID:      %s\n", clientID)
	a.printf("  default region: %s (%s)\n", region.ID, clean(regionLabel(region)))
	a.printf("  domain suffix:  .%s\n", suffix)
	if exp, ok := lifetime.ExpiresAt(cfg.CredentialCreatedAt); ok {
		a.printf("  credential:     %s, expires around %s (estimated from today)\n", lifetime.Label(), exp.Format("2 Jan 2006"))
	}
	a.printf("  secret:         stored in the OS keychain\n  config:         %s\n", p)
	a.printf("Next: ccx add <name>\n")
	return nil
}

// sortRegions orders by continent, then country, then region name.
func sortRegions(rs []api.Region) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Continent != b.Continent {
			return a.Continent < b.Continent
		}
		if a.Country != b.Country {
			return a.Country < b.Country
		}
		return a.RegionName < b.RegionName
	})
}

func regionLabel(r api.Region) string {
	parts := []string{}
	for _, p := range []string{r.RegionName, r.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// chooseRegion validates a --region flag or prompts with a numbered list.
func (a *app) chooseRegion(regions []api.Region, flag, prev string) (api.Region, error) {
	byID := func(id string) (api.Region, bool) {
		for _, r := range regions {
			if r.ID == id {
				return r, true
			}
		}
		return api.Region{}, false
	}
	if flag != "" {
		if r, ok := byID(flag); ok {
			return r, nil
		}
		return api.Region{}, fmt.Errorf("unknown region %q", flag)
	}
	fmt.Fprintln(a.errOut, "Regions:")
	defIdx := ""
	continent := ""
	for i, r := range regions {
		if r.Continent != continent {
			continent = r.Continent
			fmt.Fprintf(a.errOut, "  %s\n", clean(continent))
		}
		fmt.Fprintf(a.errOut, "    %3d) %-16s %s\n", i+1, clean(r.ID), clean(regionLabel(r)))
		if r.ID == prev {
			defIdx = strconv.Itoa(i + 1)
		}
	}
	for {
		ans, err := a.prompt("Default region (number or ID)", defIdx)
		if err != nil {
			return api.Region{}, err
		}
		if n, err := strconv.Atoi(ans); err == nil && n >= 1 && n <= len(regions) {
			return regions[n-1], nil
		}
		if r, ok := byID(ans); ok {
			return r, nil
		}
		fmt.Fprintf(a.errOut, "Not a listed region: %q\n", ans)
	}
}
