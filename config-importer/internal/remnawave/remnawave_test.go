package remnawave

import (
	"encoding/json"
	"testing"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

func TestHysteriaOutboundMapping(t *testing.T) {
	host := singbox.Obj{
		"remarks": "Hy2",
		"outbounds": []any{
			singbox.Obj{"tag": "direct", "protocol": "freedom"},
			singbox.Obj{
				"tag":      "proxy",
				"protocol": "hysteria",
				"settings": singbox.Obj{
					"address": "hy2.example.com",
					"port":    json.Number("5443"),
					"version": json.Number("2"),
				},
				"streamSettings": singbox.Obj{
					"network":          "hysteria",
					"hysteriaSettings": singbox.Obj{"version": json.Number("2"), "auth": "uuid-123"},
					"security":         "tls",
					"tlsSettings": singbox.Obj{
						"serverName":  "hy2.example.com",
						"fingerprint": "chrome",
						"alpn":        []any{"h3"},
					},
				},
			},
		},
	}

	ob := hysteriaOutbound(host)
	if ob == nil {
		t.Fatal("hysteriaOutbound returned nil")
	}
	if got := singbox.Type(ob); got != "hysteria2" {
		t.Errorf("type = %q, want hysteria2", got)
	}
	if got := singbox.Str(ob, "password"); got != "uuid-123" {
		t.Errorf("password = %q, want uuid-123", got)
	}
	if got := singbox.Str(ob, "server"); got != "hy2.example.com" {
		t.Errorf("server = %q", got)
	}
	tls, _ := ob["tls"].(singbox.Obj)
	if tls == nil {
		t.Fatal("tls missing")
	}
	if _, ok := tls["utls"]; ok {
		t.Error("tls.utls must not be set: sing-box hysteria2 rejects uTLS")
	}
	if got := singbox.Str(tls, "server_name"); got != "hy2.example.com" {
		t.Errorf("tls.server_name = %q", got)
	}
}

func TestHysteriaOutboundNoHysteria(t *testing.T) {
	host := singbox.Obj{
		"remarks":   "Trojan",
		"outbounds": []any{singbox.Obj{"tag": "proxy", "protocol": "trojan"}},
	}
	if ob := hysteriaOutbound(host); ob != nil {
		t.Fatalf("expected nil for non-hysteria host, got %v", ob)
	}
}
