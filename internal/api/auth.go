package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	tokenPath = "/api/v1/oauth/token" //nolint:gosec // G101: a URL path, not a credential
	// refreshMargin is how long before expiry a cached token is replaced.
	refreshMargin = 5 * time.Minute
	// defaultTokenTTL is assumed when the server omits expires_in.
	defaultTokenTTL = time.Hour
)

// authMode records how the token endpoint accepted credentials.
type authMode int

const (
	authUnknown authMode = iota
	authForm             // application/x-www-form-urlencoded body (preferred)
	authQuery            // query parameters, as documented in v1.json
)

func (m authMode) String() string {
	switch m {
	case authForm:
		return "form body"
	case authQuery:
		return "query parameters"
	}
	return "unknown"
}

// oauthResponse is v1.json's OAuthResponse. expires_in is declared as a
// string but documented with a numeric example, so both are accepted.
type oauthResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   flexInt64 `json:"expires_in"`
	Scope       string    `json:"scope"`
}

type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("expires_in: not an integer")
	}
	*f = flexInt64(n)
	return nil
}

// Authenticate fetches a fresh access token, validating the credentials.
func (c *Client) Authenticate(ctx context.Context) error {
	c.invalidateToken()
	_, err := c.accessToken(ctx)
	return err
}

// AuthMode reports how the token endpoint accepted credentials (for diagnostics).
func (c *Client) AuthMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authMode.String()
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	c.token = ""
	c.expiresAt = time.Time{}
	c.mu.Unlock()
}

// accessToken returns a cached token or fetches a new one.
func (c *Client) accessToken(ctx context.Context) (Secret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expiresAt.Add(-refreshMargin)) {
		return c.token, nil
	}
	modes := []authMode{authForm, authQuery}
	if c.authMode != authUnknown {
		modes = []authMode{c.authMode}
	}
	var lastErr error
	for i, mode := range modes {
		tok, ttl, err := c.fetchToken(ctx, mode)
		if err == nil {
			if c.authMode != mode {
				c.log.Debug("oauth token accepted", "mode", mode.String())
			}
			c.authMode = mode
			c.token = tok
			c.expiresAt = c.now().Add(ttl)
			return tok, nil
		}
		lastErr = err
		var apiErr *APIError
		if i+1 < len(modes) && errors.As(err, &apiErr) && formRejected(apiErr.Status) {
			c.log.Debug("oauth token: form body rejected, trying query parameters", "status", apiErr.Status)
			continue
		}
		break
	}
	return "", lastErr
}

// formRejected reports statuses that may mean "credentials not read from the body".
func formRejected(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType:
		return true
	}
	return false
}

func (c *Client) fetchToken(ctx context.Context, mode authMode) (Secret, time.Duration, error) {
	creds := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {string(c.secret)},
		"grant_type":    {"client_credentials"},
	}
	body, _, err := c.withRetry(ctx, http.MethodPost, tokenPath, true, func() (*http.Request, error) {
		u := c.base.JoinPath(tokenPath)
		var req *http.Request
		var err error
		if mode == authForm {
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(creds.Encode()))
			if err == nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		} else {
			u.RawQuery = creds.Encode()
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
		}
		if err != nil {
			return nil, errors.New("build token request")
		}
		req.Header.Set("Accept", "application/json")
		return req, nil
	}, nil)
	if err != nil {
		return "", 0, err
	}
	var or oauthResponse
	if err := json.Unmarshal(body, &or); err != nil {
		return "", 0, errors.New("decode OAuth token response: unexpected format")
	}
	if or.AccessToken == "" {
		return "", 0, errors.New("OAuth token response has no access_token")
	}
	if tt := strings.ToLower(or.TokenType); tt != "" && tt != "bearer" {
		return "", 0, fmt.Errorf("unsupported OAuth token type %q", or.TokenType)
	}
	ttl := time.Duration(or.ExpiresIn) * time.Second
	switch {
	case ttl <= 0:
		ttl = defaultTokenTTL
	case ttl <= 2*refreshMargin:
		// Short-lived token: keep it for half its life instead of refreshing every call.
		ttl = refreshMargin + ttl/2
	}
	return Secret(or.AccessToken), ttl, nil
}
