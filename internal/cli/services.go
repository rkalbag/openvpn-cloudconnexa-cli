package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/api"
)

// parseServices turns `ccx add` service arguments into an IP service config.
//
// Accepted forms (case-insensitive, comma or space separated):
//   - a predefined service type: ssh, http, https, rdp, dns, …
//   - tcp:<port>, tcp:<lo>-<hi>, udp:<port>, udp:<lo>-<hi>
//   - icmp
//   - any (alone): all traffic, also the default when no services are given
func parseServices(args []string) (api.ServiceConfig, error) {
	var items []string
	for _, a := range args {
		for _, p := range strings.Split(a, ",") {
			if p = strings.TrimSpace(p); p != "" {
				items = append(items, strings.ToLower(p))
			}
		}
	}
	if len(items) == 0 {
		return api.ServiceConfig{ServiceTypes: []string{api.ServiceAny}}, nil
	}

	var named []string
	ports := map[string][]api.RangeInteger{} // protocol → ranges
	var protocols []string                   // first-seen order
	addProto := func(p string) {
		if _, ok := ports[p]; !ok {
			protocols = append(protocols, p)
			ports[p] = nil
		}
	}
	for _, it := range items {
		if it == "any" {
			if len(items) > 1 {
				return api.ServiceConfig{}, fmt.Errorf("service %q cannot be combined with other services", "any")
			}
			return api.ServiceConfig{ServiceTypes: []string{api.ServiceAny}}, nil
		}
		if it == "icmp" {
			addProto("ICMP")
			continue
		}
		if proto, spec, ok := strings.Cut(it, ":"); ok {
			if proto != "tcp" && proto != "udp" {
				return api.ServiceConfig{}, fmt.Errorf("invalid service %q: protocol must be tcp or udp", it)
			}
			r, err := parsePortRange(spec)
			if err != nil {
				return api.ServiceConfig{}, fmt.Errorf("invalid service %q: %w", it, err)
			}
			p := strings.ToUpper(proto)
			addProto(p)
			ports[p] = append(ports[p], r)
			continue
		}
		up := strings.ToUpper(it)
		if !slices.Contains(api.NamedServiceTypes, up) {
			return api.ServiceConfig{}, fmt.Errorf("unknown service %q: use one of %s, tcp:<port>, udp:<port>, icmp or any",
				it, strings.ToLower(strings.Join(api.NamedServiceTypes, ", ")))
		}
		if !slices.Contains(named, up) {
			named = append(named, up)
		}
	}

	cfg := api.ServiceConfig{ServiceTypes: named}
	for _, p := range protocols {
		ct := api.CustomServiceType{Protocol: p}
		if p == "ICMP" {
			lo, hi := 0, 255
			ct.ICMPType = []api.RangeInteger{{LowerValue: &lo, UpperValue: &hi}}
		} else {
			ct.Port = ports[p]
		}
		cfg.CustomServiceTypes = append(cfg.CustomServiceTypes, ct)
	}
	if len(cfg.CustomServiceTypes) > 0 {
		cfg.ServiceTypes = append(cfg.ServiceTypes, api.ServiceCustom)
	}
	return cfg, nil
}

func parsePortRange(s string) (api.RangeInteger, error) {
	port := func(v string) (int, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return 0, fmt.Errorf("port %q must be 1-65535", v)
		}
		return n, nil
	}
	if lo, hi, ok := strings.Cut(s, "-"); ok {
		l, err := port(lo)
		if err != nil {
			return api.RangeInteger{}, err
		}
		h, err := port(hi)
		if err != nil {
			return api.RangeInteger{}, err
		}
		if l > h {
			return api.RangeInteger{}, fmt.Errorf("port range %q is reversed", s)
		}
		if l == h {
			return api.RangeInteger{Value: &l}, nil
		}
		return api.RangeInteger{LowerValue: &l, UpperValue: &h}, nil
	}
	n, err := port(s)
	if err != nil {
		return api.RangeInteger{}, err
	}
	return api.RangeInteger{Value: &n}, nil
}

// describeServices is a short human summary, e.g. "SSH, HTTP, TCP 5432".
func describeServices(cfg api.ServiceConfig) string {
	var parts []string
	for _, t := range cfg.ServiceTypes {
		switch t {
		case api.ServiceCustom:
		case api.ServiceAny:
			parts = append(parts, "all traffic")
		default:
			parts = append(parts, t)
		}
	}
	for _, c := range cfg.CustomServiceTypes {
		if c.Protocol == "ICMP" {
			parts = append(parts, "ICMP")
			continue
		}
		for _, r := range c.Port {
			switch {
			case r.Value != nil:
				parts = append(parts, fmt.Sprintf("%s %d", c.Protocol, *r.Value))
			case r.LowerValue != nil && r.UpperValue != nil:
				parts = append(parts, fmt.Sprintf("%s %d-%d", c.Protocol, *r.LowerValue, *r.UpperValue))
			}
		}
	}
	return strings.Join(parts, ", ")
}
