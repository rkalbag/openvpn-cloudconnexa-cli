package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testConfig() *Config {
	return &Config{
		CloudID:             "acme",
		ClientID:            "ccx.client-1",
		RegionID:            "us-east-1",
		DomainSuffix:        "internal",
		CredentialCreatedAt: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
		CredentialLifetime:  Lifetime1Year,
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "ccx")
	t.Setenv(DirEnv, dir)

	if _, err := Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load before save: %v", err)
	}
	want := testConfig()
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if err := Remove(); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestConfigFileFields(t *testing.T) {
	t.Setenv(DirEnv, t.TempDir())
	if err := Save(testConfig()); err != nil {
		t.Fatal(err)
	}
	p, _ := Path()
	b, _ := os.ReadFile(p)
	for _, key := range []string{"cloud_id", "client_id", "region_id", "domain_suffix", "credential_created_at", "credential_lifetime"} {
		if !strings.Contains(string(b), key+" = ") {
			t.Errorf("config missing %s:\n%s", key, b)
		}
	}
	if strings.Contains(strings.ToLower(string(b)), "secret") {
		t.Fatalf("config mentions a secret:\n%s", b)
	}
}

func TestLoadDefaultsSuffixAndRejectsBadValues(t *testing.T) {
	t.Setenv(DirEnv, t.TempDir())
	p, _ := Path()
	write := func(s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cloud_id = 'acme'\nclient_id = 'c1'\nregion_id = 'us-east-1'\n")
	c, err := Load()
	if err != nil || c.DomainSuffix != DefaultDomainSuffix {
		t.Fatalf("got %+v, %v", c, err)
	}
	write("cloud_id = 'acme.evil.com'\nclient_id = 'c1'\nregion_id = 'r'\n")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid cloud_id error")
	}
	write("not toml [[[")
	if _, err := Load(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestValidateDomainSuffix(t *testing.T) {
	for _, ok := range []string{"internal", "cloud", "corp.example", "a-b.c1"} {
		if err := ValidateDomainSuffix(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".internal", "internal.", "Internal", "a..b", "-x", "x_y", "a b"} {
		if err := ValidateDomainSuffix(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestLifetimes(t *testing.T) {
	for in, want := range map[string]Lifetime{"2w": Lifetime2Weeks, "1 month": Lifetime1Month, "6M": Lifetime6Months, "1 year": Lifetime1Year} {
		got, err := ParseLifetime(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v", in, got, err)
		}
	}
	if _, err := ParseLifetime("3d"); err == nil {
		t.Error("expected error for 3d")
	}
}

func TestCredentialAge(t *testing.T) {
	c := testConfig()
	created := c.CredentialCreatedAt

	age := c.CredentialAge(created.AddDate(0, 6, 0))
	if !age.Known || age.Warn || !age.ExpiresAt.Equal(created.AddDate(1, 0, 0)) {
		t.Fatalf("mid-life: %+v", age)
	}
	age = c.CredentialAge(created.AddDate(1, 0, -14))
	if !age.Warn {
		t.Fatalf("14 days left should warn: %+v", age)
	}
	age = c.CredentialAge(created.AddDate(1, 0, -15))
	if age.Warn {
		t.Fatalf("15 days left should not warn: %+v", age)
	}
	age = c.CredentialAge(created.AddDate(2, 0, 0))
	if !age.Warn || age.Left >= 0 {
		t.Fatalf("expired: %+v", age)
	}
	c.CredentialLifetime = ""
	if c.CredentialAge(created).Known {
		t.Fatal("unknown lifetime should not be Known")
	}
}
