//go:build windows

package tun

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/Be1zebub/sing-box-tray/internal/config"
)

// InjectTUN reads the sing-box config at sbConfigPath and strips any inbound
// that is not a tun inbound (so an http/mixed inbound left over from the base
// config isn't run alongside TUN). If the base config already has a tun
// inbound, it is kept; otherwise a default one built from cfg is appended.
// Blacklist ip_cidr values are appended to that inbound's route_exclude_address.
// Whitelist ip_cidr values are routed to route.final, and any exclude prefix
// that overlaps them is removed so those packets can enter the adapter.
// Split process/domain/ip entries are prepended as route rules
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
		excl := asStrings(tunInbound["route_exclude_address"])
		switch {
		case excludeCIDRs(split):
			tunInbound["route_exclude_address"] = mergeUnique(excl, split.IPCIDR)
		case whitelistCIDRs(split):
			tunInbound["route_exclude_address"] = dropOverlappingCIDRs(excl, split.IPCIDR)
		}
		tunRaw, err := json.Marshal(tunInbound)
		if err != nil {
			return "", fmt.Errorf("marshal tun inbound: %w", err)
		}
		inbounds = append(inbounds, json.RawMessage(tunRaw))
	} else {
		switch {
		case excludeCIDRs(split):
			inbounds, err = appendRouteExcludes(inbounds, split.IPCIDR)
		case whitelistCIDRs(split):
			inbounds, err = dropOverlappingExcludes(inbounds, split.IPCIDR)
		}
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
//   - prepends route rules. Blacklist: ip_is_private → direct, then sing-box's
//     own process, then the split-tun.json bypasses to direct. Whitelist:
//     sing-box's own process, the listed entries → route.final, then a
//     0.0.0.0/0 + ::/0 catch-all → direct. That catch-all sits in front of the
//     user's own rules, so they do not match IP traffic. When split lists
//     domains, a sniff action goes first so domain_suffix can match the TLS
//     SNI / HTTP Host. Rules use action "route"; the bare top-level
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

	prefix, err := bypassRules(processName, split, route)
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
func bypassRules(processName string, split config.SplitTUN, route map[string]json.RawMessage) ([]json.RawMessage, error) {
	if !split.Active() {
		split = config.SplitTUN{}
	}
	outbound := "direct"
	whitelist := split.Whitelist()
	// An empty whitelist still catch-alls to direct. route.final is only
	// needed when some listed entry is actually sent there.
	if whitelist && !split.Empty() {
		final, err := routeFinal(route)
		if err != nil {
			return nil, err
		}
		outbound = final
	}

	var prefix []json.RawMessage
	if len(split.DomainSuffix) > 0 {
		sniff, err := json.Marshal(map[string]any{"action": "sniff"})
		if err != nil {
			return nil, fmt.Errorf("marshal sniff rule: %w", err)
		}
		prefix = append(prefix, sniff)
	}

	// Whitelist matches must run before the private bypass, otherwise a listed
	// private CIDR would always go direct. The catch-all below still sends
	// every other private destination direct.
	if !whitelist {
		private, err := directRule(map[string]any{"ip_is_private": true})
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, private)
	}
	self, err := directRule(map[string]any{"process_name": []string{processName}})
	if err != nil {
		return nil, err
	}
	prefix = append(prefix, self)

	lists := []struct {
		key   string
		items []string
	}{
		{"process_name", split.ProcessName},
		{"process_path", split.ProcessPath},
		{"ip_cidr", split.IPCIDR},
		{"domain_suffix", split.DomainSuffix},
	}
	for _, list := range lists {
		if len(list.items) == 0 {
			continue
		}
		rule, err := routeRule(map[string]any{list.key: list.items}, outbound)
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, rule)
	}
	if whitelist {
		catchAll, err := directRule(map[string]any{"ip_cidr": []string{"0.0.0.0/0", "::/0"}})
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, catchAll)
	}
	return prefix, nil
}

func routeFinal(route map[string]json.RawMessage) (string, error) {
	raw, ok := route["final"]
	if !ok {
		return "", fmt.Errorf("split-tun whitelist: sing-box config has no route.final")
	}
	var tag string
	if err := json.Unmarshal(raw, &tag); err != nil || strings.TrimSpace(tag) == "" {
		return "", fmt.Errorf("split-tun whitelist: route.final is empty")
	}
	if tag == "direct" {
		return "", fmt.Errorf("split-tun whitelist: route.final is %q", tag)
	}
	return tag, nil
}

func excludeCIDRs(s config.SplitTUN) bool {
	return len(s.IPCIDR) > 0 && s.Active() && !s.Whitelist()
}

func whitelistCIDRs(s config.SplitTUN) bool {
	return len(s.IPCIDR) > 0 && s.Whitelist()
}

// dropOverlappingCIDRs removes exclude prefixes that contain, or are contained
// in, a whitelist prefix. Otherwise the default private excludes would drop a
// listed LAN address before the route rule could send it to the proxy.
func dropOverlappingCIDRs(exclude, allow []string) []string {
	nets := parseCIDRNets(allow)
	if len(nets) == 0 {
		return exclude
	}
	out := make([]string, 0, len(exclude))
	for _, raw := range exclude {
		_, n, err := net.ParseCIDR(raw)
		if err != nil || !overlapsAny(n, nets) {
			out = append(out, raw)
		}
	}
	return out
}

func parseCIDRNets(cidrs []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, raw := range cidrs {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			continue
		}
		nets = append(nets, n)
	}
	return nets
}

func overlapsAny(n *net.IPNet, allow []*net.IPNet) bool {
	for _, a := range allow {
		if n.Contains(a.IP) || a.Contains(n.IP) {
			return true
		}
	}
	return false
}

func directRule(match map[string]any) (json.RawMessage, error) {
	return routeRule(match, "direct")
}

func routeRule(match map[string]any, outbound string) (json.RawMessage, error) {
	match["action"] = "route"
	match["outbound"] = outbound
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

// dropOverlappingExcludes removes route_exclude_address prefixes that overlap
// allow on each tun inbound. Other inbound fields are left as they were.
func dropOverlappingExcludes(inbounds []json.RawMessage, allow []string) ([]json.RawMessage, error) {
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
		kept, err := json.Marshal(dropOverlappingCIDRs(existing, allow))
		if err != nil {
			return nil, fmt.Errorf("marshal route_exclude_address: %w", err)
		}
		inbound["route_exclude_address"] = kept
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
