// Package api is a small client for the CloudConnexa Public API v1.
//
// It covers only the endpoints ccx needs (see docs/API_NOTES.md). It never
// logs or formats the client secret or access token, and error messages
// never include request URLs (which may carry credentials in the query).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxBodyBytes caps how much of any response body is read.
const maxBodyBytes = 16 << 20

var cloudIDRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Secret holds a credential and refuses to print itself.
type Secret string

// String implements fmt.Stringer without revealing the value.
func (Secret) String() string { return "[REDACTED]" }

// GoString implements fmt.GoStringer without revealing the value.
func (Secret) GoString() string { return "[REDACTED]" }

// Config configures a Client.
type Config struct {
	// CloudID selects https://<cloud-id>.api.openvpn.com. Ignored if BaseURL is set.
	CloudID string
	// BaseURL overrides the API origin (tests only).
	BaseURL      string
	ClientID     string
	ClientSecret Secret
	// HTTPClient defaults to a client with a 30 s timeout.
	HTTPClient *http.Client
	// Logger receives debug logs (never credentials). Defaults to discarding.
	Logger *slog.Logger
	// MaxAttempts per request including the first; defaults to 5.
	MaxAttempts int
}

// Client talks to the CloudConnexa API. It is safe for concurrent use.
type Client struct {
	base        *url.URL
	clientID    string
	secret      Secret
	hc          *http.Client
	log         *slog.Logger
	maxAttempts int

	// Test hooks.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu        sync.Mutex
	token     Secret
	expiresAt time.Time
	authMode  authMode
}

// String implements fmt.Stringer so printing a Client never reveals credentials
// (fmt prints unexported fields via reflection, bypassing Secret.String).
func (c *Client) String() string { return "api.Client{" + c.base.Host + "}" }

// GoString implements fmt.GoStringer for the same reason.
func (c *Client) GoString() string { return c.String() }

// BaseURLForCloudID returns the API origin for a Cloud ID.
func BaseURLForCloudID(cloudID string) (string, error) {
	if !cloudIDRe.MatchString(cloudID) {
		return "", fmt.Errorf("invalid Cloud ID %q: expected lowercase letters, digits and hyphens", cloudID)
	}
	return "https://" + cloudID + ".api.openvpn.com", nil
}

// New returns a Client for cfg.
func New(cfg Config) (*Client, error) {
	raw := cfg.BaseURL
	if raw == "" {
		var err error
		if raw, err = BaseURLForCloudID(cfg.CloudID); err != nil {
			return nil, err
		}
	}
	base, err := url.Parse(raw)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid API base URL %q", raw)
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("client ID and client secret are required")
	}
	c := &Client{
		base:        base,
		clientID:    cfg.ClientID,
		secret:      cfg.ClientSecret,
		hc:          cfg.HTTPClient,
		log:         cfg.Logger,
		maxAttempts: cfg.MaxAttempts,
		now:         time.Now,
		sleep:       sleepCtx,
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: 30 * time.Second}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.maxAttempts <= 0 {
		c.maxAttempts = 5
	}
	return c, nil
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

// request describes one API call.
type request struct {
	method string
	path   string
	query  url.Values
	body   any // JSON-encoded if non-nil
}

// do sends an authenticated request with retries and returns the response body.
func (c *Client) do(ctx context.Context, r request) ([]byte, int, error) {
	var payload []byte
	if r.body != nil {
		var err error
		if payload, err = json.Marshal(r.body); err != nil {
			return nil, 0, fmt.Errorf("encode %s %s: %w", r.method, r.path, err)
		}
	}
	refreshed := false
	idempotent := r.method != http.MethodPost
	return c.withRetry(ctx, r.method, r.path, idempotent, func() (*http.Request, error) {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		u := c.base.JoinPath(r.path)
		u.RawQuery = r.query.Encode()
		req, err := http.NewRequestWithContext(ctx, r.method, u.String(), bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build %s %s: %w", r.method, r.path, err)
		}
		req.Header.Set("Authorization", "Bearer "+string(tok))
		req.Header.Set("Accept", "application/json, text/plain, */*")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}, func(status int) bool {
		// A cached token can be revoked or expire early: refresh it once.
		if status == http.StatusUnauthorized && !refreshed {
			refreshed = true
			c.invalidateToken()
			return true
		}
		return false
	})
}

// withRetry runs build+send until success, a non-retryable error, or maxAttempts.
// Non-idempotent requests are retried only on 429, which means "not processed".
// extraRetry may request one more attempt for a given status (used for 401 refresh).
func (c *Client) withRetry(ctx context.Context, method, path string, idempotent bool, build func() (*http.Request, error), extraRetry func(int) bool) ([]byte, int, error) {
	var lastErr error
	for attempt := 1; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, 0, err
		}
		body, status, retryAfter, err := c.send(req, method, path)
		c.log.Debug("api request", "method", method, "path", path, "status", status, "attempt", attempt)
		if err == nil && status >= 200 && status < 300 {
			return body, status, nil
		}
		if err != nil {
			lastErr = err
			// Network errors on POST may have reached the server; never repeat a create.
			if !idempotent || ctx.Err() != nil {
				return nil, 0, err
			}
		} else {
			apiErr := newAPIError(method, path, status, body, retryAfter)
			if path == tokenPath {
				apiErr.Message = "" // never surface token-endpoint bodies
			}
			lastErr = apiErr
			if extraRetry != nil && extraRetry(status) {
				continue
			}
			// 5xx on POST may have created the resource, so only 429 is retried there.
			retryable := status == http.StatusTooManyRequests || (status >= 500 && idempotent)
			if !retryable {
				return nil, status, apiErr
			}
		}
		if attempt >= c.maxAttempts {
			return nil, 0, lastErr
		}
		wait := backoff(attempt, retryAfter)
		c.log.Debug("api retry", "method", method, "path", path, "wait", wait.String())
		if err := c.sleep(ctx, wait); err != nil {
			return nil, 0, err
		}
	}
}

// send performs one HTTP exchange. Errors never include the request URL.
func (c *Client) send(req *http.Request, method, path string) ([]byte, int, time.Duration, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // drop the URL: it may contain credentials
		}
		return nil, 0, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	return body, resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After"), c.now()), nil
}

// decodeJSON unmarshals body into v with context for errors.
func decodeJSON(what string, body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", what, err)
	}
	return nil
}

// decodeString accepts a bare string body as raw text or a JSON string.
func decodeString(body []byte) (string, error) {
	s := strings.TrimSpace(string(body))
	if strings.HasPrefix(s, `"`) {
		var out string
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return "", err
		}
		s = strings.TrimSpace(out)
	}
	return s, nil
}
