package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

const (
	pageSize = 1000
	// maxPages bounds pagination against a misbehaving server.
	maxPages = 1000
)

// profileTokenRe is a conservative allowlist: the token is pasted into a
// shell command line, so anything outside it is rejected (VERIFY live).
var profileTokenRe = regexp.MustCompile(`^[A-Za-z0-9._~+/=:-]+$`)

// idRe matches resource IDs (UUIDs in practice); IDs come from API data and
// are validated before being placed in a path.
var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func idPath(format, id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("invalid resource ID %q", sanitize(id))
	}
	return fmt.Sprintf(format, id), nil
}

// listAll reads every page of a paginated endpoint.
func listAll[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var all []T
	for p := 0; p < maxPages; p++ {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("page", fmt.Sprint(p))
		q.Set("size", fmt.Sprint(pageSize))
		body, _, err := c.do(ctx, request{method: http.MethodGet, path: path, query: q})
		if err != nil {
			return nil, err
		}
		var pg page[T]
		if err := decodeJSON(path, body, &pg); err != nil {
			return nil, err
		}
		all = append(all, pg.Content...)
		if len(pg.Content) == 0 || p+1 >= pg.TotalPages {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GET %s: more than %d pages", path, maxPages)
}

// Regions lists VPN regions.
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	body, _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v1/regions"})
	if err != nil {
		return nil, err
	}
	var out []Region
	if err := decodeJSON("regions", body, &out); err != nil {
		return nil, err
	}
	valid := out[:0]
	for _, r := range out {
		if r.ID != "" {
			valid = append(valid, r)
		}
	}
	return valid, nil
}

// Topology returns the WPC topology (FULL_MESH or CUSTOM).
func (c *Client) Topology(ctx context.Context) (string, error) {
	body, _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v1/settings/wpc/topology"})
	if err != nil {
		return "", err
	}
	s, err := decodeString(body)
	if err != nil || s == "" {
		return "", errors.New("decode topology: unexpected format")
	}
	return sanitize(s), nil
}

// CreateHost creates a host with its connectors.
func (c *Client) CreateHost(ctx context.Context, req HostCreateRequest) (*Host, error) {
	body, _, err := c.do(ctx, request{method: http.MethodPost, path: "/api/v1/hosts", body: req})
	if err != nil {
		return nil, err
	}
	var h Host
	if err := decodeJSON("created host", body, &h); err != nil {
		return nil, err
	}
	if h.ID == "" {
		return nil, errors.New("create host: response has no host id")
	}
	return &h, nil
}

// ListHosts returns all hosts.
func (c *Client) ListHosts(ctx context.Context) ([]Host, error) {
	return listAll[Host](ctx, c, "/api/v1/hosts", nil)
}

// GetHost returns one host.
func (c *Client) GetHost(ctx context.Context, id string) (*Host, error) {
	p, err := idPath("/api/v1/hosts/%s", id)
	if err != nil {
		return nil, err
	}
	body, _, err := c.do(ctx, request{method: http.MethodGet, path: p})
	if err != nil {
		return nil, err
	}
	var h Host
	if err := decodeJSON("host", body, &h); err != nil {
		return nil, err
	}
	if h.ID == "" {
		return nil, errors.New("get host: response has no host id")
	}
	return &h, nil
}

// DeleteHost deletes a host, its connectors and IP services.
func (c *Client) DeleteHost(ctx context.Context, id string) error {
	p, err := idPath("/api/v1/hosts/%s", id)
	if err != nil {
		return err
	}
	_, _, err = c.do(ctx, request{method: http.MethodDelete, path: p})
	return err
}

// ListConnectors returns all host connectors, optionally for one host.
func (c *Client) ListConnectors(ctx context.Context, hostID string) ([]Connector, error) {
	var q url.Values
	if hostID != "" {
		if !idRe.MatchString(hostID) {
			return nil, fmt.Errorf("list connectors: invalid host ID %q", sanitize(hostID))
		}
		q = url.Values{"hostId": {hostID}}
	}
	return listAll[Connector](ctx, c, "/api/v1/hosts/connectors", q)
}

// GetConnector returns one host connector.
func (c *Client) GetConnector(ctx context.Context, id string) (*Connector, error) {
	p, err := idPath("/api/v1/hosts/connectors/%s", id)
	if err != nil {
		return nil, err
	}
	body, _, err := c.do(ctx, request{method: http.MethodGet, path: p})
	if err != nil {
		return nil, err
	}
	var conn Connector
	if err := decodeJSON("connector", body, &conn); err != nil {
		return nil, err
	}
	if conn.ID == "" {
		return nil, errors.New("get connector: response has no connector id")
	}
	return &conn, nil
}

// ProfileToken generates a one-time connector profile token, invalidating
// any previous one. The returned value must only be shown to the user.
func (c *Client) ProfileToken(ctx context.Context, connectorID string) (Secret, error) {
	p, err := idPath("/api/v1/hosts/connectors/%s/profile/encrypt", connectorID)
	if err != nil {
		return "", err
	}
	body, _, err := c.do(ctx, request{method: http.MethodPost, path: p})
	if err != nil {
		return "", err
	}
	tok, err := decodeString(body)
	if err != nil || len(tok) < 16 || len(tok) > 4096 || !profileTokenRe.MatchString(tok) {
		return "", errors.New("profile token: unexpected format from server")
	}
	return Secret(tok), nil
}

// SuspendConnector suspends a connector, freeing its seat.
func (c *Client) SuspendConnector(ctx context.Context, id string) error {
	return c.putConnectorAction(ctx, id, "suspend")
}

// ActivateConnector re-activates a suspended connector.
func (c *Client) ActivateConnector(ctx context.Context, id string) error {
	return c.putConnectorAction(ctx, id, "activate")
}

func (c *Client) putConnectorAction(ctx context.Context, id, action string) error {
	p, err := idPath("/api/v1/hosts/connectors/%s/"+action, id)
	if err != nil {
		return err
	}
	_, _, err = c.do(ctx, request{method: http.MethodPut, path: p})
	return err
}

// CreateIPService adds an IP service to a host.
func (c *Client) CreateIPService(ctx context.Context, hostID string, req IPServiceRequest) (*IPService, error) {
	if !idRe.MatchString(hostID) {
		return nil, fmt.Errorf("create IP service: invalid host ID %q", sanitize(hostID))
	}
	body, _, err := c.do(ctx, request{
		method: http.MethodPost,
		path:   "/api/v1/hosts/ip-services",
		query:  url.Values{"hostId": {hostID}},
		body:   req,
	})
	if err != nil {
		return nil, err
	}
	var svc IPService
	if err := decodeJSON("IP service", body, &svc); err != nil {
		return nil, err
	}
	if svc.ID == "" {
		return nil, errors.New("create IP service: response has no id")
	}
	return &svc, nil
}
