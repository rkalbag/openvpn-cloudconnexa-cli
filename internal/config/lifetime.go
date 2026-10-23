package config

import (
	"fmt"
	"strings"
	"time"
)

// Lifetime is an API credential lifetime as offered by the Admin Portal.
type Lifetime string

// Portal lifetimes.
const (
	Lifetime2Weeks  Lifetime = "2w"
	Lifetime1Month  Lifetime = "1m"
	Lifetime6Months Lifetime = "6m"
	Lifetime1Year   Lifetime = "1y"
)

// Lifetimes lists the valid lifetimes in portal order.
var Lifetimes = []Lifetime{Lifetime2Weeks, Lifetime1Month, Lifetime6Months, Lifetime1Year}

// ExpiryWarning is how close to expiry `ccx doctor` starts warning.
const ExpiryWarning = 14 * 24 * time.Hour

// ParseLifetime accepts "2w", "1m", "6m", "1y" and spelled-out forms.
func ParseLifetime(s string) (Lifetime, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "")) {
	case "2w", "2weeks":
		return Lifetime2Weeks, nil
	case "1m", "1month":
		return Lifetime1Month, nil
	case "6m", "6months":
		return Lifetime6Months, nil
	case "1y", "1year", "12m", "12months":
		return Lifetime1Year, nil
	}
	return "", fmt.Errorf("invalid credential lifetime %q: use 2w, 1m, 6m or 1y", s)
}

// Label is a human-readable form.
func (l Lifetime) Label() string {
	switch l {
	case Lifetime2Weeks:
		return "2 weeks"
	case Lifetime1Month:
		return "1 month"
	case Lifetime6Months:
		return "6 months"
	case Lifetime1Year:
		return "1 year"
	}
	return string(l)
}

// ExpiresAt returns when a credential created at t expires. The API does not
// expose the real expiry, so this is an estimate from the login time.
func (l Lifetime) ExpiresAt(created time.Time) (time.Time, bool) {
	switch l {
	case Lifetime2Weeks:
		return created.Add(14 * 24 * time.Hour), true
	case Lifetime1Month:
		return created.AddDate(0, 1, 0), true
	case Lifetime6Months:
		return created.AddDate(0, 6, 0), true
	case Lifetime1Year:
		return created.AddDate(1, 0, 0), true
	}
	return time.Time{}, false
}

// CredentialAge describes the estimated state of the API credential.
type CredentialAge struct {
	Known     bool          // false if lifetime or creation time is missing
	ExpiresAt time.Time     // estimated
	Left      time.Duration // negative once expired
	Warn      bool          // within ExpiryWarning of expiry (or expired)
}

// CredentialAge estimates the credential's remaining life at now.
func (c *Config) CredentialAge(now time.Time) CredentialAge {
	if c.CredentialCreatedAt.IsZero() {
		return CredentialAge{}
	}
	exp, ok := c.CredentialLifetime.ExpiresAt(c.CredentialCreatedAt)
	if !ok {
		return CredentialAge{}
	}
	left := exp.Sub(now)
	return CredentialAge{Known: true, ExpiresAt: exp, Left: left, Warn: left <= ExpiryWarning}
}
