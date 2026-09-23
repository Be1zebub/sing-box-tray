//go:build windows

package tun

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Be1zebub/sing-box-tray-runner/internal/config"
)

// InjectTUN reads the sing-box config at sbConfigPath and strips any inbound
// that is not a tun inbound (so an http/mixed inbound left over from the base
// config isn't run alongside TUN). If the base config already has a tun
// inbound, it is kept; otherwise a default one built from cfg is appended.
// split ip_cidr values are appended to that inbound's route_exclude_address,
// and split process/domain/ip entries are prepended as direct route rules
// (a sniff action is prepended first when domain_suffix is set). A
// process-exclusion route rule for sing-box itself is always prepended so its
// own traffic is not looped back through TUN. The result is written to a temp
// file, whose path is returned.
//
// The injected tun inbound always carries an IPv6 address in addition to the
// IPv4 one: with strict_route enabled, sing-tun installs an unconditional WFP
// block filter on the IPv6 connect layer when the interface has no IPv6
// address, which blackholes ::1 and breaks anything that resolves localhost
// to IPv6 (Node/Vite, Next, etc.). route_address stays IPv4-only so no IPv6
// default route is added, and route_exclude_address keeps loopback, private
// and link-local traffic out of the tunnel.
func InjectTUN(sbConfigPath string, cfg config.TUNConfig, singBoxPath string, split config.SplitTUN) (string, error) {
	root, err := config.LoadRawSingBoxConfig(sbConfigPath)
	if err != nil {
		return "", err
	}

	inbounds, err := config.FilterInbounds(root["inbounds"], "tun")
	if err != nil {
		return "", err
	}

	if len(inbounds) == 0 {
		tunInbound := buildTUNInbound(cfg)
		if len(split.IPCIDR) > 0 {
			tunInbound["route_exclude_address"] = mergeUnique(asStrings(tunInbound["route_exclude_address"]), split.IPCIDR)
		}
		tunRaw, err := json.Marshal(tunInbound)
		if err != nil {
			return "", fmt.Errorf("marshal tun inbound: %w", err)
		}
		inbounds = append(inbounds, json.RawMessage(tunRaw))
	} else if len(split.IPCIDR) > 0 {
		inbounds, err = appendRouteExcludes(inbounds, split.IPCIDR)
		if err != nil {
			return "", err
		}
	}

	inboundsRaw, err := json.Marshal(inbounds)
	if err != nil {
		return "", fmt.Errorf("marshal inbounds: %w", err)
	}
	root["inbounds"] = json.RawMessage(inboundsRaw)

	// Prepend route rules that send sing-box's own process traffic, and any
	// split-tun.json bypasses, directly — so they are not looped back through
	// the TUN interface.
	if err := injectSelfBypassRule(root, filepath.Base(singBoxPath), split); err != nil {
		return "", err
	}

	return config.WriteRawSingBoxConfig(root)
}

// EnsureWintunDll copies wintun.dll from src to dstDir/wintun.dll if the
// destination does not already exist.
func EnsureWintunDll(src, dstDir string) error {
	if src == "" {
		return nil
	}
	dst := filepath.Join(dstDir, "wintun.dll")
	if _, err := os.Stat(dst); err == nil {
		return nil // already present
	}
	return copyFile(src, dst)
}

// Defaults used when tray-config.json leaves the corresponding tun.* field
// empty. tun.address MUST include an IPv6 address — see InjectTUN for why.
var (
	defaultTUNAddress = []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"}

	// IPv4-only on purpose: an IPv6 entry here would make auto_route install
	// an IPv6 default route, and direct IPv6 traffic could re-enter the TUN.
	defaultRouteAddress = []string{"0.0.0.0/1", "128.0.0.0/1"}

	// Loopback, RFC1918 and link-local, kept out of the tunnel entirely.
	defaultRouteExcludeAddress = []string{
		"127.0.0.0/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16",
		"fc00::/7", "fe80::/10", "ff00::/8",
	}
)

func buildTUNInbound(cfg config.TUNConfig) map[string]any {
	addr := cfg.Address
	if len(addr) == 0 {
		addr = defaultTUNAddress
	}
	routeAddr := cfg.RouteAddress
	if len(routeAddr) == 0 {
		routeAddr = defaultRouteAddress
	}
	routeExclude := cfg.RouteExcludeAddress
	if len(routeExclude) == 0 {
		routeExclude = defaultRouteExcludeAddress
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 9000
	}
	name := cfg.InterfaceName
	if name == "" {
		name = "singbox-tun"
	}
	return map[string]any{
		"type":                  "tun",
		"tag":                   "tun-in",
		"interface_name":        name,
		"address":               addr,
		"mtu":                   mtu,
		"auto_route":            true,
		"strict_route":          true,
		"route_address":         routeAddr,
		"route_exclude_address": routeExclude,
	}
}

// injectSelfBypassRule patches the route section for TUN mode:
//   - sets auto_detect_interface: true so sing-box knows which physical NIC to
//     use when building the auto_route routing table (without this the default
//     routes that redirect browser traffic into TUN are not set up correctly on
//     Windows)
//   - prepends "direct" rules: ip_is_private (so private/loopback destinations
//     never enter the tunnel), a process_name rule for sing-box's own
//     connections (breaking the TUN loop for the proxy process itself), then
//     the split-tun.json bypasses. When split lists domains, a sniff action
//     goes first so domain_suffix can match the TLS SNI / HTTP Host. Direct
//     rules use the explicit action: "route" form; the bare top-level
//     "outbound" form is deprecated.
func injectSelfBypassRule(root map[string]json.RawMessage, processName string, split config.SplitTUN) error {
	var route map[string]json.RawMessage
	if raw, ok := root["route"]; ok {
		if err := json.Unmarshal(raw, &route); err != nil {
			return fmt.Errorf("parse route: %w", err)
		}
	} else {
		route = make(map[string]json.RawMessage)
	}

	// auto_detect_interface is required for auto_route to work on Windows: it
	// tells sing-box which physical interface carries the real default gateway,
	// so it can add exclusion routes and properly redirect all other traffic
	// through TUN. Only inject if the user hasn't set it explicitly.
	if _, ok := route["auto_detect_interface"]; !ok {
		trueVal, _ := json.Marshal(true)
		route["auto_detect_interface"] = trueVal
	}

	var rules []json.RawMessage
	if raw, ok := route["rules"]; ok {
		if err := json.Unmarshal(raw, &rules); err != nil {
			return fmt.Errorf("parse route.rules: %w", err)
		}
	}

	prefix, err := bypassRules(processName, split)
	if err != nil {
		return err
	}
	rules = append(prefix, rules...)

	rulesRaw, err := json.Marshal(rules)
	if err != nil {
		return fmt.Errorf("marshal route.rules: %w", err)
	}
	route["rules"] = rulesRaw

	routeRaw, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("marshal route: %w", err)
	}
	root["route"] = routeRaw
	return nil
}

// bypassRules returns the rules prepended to route.rules. Sniff is included
// only when domains need it; otherwise an unconditional sniff would change
// how the user's own later rules match.
func bypassRules(processName string, split config.SplitTUN) ([]json.RawMessage, error) {
	var prefix []json.RawMessage
	if len(split.DomainSuffix) > 0 {
		sniff, err := json.Marshal(map[string]any{"action": "sniff"})
		if err != nil {
			return nil, fmt.Errorf("marshal sniff rule: %w", err)
		}
		prefix = append(prefix, sniff)
	}

	private, err := directRule(map[string]any{"ip_is_private": true})
	if err != nil {
		return nil, err
	}
	self, err := directRule(map[string]any{"process_name": []string{processName}})
	if err != nil {
		return nil, err
	}
	prefix = append(prefix, private, self)

	if len(split.ProcessName) > 0 {
		rule, err := directRule(map[string]any{"process_name": split.ProcessName})
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, rule)
	}
	if len(split.ProcessPath) > 0 {
		rule, err := directRule(map[string]any{"process_path": split.ProcessPath})
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, rule)
	}
	if len(split.IPCIDR) > 0 {
		rule, err := directRule(map[string]any{"ip_cidr": split.IPCIDR})
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, rule)
	}
	if len(split.DomainSuffix) > 0 {
		rule, err := directRule(map[string]any{"domain_suffix": split.DomainSuffix})
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, rule)
	}
	return prefix, nil
}

func directRule(match map[string]any) (json.RawMessage, error) {
	match["action"] = "route"
	match["outbound"] = "direct"
	raw, err := json.Marshal(match)
	if err != nil {
		return nil, fmt.Errorf("marshal direct rule: %w", err)
	}
	return raw, nil
}

// appendRouteExcludes adds cidrs to each tun inbound's route_exclude_address,
// keeping addresses the user already listed and skipping duplicates. Other
// inbound fields are left as they were.
func appendRouteExcludes(inbounds []json.RawMessage, cidrs []string) ([]json.RawMessage, error) {
	for i, raw := range inbounds {
		var inbound map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inbound); err != nil {
			return nil, fmt.Errorf("parse tun inbound: %w", err)
		}
		var existing []string
		if excl, ok := inbound["route_exclude_address"]; ok && len(excl) > 0 && string(excl) != "null" {
			if err := json.Unmarshal(excl, &existing); err != nil {
				return nil, fmt.Errorf("parse route_exclude_address: %w", err)
			}
		}
		merged, err := json.Marshal(mergeUnique(existing, cidrs))
		if err != nil {
			return nil, fmt.Errorf("marshal route_exclude_address: %w", err)
		}
		inbound["route_exclude_address"] = merged
		updated, err := json.Marshal(inbound)
		if err != nil {
			return nil, fmt.Errorf("marshal tun inbound: %w", err)
		}
		inbounds[i] = updated
	}
	return inbounds, nil
}

func mergeUnique(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, s := range list {
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open wintun.dll source: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create wintun.dll destination: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy wintun.dll: %w", err)
	}
	return nil
}
