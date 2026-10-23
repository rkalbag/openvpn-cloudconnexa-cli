// Package config reads and writes ccx's non-secret settings
// (config.toml). The client secret never goes here; see internal/keychain.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// DirEnv overrides the config directory (tests and unusual setups).
const DirEnv = "CCX_CONFIG_DIR"

// DefaultDomainSuffix is used when the user does not choose one.
const DefaultDomainSuffix = "internal"

// ErrNotFound means ccx has not been logged in on this machine.
var ErrNotFound = errors.New("not logged in: run `ccx login`")

// Config is the content of config.toml. It must never hold the client secret.
type Config struct {
	CloudID             string    `toml:"cloud_id"`
	ClientID            string    `toml:"client_id"`
	RegionID            string    `toml:"region_id"`
	DomainSuffix        string    `toml:"domain_suffix"`
	CredentialCreatedAt time.Time `toml:"credential_created_at"`
	CredentialLifetime  Lifetime  `toml:"credential_lifetime"`
}

var (
	cloudIDRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	clientIDRe = regexp.MustCompile(`^[A-Za-z0-9.-]+$`) // v1.json pattern
	labelRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ValidateDomainSuffix checks a DNS suffix such as "internal" or "corp.example".
func ValidateDomainSuffix(s string) error {
	if s == "" || len(s) > 200 {
		return fmt.Errorf("invalid domain suffix %q", s)
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' {
			if !labelRe.MatchString(s[start:i]) {
				return fmt.Errorf("invalid domain suffix %q: labels must be lowercase letters, digits and hyphens", s)
			}
			start = i + 1
		}
	}
	return nil
}

// Validate checks that the config is complete and well-formed.
func (c *Config) Validate() error {
	switch {
	case !cloudIDRe.MatchString(c.CloudID):
		return fmt.Errorf("config: invalid cloud_id %q", c.CloudID)
	case !clientIDRe.MatchString(c.ClientID):
		return fmt.Errorf("config: invalid client_id %q", c.ClientID)
	case c.RegionID == "":
		return errors.New("config: region_id is empty")
	}
	if err := ValidateDomainSuffix(c.DomainSuffix); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if c.CredentialLifetime != "" {
		if _, err := ParseLifetime(string(c.CredentialLifetime)); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	return nil
}

// Dir returns the ccx config directory:
// $XDG_CONFIG_HOME/ccx (or ~/.config/ccx) on Linux,
// ~/Library/Application Support/ccx on macOS, %AppData%\ccx on Windows.
func Dir() (string, error) {
	if d := os.Getenv(DirEnv); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(base, "ccx"), nil
}

// Path returns the config file path.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// Load reads and validates the config file.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: path is ccx's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := toml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if c.DomainSuffix == "" {
		c.DomainSuffix = DefaultDomainSuffix
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%w (fix it or re-run `ccx login`)", err)
	}
	return &c, nil
}

// Save validates and atomically writes the config with mode 0600.
func Save(c *Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	b, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// CreateTemp creates the file with mode 0600, which Rename preserves.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Remove deletes the config file. A missing file is not an error.
func Remove() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove config: %w", err)
	}
	return nil
}
