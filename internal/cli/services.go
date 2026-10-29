package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/api"
)

// appSpec is one host application ccx creates: <label>.<host domain>,
// allowing only the traffic in config.
type appSpec struct {
	label  string
	config api.ServiceConfig
}

// parseServices turns `ccx add` service arguments into one application per
// service.
//
// Accepted forms (case-insensitive, comma or space separated):
//   - a predefined service type: ssh, http, https, rdp, dns, … (label "https")
//   - tcp:<port>, tcp:<lo>-<hi>, udp:<port>, udp:<lo>-<hi> (label "tcp5432")
//   - icmp
//   - any (alone): all traffic
//
// No arguments means no applications.
func parseServices(args []string) ([]appSpec, error) {
	var items []string
	for _, a := range args {
		for _, p := range strings.Split(a, ",") {
			if p = strings.TrimSpace(p); p != "" {
				items = append(items, strings.ToLower(p))
			}
		}
	}

	var specs []appSpec
	add := func(sp appSpec) {
		if !slices.ContainsFunc(specs, func(o appSpec) bool { return o.label == sp.label }) {
			specs = append(specs, sp)
		}
	}
	for _, it := range items {
		switch {
		case it == "any":
			if len(items) > 1 {
				return nil, fmt.Errorf("service %q cannot be combined with other services", "any")
			}
			add(appSpec{"any", api.ServiceConfig{ServiceTypes: []string{api.ServiceAny}}})
		case it == "icmp":
			lo, hi := 0, 255
			add(appSpec{"icmp", customConfig(api.CustomServiceType{Protocol: "ICMP", ICMPType: []api.RangeInteger{{LowerValue: &lo, UpperValue: &hi}}})})
		case strings.Contains(it, ":"):
			proto, spec, _ := strings.Cut(it, ":")
			if proto != "tcp" && proto != "udp" {
				return nil, fmt.Errorf("invalid service %q: protocol must be tcp or udp", it)
			}
			r, err := parsePortRange(spec)
			if err != nil {
				return nil, fmt.Errorf("invalid service %q: %w", it, err)
			}
			var label string
			if r.Value != nil {
				label = proto + strconv.Itoa(*r.Value)
			} else {
				label = fmt.Sprintf("%s%d-%d", proto, *r.LowerValue, *r.UpperValue)
			}
			add(appSpec{label, customConfig(api.CustomServiceType{Protocol: strings.ToUpper(proto), Port: []api.RangeInteger{r}})})
		default:
			up := strings.ToUpper(it)
			if !slices.Contains(api.NamedServiceTypes, up) {
				return nil, fmt.Errorf("unknown service %q: use one of %s, tcp:<port>, udp:<port>, icmp or any",
					it, strings.ToLower(strings.Join(api.NamedServiceTypes, ", ")))
			}
			add(appSpec{it, api.ServiceConfig{ServiceTypes: []string{up}}})
		}
	}
	return specs, nil
}

func customConfig(ct api.CustomServiceType) api.ServiceConfig {
	return api.ServiceConfig{ServiceTypes: []string{api.ServiceCustom}, CustomServiceTypes: []api.CustomServiceType{ct}}
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
