//go:build windows

package tun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Be1zebub/sing-box-tray-runner/internal/config"
)

// writeFixture writes a minimal sing-box config whose only inbound is not a
// tun, so InjectTUN has to append the default tun inbound.
func writeFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	src := `{
  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080}],
  "outbounds": [{"type": "direct", "tag": "direct"}]
}`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func isIPv6(cidr string) bool { return strings.Contains(cidr, ":") }

// TestBuildTUNInbound pins the defaults and the tray-config.json overrides.
func TestBuildTUNInbound(t *testing.T) {
	cases := []struct {
		name             string
		cfg              config.TUNConfig
		wantInterface    string
		wantMTU          int
		wantAddress      []string
		wantRouteAddr    []string
		wantRouteExclude []string
	}{
		{
			name:             "defaults",
			cfg:              config.TUNConfig{},
			wantInterface:    "singbox-tun",
			wantMTU:          9000,
			wantAddress:      defaultTUNAddress,
			wantRouteAddr:    defaultRouteAddress,
			wantRouteExclude: defaultRouteExcludeAddress,
		},
		{
			name: "overrides",
			cfg: config.TUNConfig{
				InterfaceName:       "my-tun",
				MTU:                 1400,
				Address:             []string{"10.9.0.1/30"},
				RouteAddress:        []string{"0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2"},
				RouteExcludeAddress: []string{"127.0.0.0/8"},
			},
			wantInterface:    "my-tun",
			wantMTU:          1400,
			wantAddress:      []string{"10.9.0.1/30"},
			wantRouteAddr:    []string{"0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2"},
			wantRouteExclude: []string{"127.0.0.0/8"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildTUNInbound(tc.cfg)

			if got["interface_name"] != tc.wantInterface {
				t.Errorf("interface_name = %v, want %q", got["interface_name"], tc.wantInterface)
			}
			if got["mtu"] != tc.wantMTU {
				t.Errorf("mtu = %v, want %d", got["mtu"], tc.wantMTU)
			}
			assertStrings(t, "address", got["address"], tc.wantAddress)
			assertStrings(t, "route_address", got["route_address"], tc.wantRouteAddr)
			assertStrings(t, "route_exclude_address", got["route_exclude_address"], tc.wantRouteExclude)

			// strict_route must stay on for its DNS-leak protection, and
			// auto_route for the routing table itself.
			if got["strict_route"] != true || got["auto_route"] != true {
				t.Errorf("strict_route/auto_route must both be true, got %v/%v",
					got["strict_route"], got["auto_route"])
			}
		})
	}
}

// TestInjectTUNDefaults runs the full config rewrite and checks the JSON that
// actually lands on disk.
func TestInjectTUNDefaults(t *testing.T) {
	out, err := InjectTUN(writeFixture(t), config.TUNConfig{}, "sing-box.exe", config.SplitTUN{})
	if err != nil {
		t.Fatalf("InjectTUN: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(out) })

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read injected config: %v", err)
	}

	var got struct {
		Inbounds []struct {
			Type                string   `json:"type"`
			Address             []string `json:"address"`
			RouteAddress        []string `json:"route_address"`
			RouteExcludeAddress []string `json:"route_exclude_address"`
		} `json:"inbounds"`
		Route struct {
			AutoDetectInterface bool             `json:"auto_detect_interface"`
			Rules               []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse injected config: %v", err)
	}

	if len(got.Inbounds) != 1 || got.Inbounds[0].Type != "tun" {
		t.Fatalf("want exactly one tun inbound, got %+v", got.Inbounds)
	}
	in := got.Inbounds[0]

	// An IPv6 address on the interface is what stops strict_route from
	// installing an unconditional IPv6 block filter that kills ::1.
	if !slices.ContainsFunc(in.Address, isIPv6) {
		t.Errorf("address must include an IPv6 CIDR, got %v", in.Address)
	}
	if len(in.RouteAddress) == 0 || slices.ContainsFunc(in.RouteAddress, isIPv6) {
		t.Errorf("route_address must be non-empty and IPv4-only, got %v", in.RouteAddress)
	}
	for _, want := range []string{"::1/128", "10.0.0.0/8", "192.168.0.0/16"} {
		if !slices.Contains(in.RouteExcludeAddress, want) {
			t.Errorf("route_exclude_address missing %q: %v", want, in.RouteExcludeAddress)
		}
	}

	if !got.Route.AutoDetectInterface {
		t.Error("route.auto_detect_interface must be injected for auto_route to work")
	}

	// Rule order: private/loopback first, then the process bypass. Both must
	// use the explicit action: "route" form.
	if len(got.Route.Rules) < 2 {
		t.Fatalf("want at least the two injected rules, got %v", got.Route.Rules)
	}
	private := got.Route.Rules[0]
	if private["ip_is_private"] != true {
		t.Errorf(`rules[0] must be the ip_is_private rule, got %v`, private)
	}
	assertRouteDirect(t, "rules[0]", private)

	process := got.Route.Rules[1]
	names, ok := process["process_name"].([]any)
	if !ok || len(names) != 1 || names[0] != "sing-box.exe" {
		t.Errorf(`rules[1] must target process_name ["sing-box.exe"], got %v`, process["process_name"])
	}
	assertRouteDirect(t, "rules[1]", process)
}

func TestInjectTUNSplitBypass(t *testing.T) {
	split := config.SplitTUN{
		IPCIDR:       []string{"1.2.3.4/32", "2001:db8::/32"},
		DomainSuffix: []string{"example.com"},
		ProcessName:  []string{"Discord.exe"},
		ProcessPath:  []string{`C:\Games\Game.exe`},
	}
	out, err := InjectTUN(writeFixture(t), config.TUNConfig{}, "sing-box.exe", split)
	if err != nil {
		t.Fatalf("InjectTUN: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(out) })

	got := readInjected(t, out)
	in := tunInbound(t, got)
	for _, want := range []string{"1.2.3.4/32", "2001:db8::/32", "10.0.0.0/8"} {
		if !slices.Contains(in.RouteExcludeAddress, want) {
			t.Errorf("route_exclude_address missing %q: %v", want, in.RouteExcludeAddress)
		}
	}

	rules := got.Route.Rules
	if len(rules) != 7 {
		t.Fatalf("want sniff + private + self + 4 split rules, got %v", rules)
	}
	if rules[0]["action"] != "sniff" {
		t.Errorf("rules[0] = %v, want sniff", rules[0])
	}
	if rules[1]["ip_is_private"] != true {
		t.Errorf("rules[1] = %v, want ip_is_private", rules[1])
	}
	assertStringList(t, "rules[2].process_name", rules[2]["process_name"], []string{"sing-box.exe"})
	assertStringList(t, "rules[3].process_name", rules[3]["process_name"], []string{"Discord.exe"})
	assertStringList(t, "rules[4].process_path", rules[4]["process_path"], []string{`C:\Games\Game.exe`})
	assertStringList(t, "rules[5].ip_cidr", rules[5]["ip_cidr"], split.IPCIDR)
	assertStringList(t, "rules[6].domain_suffix", rules[6]["domain_suffix"], []string{"example.com"})
	for _, i := range []int{1, 2, 3, 4, 5, 6} {
		assertRouteDirect(t, "rules", rules[i])
	}
}

func TestInjectTUNSplitKeepsExistingTunInbound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	src := `{
	  "inbounds": [{
	    "type": "tun",
	    "tag": "my-tun",
	    "address": ["172.19.0.1/30"],
	    "route_exclude_address": ["10.0.0.0/8", "1.2.3.4/32"]
	  }],
	  "route": {"rules": [{"domain_suffix": ["keep.example"], "action": "route", "outbound": "proxy"}]}
	}`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := InjectTUN(p, config.TUNConfig{}, "sing-box.exe", config.SplitTUN{
		IPCIDR: []string{"1.2.3.4/32", "9.9.9.9/32"},
	})
	if err != nil {
		t.Fatalf("InjectTUN: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(out) })

	got := readInjected(t, out)
	if len(got.Inbounds) != 1 || got.Inbounds[0].Tag != "my-tun" {
		t.Fatalf("existing tun inbound was replaced: %+v", got.Inbounds)
	}
	in := got.Inbounds[0]
	if !slices.Equal(in.Address, []string{"172.19.0.1/30"}) {
		t.Errorf("address = %v", in.Address)
	}
	if !slices.Equal(in.RouteExcludeAddress, []string{"10.0.0.0/8", "1.2.3.4/32", "9.9.9.9/32"}) {
		t.Errorf("route_exclude_address = %v", in.RouteExcludeAddress)
	}

	rules := got.Route.Rules
	if len(rules) != 4 {
		t.Fatalf("want private + self + ip_cidr + original, got %v", rules)
	}
	if rules[0]["action"] == "sniff" {
		t.Fatal("sniff must not be injected without domain_suffix entries")
	}
	assertStringList(t, "rules[2].ip_cidr", rules[2]["ip_cidr"], []string{"1.2.3.4/32", "9.9.9.9/32"})
	assertStringList(t, "last rule domain", rules[3]["domain_suffix"], []string{"keep.example"})
	if rules[3]["outbound"] != "proxy" {
		t.Errorf("original rule outbound = %v", rules[3]["outbound"])
	}
}

type injectedConfig struct {
	Inbounds []struct {
		Type                string   `json:"type"`
		Tag                 string   `json:"tag"`
		Address             []string `json:"address"`
		RouteAddress        []string `json:"route_address"`
		RouteExcludeAddress []string `json:"route_exclude_address"`
	} `json:"inbounds"`
	Route struct {
		AutoDetectInterface bool             `json:"auto_detect_interface"`
		Rules               []map[string]any `json:"rules"`
	} `json:"route"`
}

func readInjected(t *testing.T, path string) injectedConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read injected config: %v", err)
	}
	var got injectedConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse injected config: %v", err)
	}
	return got
}

func tunInbound(t *testing.T, got injectedConfig) struct {
	Type                string
	Tag                 string
	Address             []string
	RouteAddress        []string
	RouteExcludeAddress []string
} {
	t.Helper()
	if len(got.Inbounds) != 1 || got.Inbounds[0].Type != "tun" {
		t.Fatalf("want exactly one tun inbound, got %+v", got.Inbounds)
	}
	in := got.Inbounds[0]
	return struct {
		Type                string
		Tag                 string
		Address             []string
		RouteAddress        []string
		RouteExcludeAddress []string
	}{in.Type, in.Tag, in.Address, in.RouteAddress, in.RouteExcludeAddress}
}

func assertStringList(t *testing.T, field string, got any, want []string) {
	t.Helper()
	raw, ok := got.([]any)
	if !ok {
		t.Fatalf("%s is %T, want list", field, got)
	}
	have := make([]string, len(raw))
	for i, v := range raw {
		have[i], ok = v.(string)
		if !ok {
			t.Fatalf("%s[%d] is %T", field, i, v)
		}
	}
	if !slices.Equal(have, want) {
		t.Errorf("%s = %v, want %v", field, have, want)
	}
}

// assertRouteDirect checks a rule is the modern action/outbound direct form.
func assertRouteDirect(t *testing.T, where string, rule map[string]any) {
	t.Helper()
	if rule["action"] != "route" {
		t.Errorf(`%s action = %v, want "route"`, where, rule["action"])
	}
	if rule["outbound"] != "direct" {
		t.Errorf(`%s outbound = %v, want "direct"`, where, rule["outbound"])
	}
}

func assertStrings(t *testing.T, field string, got any, want []string) {
	t.Helper()
	gotSlice, ok := got.([]string)
	if !ok {
		t.Fatalf("%s is %T, want []string", field, got)
	}
	if !slices.Equal(gotSlice, want) {
		t.Errorf("%s = %v, want %v", field, gotSlice, want)
	}
}
