package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/api"
	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/config"
	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/fakeapi"
	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/keychain"
)

const cloudID = "acme"

// harness runs ccx commands against a fake API, keychain and clock.
type harness struct {
	t         *testing.T
	fake      *fakeapi.Server
	keys      *keychain.Memory
	configDir string
	clock     time.Time
	out, err  bytes.Buffer
	allOutput bytes.Buffer // every run's stdout and stderr, for redaction checks

	resolves  bool                 // lookupHost succeeds
	sshCalls  [][]string           // args of each runSSH call
	sshStdin  []string             // stdin of each runSSH call
	onSleep   func()               // called on every app sleep
	sshResult func([]string) error // optional runSSH result
	debug     bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:         t,
		fake:      fakeapi.New(t),
		keys:      keychain.NewMemory(),
		configDir: t.TempDir(),
		clock:     time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		resolves:  true,
	}
	t.Setenv(config.DirEnv, h.configDir)
	t.Cleanup(func() {
		if bytes.Contains(h.allOutput.Bytes(), []byte(fakeapi.Secret)) {
			t.Errorf("client secret appeared in output:\n%s", h.allOutput.String())
		}
	})
	return h
}

func (h *harness) app(stdin string) *app {
	a := &app{
		in:              bufio.NewReader(strings.NewReader(stdin)),
		out:             &h.out,
		errOut:          &h.err,
		keys:            h.keys,
		apiBaseURL:      h.fake.URL,
		logger:          slog.New(slog.DiscardHandler),
		stdinIsTerminal: false,
		readPassword:    func() (string, error) { return "", errors.New("no terminal") },
		now:             func() time.Time { return h.clock },
	}
	a.sleep = func(_ context.Context, d time.Duration) error {
		h.clock = h.clock.Add(d)
		if h.onSleep != nil {
			h.onSleep()
		}
		return nil
	}
	a.lookupHost = func(context.Context, string) ([]string, error) {
		if h.resolves {
			return []string{"100.96.0.10"}, nil
		}
		return nil, errors.New("no such host")
	}
	a.runSSH = func(_ context.Context, args []string, stdin io.Reader) error {
		h.sshCalls = append(h.sshCalls, args)
		in := ""
		if stdin != nil {
			b, _ := io.ReadAll(stdin)
			in = string(b)
		}
		h.sshStdin = append(h.sshStdin, in)
		if h.sshResult != nil {
			return h.sshResult(args)
		}
		return nil
	}
	return a
}

// run executes ccx with args and the given stdin, resetting captured output.
func (h *harness) run(stdin string, args ...string) error {
	h.t.Helper()
	h.out.Reset()
	h.err.Reset()
	cmd := newRootCmd(h.app(stdin))
	if h.debug {
		args = append([]string{"--debug"}, args...)
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	h.allOutput.Write(h.out.Bytes())
	h.allOutput.Write(h.err.Bytes())
	if err != nil {
		h.allOutput.WriteString(err.Error())
	}
	return err
}

func (h *harness) mustRun(stdin string, args ...string) {
	h.t.Helper()
	if err := h.run(stdin, args...); err != nil {
		h.t.Fatalf("ccx %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, h.out.String(), h.err.String())
	}
}

func (h *harness) login() {
	h.t.Helper()
	h.mustRun(fakeapi.Secret+"\n", "login", "--cloud-id", cloudID, "--client-id", fakeapi.ClientID,
		"--secret-stdin", "--region", "eu-west-1", "--lifetime", "1y", "--suffix", "internal")
}

func (h *harness) host(name string) api.Host {
	h.t.Helper()
	for _, x := range h.fake.Hosts() {
		if x.Name == name {
			return x
		}
	}
	h.t.Fatalf("no host %q", name)
	return api.Host{}
}

func TestVersion(t *testing.T) {
	h := newHarness(t)
	h.mustRun("", "version")
	if got, want := h.out.String(), "ccx dev\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoginStoresSecretOnlyInKeychain(t *testing.T) {
	h := newHarness(t)
	h.debug = true
	h.login()

	got, err := h.keys.Get(cloudID, fakeapi.ClientID)
	if err != nil || got != fakeapi.Secret {
		t.Fatalf("keychain: %q, %v", got, err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CloudID != cloudID || cfg.ClientID != fakeapi.ClientID || cfg.RegionID != "eu-west-1" ||
		cfg.DomainSuffix != "internal" || cfg.CredentialLifetime != config.Lifetime1Year || !cfg.CredentialCreatedAt.Equal(h.clock) {
		t.Fatalf("config: %+v", cfg)
	}
	// Acceptance 1: nothing under the config directory contains the secret.
	_ = filepath.WalkDir(h.configDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte(fakeapi.Secret)) {
				t.Errorf("%s contains the secret", p)
			}
		}
		return nil
	})
	if !strings.Contains(h.out.String(), "stored in the OS keychain") {
		t.Errorf("summary: %s", h.out.String())
	}
}

func TestLoginInteractivePromptsAndRegionChoice(t *testing.T) {
	h := newHarness(t)
	// Region list is sorted by continent: Europe (Germany, Ireland), North America.
	stdin := strings.Join([]string{fakeapi.Secret, cloudID + ".openvpn.com", fakeapi.ClientID, "", "", "2"}, "\n") + "\n"
	h.mustRun(stdin, "login", "--secret-stdin")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CloudID != cloudID || cfg.RegionID != "eu-west-1" || cfg.DomainSuffix != "internal" || cfg.CredentialLifetime != config.Lifetime1Year {
		t.Fatalf("config: %+v", cfg)
	}
}

func TestLoginErrors(t *testing.T) {
	h := newHarness(t)
	err := h.run("Wr0ngSecretValue123\n", "login", "--cloud-id", cloudID, "--client-id", fakeapi.ClientID,
		"--secret-stdin", "--region", "eu-west-1", "--lifetime", "1y", "--suffix", "internal")
	if err == nil || !strings.Contains(err.Error(), "credentials rejected") {
		t.Fatalf("got %v", err)
	}
	if h.keys.Len() != 0 {
		t.Fatal("rejected secret was stored")
	}
	if _, err := config.Load(); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("config written: %v", err)
	}

	err = h.run("", "login", "--cloud-id", cloudID, "--client-id", fakeapi.ClientID, "--lifetime", "1y", "--suffix", "internal")
	if err == nil || !strings.Contains(err.Error(), "--secret-stdin") {
		t.Fatalf("non-terminal without --secret-stdin: %v", err)
	}

	err = h.run("bad secret!\n", "login", "--cloud-id", cloudID, "--client-id", fakeapi.ClientID, "--secret-stdin")
	if err == nil || strings.Contains(err.Error(), "bad secret") {
		t.Fatalf("invalid secret: %v", err)
	}
}

func TestLoginWarnsOnCustomTopology(t *testing.T) {
	h := newHarness(t)
	h.fake.Topology = api.TopologyCustom
	h.login()
	if !strings.Contains(h.err.String(), "access group") {
		t.Fatalf("no topology warning: %s", h.err.String())
	}
}

func TestAddDefault(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "add", "web1")

	hosts := h.fake.Hosts()
	if len(hosts) != 1 {
		t.Fatalf("hosts: %+v", hosts)
	}
	web := hosts[0]
	if web.Name != "web1" || web.Domain != "web1.internal" || !strings.HasPrefix(web.Description, "[ccx]") ||
		web.InternetAccess != api.InternetSplitTunnelOn || len(web.Connectors) != 1 ||
		web.Connectors[0].Name != "web1-connector" || web.Connectors[0].VPNRegionID != "eu-west-1" {
		t.Fatalf("host: %+v", web)
	}
	svcs := h.fake.Services(web.ID)
	if len(svcs) != 1 || svcs[0].Name != "web1-services" || !slices.Equal(svcs[0].Config.ServiceTypes, []string{api.ServiceAny}) {
		t.Fatalf("services: %+v", svcs)
	}
	tok := h.fake.ProfileToken(web.Connectors[0].ID)
	want := "Run this on web1 (a new token invalidates this one):\n  curl -fsSL https://raw.githubusercontent.com/rkalbag/openvpn-cloudconnexa-cli/main/scripts/install.sh | sudo bash -s -- " + tok + "\n"
	if !strings.HasPrefix(h.out.String(), want) {
		t.Fatalf("output:\n%s\nwant prefix:\n%s", h.out.String(), want)
	}
	if strings.Contains(h.err.String(), "Full Mesh") {
		t.Errorf("Full Mesh note for the default service: %s", h.err.String())
	}
	if strings.Count(h.out.String(), "curl ") != 1 || strings.Contains(h.err.String(), tok) {
		t.Fatalf("token printed more than once:\n%s\n%s", h.out.String(), h.err.String())
	}
	if !strings.Contains(h.out.String(), "ccx status web1") || !strings.Contains(h.out.String(), "ssh user@web1.internal") {
		t.Fatalf("no next step: %s", h.out.String())
	}
}

func TestAddServicesDomainRegion(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "add", "db1", "ssh,tcp:5432", "--service", "icmp", "--domain", "db.example.internal.", "--region", "us-east-1")
	if !strings.Contains(h.err.String(), "every port on this host is reachable") {
		t.Errorf("no Full Mesh note: %s", h.err.String())
	}
	db := h.host("db1")
	if db.Domain != "db.example.internal" || db.Connectors[0].VPNRegionID != "us-east-1" {
		t.Fatalf("host: %+v", db)
	}
	cfg := h.fake.Services(db.ID)[0].Config
	if !slices.Equal(cfg.ServiceTypes, []string{"SSH", api.ServiceCustom}) || len(cfg.CustomServiceTypes) != 2 ||
		cfg.CustomServiceTypes[0].Protocol != "TCP" || *cfg.CustomServiceTypes[0].Port[0].Value != 5432 ||
		cfg.CustomServiceTypes[1].Protocol != "ICMP" {
		t.Fatalf("config: %+v", cfg)
	}
}

func TestAddNoNoteWithCustomTopology(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.Topology = api.TopologyCustom
	h.mustRun("", "add", "web1", "ssh")
	if strings.Contains(h.err.String(), "Full Mesh") {
		t.Fatalf("note under Custom topology: %s", h.err.String())
	}
}

func TestAddValidation(t *testing.T) {
	h := newHarness(t)
	h.login()
	for _, args := range [][]string{
		{"add", "Web1"},
		{"add", "-web"},
		{"add", "web1", "gopher"},
		{"add", "web1", "tcp:0"},
		{"add", "web1", "any", "ssh"},
		{"add", "web1", "--domain", "web1"},
		{"add", "web1", "--push", "-oProxyCommand=x"},
	} {
		if err := h.run("", args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
	if n := len(h.fake.Hosts()); n != 0 {
		t.Fatalf("%d hosts created by invalid adds", n)
	}

	h.mustRun("", "add", "web1")
	if err := h.run("", "add", "web1"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := h.run("", "add", "web2", "--domain", "web1.internal"); err == nil || !strings.Contains(err.Error(), "already uses") {
		t.Fatalf("duplicate domain: %v", err)
	}
	if n := len(h.fake.Hosts()); n != 1 {
		t.Fatalf("%d hosts", n)
	}
}

func TestAddRollsBack(t *testing.T) {
	for _, route := range []string{
		"POST /api/v1/hosts/ip-services",
		"POST /api/v1/hosts/connectors/{id}/profile/encrypt",
	} {
		t.Run(route, func(t *testing.T) {
			h := newHarness(t)
			h.login()
			h.fake.Fail(route, 400)
			err := h.run("", "add", "web1")
			if err == nil || !strings.Contains(err.Error(), "was deleted") {
				t.Fatalf("got %v", err)
			}
			// Acceptance 3: nothing left behind.
			if hosts := h.fake.Hosts(); len(hosts) != 0 {
				t.Fatalf("left behind: %+v", hosts)
			}
			if strings.Contains(h.out.String(), "curl ") {
				t.Fatal("install line printed after failure")
			}
		})
	}
}

func TestAddRollbackFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.Fail("POST /api/v1/hosts/connectors/{id}/profile/encrypt", 400)
	h.fake.Fail("DELETE /api/v1/hosts/{id}", 404)
	err := h.run("", "add", "web1")
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK FAILED") || !strings.Contains(err.Error(), "ccx rm web1 --yes") {
		t.Fatalf("got %v", err)
	}
}

func TestAddCleansUpUnconfirmedCreate(t *testing.T) {
	h := newHarness(t)
	h.login()
	// The fake records the host but answers 5xx: CreateHost must not retry,
	// and add must find and delete the host.
	h.fake.FailAfter("POST /api/v1/hosts", 502)
	err := h.run("", "add", "web1")
	if err == nil {
		t.Fatal("no error")
	}
	if hosts := h.fake.Hosts(); len(hosts) != 0 {
		t.Fatalf("left behind: %+v", hosts)
	}
	if !strings.Contains(h.err.String(), "created despite the error; deleted it") {
		t.Fatalf("stderr: %s", h.err.String())
	}
}

func TestAddPush(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "add", "web1", "--push", "ubuntu@203.0.113.7")
	if len(h.sshCalls) != 1 {
		t.Fatalf("ssh calls: %v", h.sshCalls)
	}
	tok := h.fake.ProfileToken(h.host("web1").Connectors[0].ID)
	if strings.Contains(strings.Join(h.sshCalls[0], " "), tok) {
		t.Fatal("token on the ssh command line")
	}
	if h.sshStdin[0] != tok+"\n" {
		t.Fatalf("stdin: %q", h.sshStdin[0])
	}
	if !slices.Contains(h.sshCalls[0], "ubuntu@203.0.113.7") || strings.Contains(h.out.String(), tok) {
		t.Fatalf("args %v, out %s", h.sshCalls[0], h.out.String())
	}

	// A failed push rolls back.
	h.sshResult = func([]string) error { return errors.New("exit status 1") }
	if err := h.run("", "add", "web2", "--push", "ubuntu@203.0.113.8"); err == nil || !strings.Contains(err.Error(), "was deleted") {
		t.Fatalf("got %v", err)
	}
	if n := len(h.fake.Hosts()); n != 1 {
		t.Fatalf("%d hosts", n)
	}
}

func TestLs(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "ls")
	if !strings.Contains(h.out.String(), "No ccx hosts") {
		t.Fatalf("empty: %s", h.out.String())
	}
	h.mustRun("", "add", "web1")
	h.fake.AddHost("legacy", "made in the portal", "legacy.internal", api.StatusOnline)
	h.fake.SetStatus("web1", api.StatusOnline)

	h.mustRun("", "ls")
	if !strings.Contains(h.out.String(), "web1") || strings.Contains(h.out.String(), "legacy") ||
		!strings.Contains(h.out.String(), "ONLINE") || !strings.Contains(h.out.String(), "eu-west-1") {
		t.Fatalf("ls:\n%s", h.out.String())
	}
	h.mustRun("", "ls", "--all")
	if !strings.Contains(h.out.String(), "legacy *") {
		t.Fatalf("ls --all:\n%s", h.out.String())
	}
	h.mustRun("", "ls", "--json")
	if !strings.Contains(h.out.String(), `"name": "web1"`) || strings.Contains(h.out.String(), "legacy") {
		t.Fatalf("ls --json:\n%s", h.out.String())
	}
}

func TestStatus(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "add", "web1")

	err := h.run("", "status", "web1")
	var ec exitCoder
	if !asExitCoder(err, &ec) || ec.ExitCode() != 2 || !silent(err) || !strings.Contains(h.out.String(), "web1: OFFLINE") {
		t.Fatalf("offline: %v\n%s", err, h.out.String())
	}

	sleeps := 0
	h.onSleep = func() {
		if sleeps++; sleeps == 3 {
			h.fake.SetStatus("web1", api.StatusOnline)
		}
	}
	h.mustRun("", "status", "web1", "--wait", "2m")
	if !strings.Contains(h.out.String(), "web1: ONLINE") || sleeps != 3 {
		t.Fatalf("wait: sleeps=%d\n%s", sleeps, h.out.String())
	}

	h.fake.SetStatus("web1", api.StatusOffline)
	h.onSleep = nil
	start := h.clock
	err = h.run("", "status", "web1", "--wait", "12s")
	if err == nil || !strings.Contains(err.Error(), "did not come online") || h.clock.Sub(start) != 12*time.Second {
		t.Fatalf("timeout: %v after %s", err, h.clock.Sub(start))
	}
}

func TestTokenSleepWakeRm(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "add", "web1")
	web := h.host("web1")
	connID := web.Connectors[0].ID
	first := h.fake.ProfileToken(connID)

	h.mustRun("", "token", "web1")
	second := h.fake.ProfileToken(connID)
	if second == first || !strings.Contains(h.out.String(), "sudo bash -s -- "+second) {
		t.Fatalf("token: %s", h.out.String())
	}

	h.mustRun("", "sleep", "web1")
	if !h.fake.Suspended(connID) || !strings.Contains(h.out.String(), "seat is free") {
		t.Fatalf("sleep: %s", h.out.String())
	}
	h.mustRun("", "ls")
	if !strings.Contains(h.out.String(), "SUSPENDED") {
		t.Fatalf("ls while suspended: %s", h.out.String())
	}
	if err := h.run("", "status", "web1", "--wait", "2m"); err == nil || !strings.Contains(err.Error(), "ccx wake web1") || !strings.Contains(h.out.String(), "web1: SUSPENDED") {
		t.Fatalf("status while suspended: %v\n%s", err, h.out.String())
	}
	h.mustRun("", "wake", "web1")
	if h.fake.Suspended(connID) {
		t.Fatal("still suspended")
	}

	if err := h.run("n\n", "rm", "web1"); err == nil || err.Error() != "aborted" {
		t.Fatalf("rm n: %v", err)
	}
	if err := h.run("", "rm", "web1"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("rm with no input: %v", err)
	}
	if len(h.fake.Hosts()) != 1 {
		t.Fatal("deleted without confirmation")
	}
	h.mustRun("y\n", "rm", "web1")
	if len(h.fake.Hosts()) != 0 {
		t.Fatal("not deleted")
	}
	if err := h.run("", "rm", "web1", "--yes"); err == nil || !strings.Contains(err.Error(), "no host named") {
		t.Fatalf("rm missing: %v", err)
	}
}

func TestNonCCXHostsNeedForce(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.AddHost("legacy", "made in the portal", "legacy.internal", api.StatusOnline)
	for _, args := range [][]string{{"token", "legacy"}, {"sleep", "legacy"}, {"wake", "legacy"}, {"rm", "legacy", "--yes"}} {
		err := h.run("", args...)
		if err == nil || !strings.Contains(err.Error(), "not created by ccx") {
			t.Errorf("%v: %v", args, err)
		}
	}
	h.mustRun("", "rm", "legacy", "--yes", "--force")
	if len(h.fake.Hosts()) != 0 {
		t.Fatal("not deleted with --force")
	}
}

func TestSSH(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "add", "web1")

	h.mustRun("", "ssh", "ubuntu@web1", "--", "-p", "2222", "uptime")
	if got := h.sshCalls[0]; !slices.Equal(got, []string{"ubuntu@web1.internal", "-p", "2222", "uptime"}) {
		t.Fatalf("args: %v", got)
	}

	h.resolves = false
	h.mustRun("", "ssh", "web1")
	ip := h.host("web1").Connectors[0].IPv4Address
	if got := h.sshCalls[1]; !slices.Equal(got, []string{ip}) || !strings.Contains(h.err.String(), "OpenVPN Connect") {
		t.Fatalf("fallback: %v, %s", got, h.err.String())
	}

	if err := h.run("", "ssh", "web1", "uptime"); err == nil || !strings.Contains(err.Error(), "after --") {
		t.Fatalf("args without --: %v", err)
	}
	if err := h.run("", "ssh", "-oProxyCommand=x@web1"); err == nil {
		t.Fatal("option-like target accepted")
	}
}

func TestLogout(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("", "logout")
	if h.keys.Len() != 0 {
		t.Fatal("keychain item left")
	}
	if _, err := config.Load(); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("config left: %v", err)
	}
	h.mustRun("", "logout")
	if !strings.Contains(h.out.String(), "Not logged in") {
		t.Fatalf("second logout: %s", h.out.String())
	}
	if err := h.run("", "ls"); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("ls after logout: %v", err)
	}
}

func TestDoctor(t *testing.T) {
	h := newHarness(t)
	if err := h.run("", "doctor"); err == nil || !strings.Contains(h.out.String(), "FAIL  config") {
		t.Fatalf("not logged in: %v\n%s", err, h.out.String())
	}
	h.login()
	h.mustRun("", "doctor")
	if !strings.Contains(h.out.String(), "All checks passed") {
		t.Fatalf("doctor:\n%s", h.out.String())
	}

	h.clock = h.clock.AddDate(0, 11, 20)
	h.mustRun("", "doctor")
	if !strings.Contains(h.out.String(), "warn  credential expires") {
		t.Fatalf("near expiry:\n%s", h.out.String())
	}
	h.clock = h.clock.AddDate(0, 1, 0)
	if err := h.run("", "doctor"); err == nil || !strings.Contains(h.out.String(), "FAIL  credential probably expired") {
		t.Fatalf("expired: %v\n%s", err, h.out.String())
	}
}
