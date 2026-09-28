// Package remnawave fetches configs from a Remnawave subscription endpoint.
//
// Two response shapes matter:
//   - User-Agent "singbox" -> a ready sing-box JSON config (no hysteria2: the
//     backend hard-excludes it from sing-box/mihomo templates).
//   - User-Agent "Happ/..." -> a JSON array of full Xray configs, one per host.
//     This is the only response that carries hysteria2.
package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

// User agents that select the matching Remnawave subscription template.
const (
	UASingbox = "singbox"
	UAHapp    = "Happ/1.0"
)

// DefaultHTTPClient is used when the caller passes nil.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// Fetch performs a GET with the given User-Agent.
func Fetch(ctx context.Context, client *http.Client, url, ua string) ([]byte, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

// SingboxConfig fetches the ready-made sing-box config.
func SingboxConfig(ctx context.Context, client *http.Client, url string) (*singbox.Config, error) {
	body, err := Fetch(ctx, client, url, UASingbox)
	if err != nil {
		return nil, err
	}
	cfg, err := singbox.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("sing-box response is not a JSON config (check the subscription URL): %w", err)
	}
	return cfg, nil
}

// HysteriaOutbounds fetches the Happ response and converts every hysteria
// inbound host into a sing-box hysteria2 outbound.
func HysteriaOutbounds(ctx context.Context, client *http.Client, url string) ([]singbox.Obj, error) {
	body, err := Fetch(ctx, client, url, UAHapp)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var hosts []singbox.Obj
	if err := dec.Decode(&hosts); err != nil {
		return nil, fmt.Errorf("Happ response is not a JSON array: %w", err)
	}

	var out []singbox.Obj
	for _, host := range hosts {
		if ob := hysteriaOutbound(host); ob != nil {
			out = append(out, ob)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no hysteria host found in Happ response")
	}
	return out, nil
}

// hysteriaOutbound maps one Happ entry (a full Xray config) to a sing-box
// hysteria2 outbound, or nil when the entry has no hysteria proxy.
func hysteriaOutbound(host singbox.Obj) singbox.Obj {
	proxy := findProxy(host, "hysteria")
	if proxy == nil {
		return nil
	}
	settings, _ := proxy["settings"].(singbox.Obj)
	stream, _ := proxy["streamSettings"].(singbox.Obj)
	hy, _ := stream["hysteriaSettings"].(singbox.Obj)
	tlsSettings, _ := stream["tlsSettings"].(singbox.Obj)

	auth := singbox.Str(hy, "auth")
	if auth == "" {
		return nil
	}
	ob := singbox.Obj{
		"type":        "hysteria2",
		"tag":         singbox.Str(host, "remarks"),
		"server":      singbox.Str(settings, "address"),
		"server_port": settings["port"],
		"password":    auth,
	}

	tls := singbox.Obj{"enabled": true}
	if sn := singbox.Str(tlsSettings, "serverName"); sn != "" {
		tls["server_name"] = sn
	}
	if alpn, ok := tlsSettings["alpn"].([]any); ok && len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	// No uTLS here: sing-box's hysteria2 (QUIC) rejects tls.utls with
	// "unsupported usage for uTLS". The Happ fingerprint is TCP-only.
	ob["tls"] = tls
	return ob
}

// findProxy returns the first outbound of the given protocol in a Happ entry.
func findProxy(host singbox.Obj, protocol string) singbox.Obj {
	raw, ok := host["outbounds"].([]any)
	if !ok {
		return nil
	}
	for _, it := range raw {
		m, ok := it.(singbox.Obj)
		if !ok {
			continue
		}
		if singbox.Str(m, "protocol") == protocol {
			return m
		}
	}
	return nil
}

func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
