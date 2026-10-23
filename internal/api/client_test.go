package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func TestBaseURLForCloudID(t *testing.T) {
	got, err := BaseURLForCloudID("acme-corp")
	if err != nil || got != "https://acme-corp.api.openvpn.com" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "Acme", "acme.evil.com", "acme/x", "-acme", "acme-"} {
		if _, err := BaseURLForCloudID(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestTokenFormBodyAndCache(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /api/v1/regions", writeJSON(200, fixture(t, "regions.json")))
	c, _ := newTestClient(t, f)

	for i := 0; i < 3; i++ {
		if _, err := c.Regions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1 (cached)", f.tokenCalls)
	}
	if c.AuthMode() != "form body" {
		t.Fatalf("auth mode = %s", c.AuthMode())
	}
	for _, r := range f.requests {
		if strings.Contains(r.Query, testSecret) {
			t.Fatalf("secret sent in query string in form mode: %s %s", r.Method, r.Path)
		}
	}
}

func TestTokenFallsBackToQueryParams(t *testing.T) {
	f := newFakeAPI(t)
	f.acceptForm = false
	f.handle("GET /api/v1/regions", writeJSON(200, fixture(t, "regions.json")))
	c, clock := newTestClient(t, f)

	if _, err := c.Regions(ctx); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 2 || c.AuthMode() != "query parameters" {
		t.Fatalf("token calls = %d, mode = %s", f.tokenCalls, c.AuthMode())
	}
	// After expiry the remembered mode is used directly.
	clock.Advance(12 * time.Hour)
	if _, err := c.Regions(ctx); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 3 {
		t.Fatalf("token calls = %d, want 3", f.tokenCalls)
	}
}

func TestTokenRefreshBeforeExpiry(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /api/v1/regions", writeJSON(200, fixture(t, "regions.json")))
	c, clock := newTestClient(t, f)
	if _, err := c.Regions(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Advance(43199*time.Second - refreshMargin - time.Second)
	if _, err := c.Regions(ctx); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 1 {
		t.Fatalf("refreshed too early: %d calls", f.tokenCalls)
	}
	clock.Advance(2 * time.Second)
	if _, err := c.Regions(ctx); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 2 {
		t.Fatalf("did not refresh within margin: %d calls", f.tokenCalls)
	}
}

func TestExpiresInStringOrNumber(t *testing.T) {
	for _, raw := range []string{`43199`, `"43199"`, `null`} {
		t.Run(raw, func(t *testing.T) {
			f := newFakeAPI(t)
			f.expiresIn = raw
			c, _ := newTestClient(t, f)
			if err := c.Authenticate(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTokenErrors(t *testing.T) {
	cases := []struct {
		status int
		want   error
		hint   string
	}{
		{401, ErrUnauthorized, "ccx login"},
		{403, ErrForbidden, "Essential"},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			f := newFakeAPI(t)
			f.tokenStatus = tc.status
			c, _ := newTestClient(t, f)
			err := c.Authenticate(ctx)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.hint) {
				t.Fatalf("missing hint %q in %q", tc.hint, err)
			}
		})
	}
}

func TestExpiredTokenRefreshedOnce(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /api/v1/regions", writeJSON(200, fixture(t, "regions.json")))
	c, _ := newTestClient(t, f)
	if err := c.Authenticate(ctx); err != nil {
		t.Fatal(err)
	}
	// Server-side revocation: the cached token stops working.
	f.mu.Lock()
	f.validToken = "revoked-elsewhere"
	f.mu.Unlock()
	f.handle("GET /api/v1/regions", writeJSON(200, fixture(t, "regions.json")))
	// The fake issues a new valid token on the next token call.
	if _, err := c.Regions(ctx); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 2 {
		t.Fatalf("token calls = %d, want 2", f.tokenCalls)
	}
}

func TestRetry429ThenSuccess(t *testing.T) {
	f := newFakeAPI(t)
	calls := 0
	f.handle("GET /api/v1/regions", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "retry after 1 minute")
			return
		}
		writeJSON(200, fixture(t, "regions.json"))(w, r)
	})
	c, clock := newTestClient(t, f)
	regions, err := c.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 {
		t.Fatalf("regions = %d, want 2 (entry without id dropped)", len(regions))
	}
	if calls != 3 || len(clock.sleeps) != 2 || clock.sleeps[0] != 7*time.Second {
		t.Fatalf("calls = %d, sleeps = %v", calls, clock.sleeps)
	}
}

func TestRetry429GivesUpAfterMaxAttempts(t *testing.T) {
	f := newFakeAPI(t)
	calls := 0
	f.handle("GET /api/v1/regions", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c, clock := newTestClient(t, f)
	_, err := c.Regions(ctx)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if calls != 5 || len(clock.sleeps) != 4 {
		t.Fatalf("calls = %d, sleeps = %d", calls, len(clock.sleeps))
	}
	for i, d := range clock.sleeps {
		ceiling := min(backoffBase<<i, backoffMax)
		if d < ceiling/2 || d > ceiling {
			t.Errorf("sleep %d = %v, want within [%v, %v]", i, d, ceiling/2, ceiling)
		}
	}
}

func TestRetry5xxOnlyWhenIdempotent(t *testing.T) {
	f := newFakeAPI(t)
	getCalls, postCalls := 0, 0
	f.handle("GET /api/v1/hosts/11111111-1111-1111-1111-111111111111", func(w http.ResponseWriter, r *http.Request) {
		getCalls++
		if getCalls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(200, fixture(t, "host.json"))(w, r)
	})
	f.handle("POST /api/v1/hosts", func(w http.ResponseWriter, _ *http.Request) {
		postCalls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	c, _ := newTestClient(t, f)
	if _, err := c.GetHost(ctx, "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatal(err)
	}
	if getCalls != 2 {
		t.Fatalf("GET calls = %d, want 2", getCalls)
	}
	_, err := c.CreateHost(ctx, HostCreateRequest{Name: "web1"})
	if !errors.Is(err, ErrServer) || postCalls != 1 {
		t.Fatalf("POST: err = %v, calls = %d (5xx on create must not be retried)", err, postCalls)
	}
}

func TestPaginationReadsAllPages(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /api/v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if r.URL.Query().Get("size") != "1000" {
			t.Errorf("size = %q", r.URL.Query().Get("size"))
		}
		body, _ := json.Marshal(page[Host]{
			Content:          []Host{{ID: fmt.Sprintf("h%d", p), Name: fmt.Sprintf("host%d", p)}},
			TotalElements:    3,
			TotalPages:       3,
			NumberOfElements: 1,
			Page:             p,
			Size:             1000,
		})
		writeJSON(200, body)(w, r)
	})
	c, _ := newTestClient(t, f)
	hosts, err := c.ListHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 || hosts[2].ID != "h2" {
		t.Fatalf("hosts = %+v", hosts)
	}
}

func TestEndpoints(t *testing.T) {
	const hostID = "11111111-1111-1111-1111-111111111111"
	const connID = "22222222-2222-2222-2222-222222222222"
	f := newFakeAPI(t)
	f.handle("GET /api/v1/settings/wpc/topology", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "FULL_MESH")
	})
	f.handle("POST /api/v1/hosts", writeJSON(201, fixture(t, "host.json")))
	f.handle("DELETE /api/v1/hosts/"+hostID, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	connectorPage, _ := json.Marshal(page[json.RawMessage]{Content: []json.RawMessage{mustConnectorJSON(t)}, TotalPages: 1})
	f.handle("GET /api/v1/hosts/connectors", writeJSON(200, connectorPage))
	f.handle("GET /api/v1/hosts/connectors/"+connID, writeJSON(200, mustConnectorJSON(t)))
	f.handle("POST /api/v1/hosts/connectors/"+connID+"/profile/encrypt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `"AbCdEf0123456789+/=:0123456789012345678901234567890123456789"`)
	})
	f.handle("PUT /api/v1/hosts/connectors/"+connID+"/suspend", func(_ http.ResponseWriter, _ *http.Request) {})
	f.handle("PUT /api/v1/hosts/connectors/"+connID+"/activate", func(_ http.ResponseWriter, _ *http.Request) {})
	f.handle("POST /api/v1/hosts/ip-services", writeJSON(201, fixture(t, "ip_service.json")))
	c, _ := newTestClient(t, f)

	topo, err := c.Topology(ctx)
	if err != nil || topo != TopologyFullMesh {
		t.Fatalf("topology = %q, %v", topo, err)
	}

	h, err := c.CreateHost(ctx, HostCreateRequest{
		Name: "web1", Description: "[ccx] created by ccx dev", InternetAccess: InternetSplitTunnelOn,
		Domain: "web1.internal", Connectors: []HostConnectorRequest{{Name: "web1-connector", VPNRegionID: "us-east-1"}},
	})
	if err != nil || h.ID != hostID || len(h.Connectors) != 1 || h.Connectors[0].ID != connID {
		t.Fatalf("create host = %+v, %v", h, err)
	}

	conns, err := c.ListConnectors(ctx, hostID)
	if err != nil || len(conns) != 1 || conns[0].IPv4Address != "100.96.1.10" {
		t.Fatalf("connectors = %+v, %v", conns, err)
	}
	conn, err := c.GetConnector(ctx, connID)
	if err != nil || conn.ConnectionStatus != StatusOffline {
		t.Fatalf("connector = %+v, %v", conn, err)
	}
	tok, err := c.ProfileToken(ctx, connID)
	if err != nil || !strings.HasPrefix(string(tok), "AbCdEf") {
		t.Fatalf("token err = %v", err)
	}
	if err := c.SuspendConnector(ctx, connID); err != nil {
		t.Fatal(err)
	}
	if err := c.ActivateConnector(ctx, connID); err != nil {
		t.Fatal(err)
	}
	ssh, http80 := 22, 80
	svc, err := c.CreateIPService(ctx, hostID, IPServiceRequest{Name: "web1-services", Config: ServiceConfig{
		ServiceTypes: []string{ServiceCustom},
		CustomServiceTypes: []CustomServiceType{
			{Protocol: "TCP", Port: []RangeInteger{{Value: &ssh}, {LowerValue: &http80, UpperValue: &http80}}},
		},
	}})
	if err != nil || svc.ID == "" {
		t.Fatalf("ip service = %+v, %v", svc, err)
	}
	if err := c.DeleteHost(ctx, hostID); err != nil {
		t.Fatal(err)
	}

	// Check request shapes against v1.json.
	reqs := f.apiRequests()
	byRoute := map[string]recorded{}
	for _, r := range reqs {
		byRoute[r.Method+" "+r.Path] = r
	}
	create := byRoute["POST /api/v1/hosts"]
	var sent map[string]any
	if err := json.Unmarshal([]byte(create.Body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["domain"] != "web1.internal" || sent["internetAccess"] != "SPLIT_TUNNEL_ON" {
		t.Fatalf("create host body = %s", create.Body)
	}
	if conns := sent["connectors"].([]any); conns[0].(map[string]any)["vpnRegionId"] != "us-east-1" {
		t.Fatalf("create host connectors = %v", conns)
	}
	svcReq := byRoute["POST /api/v1/hosts/ip-services"]
	if svcReq.Query != "hostId="+hostID {
		t.Fatalf("ip service query = %q", svcReq.Query)
	}
	wantSvc := `{"name":"web1-services","config":{"serviceTypes":["CUSTOM"],"customServiceTypes":[{"protocol":"TCP","port":[{"value":22},{"lowerValue":80,"upperValue":80}]}]}}`
	if svcReq.Body != wantSvc {
		t.Fatalf("ip service body:\n got %s\nwant %s", svcReq.Body, wantSvc)
	}
	if q := byRoute["GET /api/v1/hosts/connectors"].Query; !strings.Contains(q, "hostId="+hostID) || !strings.Contains(q, "page=0") {
		t.Fatalf("list connectors query = %q", q)
	}
}

func mustConnectorJSON(t *testing.T) []byte {
	t.Helper()
	var h struct {
		Connectors []json.RawMessage `json:"connectors"`
	}
	if err := json.Unmarshal(fixture(t, "host.json"), &h); err != nil {
		t.Fatal(err)
	}
	return h.Connectors[0]
}

func TestAPIErrorBodyParsed(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST /api/v1/hosts", writeJSON(400, fixture(t, "error_400.json")))
	c, _ := newTestClient(t, f)
	_, err := c.CreateHost(ctx, HostCreateRequest{Name: "web1"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("got %v", err)
	}
	if want := "Validation failed; domain: must be a valid domain"; apiErr.Message != want {
		t.Fatalf("message = %q, want %q", apiErr.Message, want)
	}
}

func TestResponseValidation(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST /api/v1/hosts", writeJSON(201, []byte(`{"name":"web1"}`)))
	f.handle("POST /api/v1/hosts/connectors/abc/profile/encrypt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "token; rm -rf / #0123456789")
	})
	c, _ := newTestClient(t, f)
	if _, err := c.CreateHost(ctx, HostCreateRequest{Name: "web1"}); err == nil {
		t.Fatal("expected error for host without id")
	}
	_, err := c.ProfileToken(ctx, "abc")
	if err == nil || strings.Contains(err.Error(), "rm -rf") {
		t.Fatalf("profile token with shell metacharacters: %v", err)
	}
	if _, err := c.GetHost(ctx, "../oauth/token"); err == nil {
		t.Fatal("expected error for path-traversal id")
	}
}

// TestRedaction checks the secret and tokens never reach error strings or logs.
func TestRedaction(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	withLogger := func(cfg *Config) { cfg.Logger = logger }
	var errs []string

	// 1. Token endpoint rejects and echoes the secret in its body.
	f := newFakeAPI(t)
	f.tokenStatus = 401
	c, _ := newTestClient(t, f, withLogger)
	if err := c.Authenticate(ctx); err != nil {
		errs = append(errs, err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err))
	}

	// 2. Network failure while credentials are in the query string.
	f2 := newFakeAPI(t)
	f2.acceptForm = false
	c2, _ := newTestClient(t, f2, withLogger)
	if err := c2.Authenticate(ctx); err != nil {
		t.Fatal(err)
	}
	c2.invalidateToken()
	f2.srv.Close()
	if err := c2.Authenticate(ctx); err == nil {
		t.Fatal("expected network error")
	} else {
		errs = append(errs, err.Error())
	}

	// 3. Profile token and access token in debug logs / formatted values.
	f3 := newFakeAPI(t)
	const profileTok = "PROFILEtoken0123456789abcdefABCDEF"
	f3.handle("POST /api/v1/hosts/connectors/abc/profile/encrypt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, profileTok)
	})
	c3, _ := newTestClient(t, f3, withLogger)
	tok, err := c3.ProfileToken(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	errs = append(errs, fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v", tok, c3, c3))
	logger.Debug("formatted", "token", tok, "client", c3)

	all := strings.Join(errs, "\n") + logs.String()
	for _, secret := range []string{testSecret, profileTok, "access-token-"} {
		if strings.Contains(all, secret) {
			t.Errorf("leaked %q in:\n%s", secret, all)
		}
	}
}
