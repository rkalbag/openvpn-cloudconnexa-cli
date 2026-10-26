// Package fakeapi is a stateful in-memory imitation of the CloudConnexa v1
// endpoints ccx uses, for tests. Shapes follow api/v1.json.
package fakeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/api"
)

// Credentials accepted by the fake token endpoint.
const (
	ClientID = "ccx-test.client"
	Secret   = "S3cr3tCl1entValue9876"
)

// Server is a fake CloudConnexa API.
type Server struct {
	URL string

	mu         sync.Mutex
	srv        *httptest.Server
	seq        int
	token      string
	Regions    []api.Region
	Topology   string
	hosts      map[string]*api.Host
	order      []string // host ids in creation order
	services   map[string][]api.IPServiceRequest
	suspended  map[string]bool
	tokens     map[string]string // connector id → current profile token
	failures   map[string][]int  // "METHOD /path-pattern" → statuses to return next
	after      map[string][]int  // like failures, but the request takes effect first
	Requests   []string          // "METHOD /path?query" for every call
	TokenCalls int
}

// New starts a fake server that is closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		Regions: []api.Region{
			{ID: "us-east-1", Continent: "North America", Country: "United States", CountryISO: "US", RegionName: "New York"},
			{ID: "eu-central-1", Continent: "Europe", Country: "Germany", CountryISO: "DE", RegionName: "Frankfurt"},
			{ID: "eu-west-1", Continent: "Europe", Country: "Ireland", CountryISO: "IE", RegionName: "Dublin"},
		},
		Topology:  api.TopologyFullMesh,
		hosts:     map[string]*api.Host{},
		services:  map[string][]api.IPServiceRequest{},
		suspended: map[string]bool{},
		tokens:    map[string]string{},
		failures:  map[string][]int{},
		after:     map[string][]int{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// Fail makes the next len(statuses) calls matching route return those
// statuses. route is "METHOD pattern" where pattern may use {id}, e.g.
// "POST /api/v1/hosts/connectors/{id}/profile/encrypt".
func (s *Server) Fail(route string, statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[route] = append(s.failures[route], statuses...)
}

// FailAfter is like Fail, but each matching request is carried out before
// the error status is returned (e.g. a create that succeeds behind a 502).
func (s *Server) FailAfter(route string, statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.after[route] = append(s.after[route], statuses...)
}

// AddHost inserts a host directly (e.g. one not created by ccx).
func (s *Server) AddHost(name, description, domain, status string) *api.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.newHostLocked(api.HostCreateRequest{
		Name: name, Description: description, Domain: domain,
		Connectors: []api.HostConnectorRequest{{Name: name + "-connector", VPNRegionID: "us-east-1"}},
	})
	h.Connectors[0].ConnectionStatus = status
	return h
}

// SetStatus sets the connection status of a host's connectors.
func (s *Server) SetStatus(hostName, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		if h.Name == hostName {
			for i := range h.Connectors {
				h.Connectors[i].ConnectionStatus = status
			}
		}
	}
}

// Hosts returns a snapshot of all hosts in creation order.
func (s *Server) Hosts() []api.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []api.Host{}
	for _, id := range s.order {
		if h, ok := s.hosts[id]; ok {
			out = append(out, *h)
		}
	}
	return out
}

// Services returns the IP services created for a host.
func (s *Server) Services(hostID string) []api.IPServiceRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.IPServiceRequest(nil), s.services[hostID]...)
}

// Suspended reports whether a connector is suspended.
func (s *Server) Suspended(connectorID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suspended[connectorID]
}

// ProfileToken returns the current profile token of a connector.
func (s *Server) ProfileToken(connectorID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[connectorID]
}

func (s *Server) nextID() string {
	s.seq++
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", s.seq)
}

func (s *Server) newHostLocked(req api.HostCreateRequest) *api.Host {
	h := &api.Host{
		ID: s.nextID(), Name: req.Name, Description: req.Description,
		InternetAccess: req.InternetAccess, Domain: req.Domain,
		SystemSubnets: []string{}, GatewaysIDs: []string{},
	}
	for i, c := range req.Connectors {
		h.Connectors = append(h.Connectors, api.Connector{
			ID: s.nextID(), NetworkItemID: h.ID, NetworkItemType: "HOST", Name: c.Name,
			Description: c.Description, VPNRegionID: c.VPNRegionID,
			IPv4Address: fmt.Sprintf("100.96.%d.%d", s.seq/250, 10+i+s.seq%200), ConnectionStatus: api.StatusOffline, Licensed: true,
		})
	}
	s.hosts[h.ID] = h
	s.order = append(s.order, h.ID)
	return h
}

// match reports whether path matches pattern with {id} wildcards.
func match(pattern, path string) (string, bool) {
	pp, ps := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(pp) != len(ps) {
		return "", false
	}
	id := ""
	for i := range pp {
		if pp[i] == "{id}" {
			id = ps[i]
			continue
		}
		if pp[i] != ps[i] {
			return "", false
		}
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)

	if r.URL.Path == "/api/v1/oauth/token" {
		s.serveToken(w, r, body)
		return
	}
	if s.token == "" || r.Header.Get("Authorization") != "Bearer "+s.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if status, ok := s.takeFailure(s.failures, r); ok {
		w.WriteHeader(status)
		return
	}
	if status, ok := s.takeFailure(s.after, r); ok {
		s.handle(httptest.NewRecorder(), r, body)
		w.WriteHeader(status)
		return
	}
	s.handle(w, r, body)
}

// takeFailure pops the next status queued for a route matching r.
func (s *Server) takeFailure(queue map[string][]int, r *http.Request) (int, bool) {
	for route, statuses := range queue {
		method, pattern, _ := strings.Cut(route, " ")
		if _, ok := match(pattern, r.URL.Path); ok && method == r.Method && len(statuses) > 0 {
			queue[route] = statuses[1:]
			return statuses[0], true
		}
	}
	return 0, false
}

// handle serves an authenticated API request. s.mu is held.
func (s *Server) handle(w http.ResponseWriter, r *http.Request, body []byte) {

	q := r.URL.Query()
	route := func(method, pattern string) (string, bool) {
		if r.Method != method {
			return "", false
		}
		return match(pattern, r.URL.Path)
	}
	if _, ok := route("GET", "/api/v1/regions"); ok {
		writeJSON(w, 200, s.Regions)
		return
	}
	if _, ok := route("GET", "/api/v1/settings/wpc/topology"); ok {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, s.Topology)
		return
	}
	if _, ok := route("POST", "/api/v1/hosts"); ok {
		var req api.HostCreateRequest
		if err := json.Unmarshal(body, &req); err != nil || req.Name == "" {
			writeJSON(w, 400, map[string]any{"errorMessage": "invalid host"})
			return
		}
		writeJSON(w, 201, s.newHostLocked(req))
		return
	}
	if _, ok := route("GET", "/api/v1/hosts"); ok {
		var all []api.Host
		for _, id := range s.order {
			if h, ok := s.hosts[id]; ok {
				all = append(all, *h)
			}
		}
		writePage(w, all, q)
		return
	}
	if _, ok := route("GET", "/api/v1/hosts/connectors"); ok {
		var all []api.Connector
		for _, id := range s.order {
			h, ok := s.hosts[id]
			if !ok || (q.Get("hostId") != "" && q.Get("hostId") != h.ID) {
				continue
			}
			all = append(all, h.Connectors...)
		}
		writePage(w, all, q)
		return
	}
	if _, ok := route("POST", "/api/v1/hosts/ip-services"); ok {
		hostID := q.Get("hostId")
		var req api.IPServiceRequest
		if _, exists := s.hosts[hostID]; !exists || json.Unmarshal(body, &req) != nil {
			writeJSON(w, 400, map[string]any{"errorMessage": "invalid IP service"})
			return
		}
		s.services[hostID] = append(s.services[hostID], req)
		writeJSON(w, 201, api.IPService{ID: s.nextID(), Name: req.Name, NetworkItemID: hostID, NetworkItemType: "HOST", Config: req.Config})
		return
	}
	if id, ok := route("POST", "/api/v1/hosts/connectors/{id}/profile/encrypt"); ok {
		if s.connectorLocked(id) == nil {
			w.WriteHeader(404)
			return
		}
		tok := fmt.Sprintf("ProfileTok%s%d", strings.ReplaceAll(id, "-", ""), s.nextSeq())
		s.tokens[id] = tok
		w.WriteHeader(200)
		_, _ = io.WriteString(w, tok)
		return
	}
	for _, action := range []string{"suspend", "activate"} {
		if id, ok := route("PUT", "/api/v1/hosts/connectors/{id}/"+action); ok {
			c := s.connectorLocked(id)
			if c == nil {
				w.WriteHeader(404)
				return
			}
			s.suspended[id] = action == "suspend"
			c.Licensed = action == "activate"
			if action == "suspend" {
				c.ConnectionStatus = api.StatusOffline
			}
			w.WriteHeader(200)
			return
		}
	}
	if id, ok := route("GET", "/api/v1/hosts/connectors/{id}"); ok {
		if c := s.connectorLocked(id); c != nil {
			writeJSON(w, 200, c)
			return
		}
		w.WriteHeader(404)
		return
	}
	if id, ok := route("GET", "/api/v1/hosts/{id}"); ok {
		if h, exists := s.hosts[id]; exists {
			writeJSON(w, 200, h)
			return
		}
		w.WriteHeader(404)
		return
	}
	if id, ok := route("DELETE", "/api/v1/hosts/{id}"); ok {
		h, exists := s.hosts[id]
		if !exists {
			w.WriteHeader(404)
			return
		}
		for _, c := range h.Connectors {
			delete(s.tokens, c.ID)
		}
		delete(s.hosts, id)
		delete(s.services, id)
		w.WriteHeader(204)
		return
	}
	w.WriteHeader(404)
}

func (s *Server) nextSeq() int {
	s.seq++
	return s.seq
}

func (s *Server) connectorLocked(id string) *api.Connector {
	for _, h := range s.hosts {
		for i := range h.Connectors {
			if h.Connectors[i].ID == id {
				return &h.Connectors[i]
			}
		}
	}
	return nil
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request, body []byte) {
	s.TokenCalls++
	creds := r.URL.Query()
	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		creds, _ = url.ParseQuery(string(body))
	}
	if creds.Get("client_id") != ClientID || creds.Get("client_secret") != Secret {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.token = "access-" + strconv.Itoa(s.nextSeq())
	writeJSON(w, 200, map[string]any{"access_token": s.token, "token_type": "bearer", "expires_in": 43199, "scope": "read write"})
}

func writePage[T any](w http.ResponseWriter, all []T, q url.Values) {
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 {
		size = 10
	}
	start := min(page*size, len(all))
	end := min(start+size, len(all))
	content := all[start:end]
	if content == nil {
		content = []T{}
	}
	totalPages := (len(all) + size - 1) / size
	writeJSON(w, 200, map[string]any{
		"content": content, "totalElements": len(all), "totalPages": totalPages,
		"numberOfElements": len(content), "page": page, "size": size,
	})
}

// SortedRegionIDs is a helper for assertions.
func (s *Server) SortedRegionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.Regions))
	for _, r := range s.Regions {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}
