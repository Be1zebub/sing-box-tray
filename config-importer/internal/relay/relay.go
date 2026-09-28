// Package relay rewrites outbound server addresses to point at an L4 relay.
package relay

import "github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"

// Apply replaces the "server" of every concrete outbound with relay.
// Ports, SNI, keys and credentials are left untouched: the relay forwards the
// same ports, and TLS/REALITY still need the real server name.
// An empty relay is a no-op (traffic goes out directly).
func Apply(cfg *singbox.Config, relay string) int {
	if relay == "" {
		return 0
	}
	n := 0
	for _, ob := range cfg.Outbounds() {
		if !singbox.IsConcrete(singbox.Type(ob)) {
			continue
		}
		if _, ok := ob["server"]; !ok {
			continue
		}
		ob["server"] = relay
		n++
	}
	return n
}
