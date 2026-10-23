package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	testClientID = "ccx-test.client"
	testSecret   = "S3cr3tCl1entValue9876"
)

// recorded is one request seen by the fake API.
type recorded struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// fakeAPI imitates the CloudConnexa v1 API for one test.
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// Token endpoint behaviour.
	acceptForm  bool
	acceptQuery bool
	tokenStatus int    // if non-zero, the token endpoint always returns this
	expiresIn   string // raw JSON for expires_in
	tokenCalls  int
	tokenSeq    int
	validToken  string
	// Routes: "METHOD /path" → handler. Unmatched routes return 404.
	routes   map[string]http.HandlerFunc
	requests []recorded
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, acceptForm: true, acceptQuery: true, expiresIn: "43199", routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) handle(route string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
	f.mu.Unlock()

	if r.URL.Path == tokenPath {
		f.serveToken(w, r, string(body))
		return
	}
	f.mu.Lock()
	valid := f.validToken
	h := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if valid == "" || r.Header.Get("Authorization") != "Bearer "+valid {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if h == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h(w, r)
}

func (f *fakeAPI) serveToken(w http.ResponseWriter, r *http.Request, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if f.tokenStatus != 0 {
		// Echo the secret back to prove error paths never surface bodies.
		w.WriteHeader(f.tokenStatus)
		fmt.Fprintf(w, `{"errorMessage":"bad secret %s"}`, testSecret)
		return
	}
	ok := false
	if f.acceptForm && r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		form, err := url.ParseQuery(body) // r.Body was already drained by serve
		if err == nil && form.Get("client_id") == testClientID && form.Get("client_secret") == testSecret && form.Get("grant_type") == "client_credentials" {
			ok = true
		}
	}
	q := r.URL.Query()
	if !ok && f.acceptQuery && q.Get("client_id") == testClientID && q.Get("client_secret") == testSecret && q.Get("grant_type") == "client_credentials" {
		ok = true
	}
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.tokenSeq++
	f.validToken = fmt.Sprintf("access-token-%d", f.tokenSeq)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":%s,"scope":"read write"}`, f.validToken, f.expiresIn)
}

func (f *fakeAPI) apiRequests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.requests {
		if r.Path != tokenPath {
			out = append(out, r)
		}
	}
	return out
}

// testClient returns a client wired to f with a fake clock and no real sleeps.
type testClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (tc *testClock) Now() time.Time {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.now
}

func (tc *testClock) Advance(d time.Duration) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.now = tc.now.Add(d)
}

func (tc *testClock) Sleep(_ context.Context, d time.Duration) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.sleeps = append(tc.sleeps, d)
	return nil
}

func newTestClient(t *testing.T, f *fakeAPI, opts ...func(*Config)) (*Client, *testClock) {
	t.Helper()
	cfg := Config{BaseURL: f.srv.URL, ClientID: testClientID, ClientSecret: testSecret}
	for _, o := range opts {
		o(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	c.now = clock.Now
	c.sleep = clock.Sleep
	return c, clock
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeJSON(status int, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}
