package relay

import (
	"testing"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

func TestApplyRewritesConcreteOnly(t *testing.T) {
	cfg := singbox.FromRoot(singbox.Obj{
		"outbounds": []any{
			singbox.Obj{"type": "vless", "tag": "a", "server": "1.2.3.4", "server_port": 443},
			singbox.Obj{"type": "selector", "tag": "s", "outbounds": []any{"a"}},
			singbox.Obj{"type": "direct", "tag": "direct"},
		},
	})

	if n := Apply(cfg, "9.9.9.9"); n != 1 {
		t.Fatalf("Apply rewrote %d outbounds, want 1", n)
	}
	obs := cfg.Outbounds()
	if got := singbox.Str(obs[0], "server"); got != "9.9.9.9" {
		t.Errorf("concrete server = %q, want 9.9.9.9", got)
	}
	if _, ok := obs[1]["server"]; ok {
		t.Errorf("group must not be rewritten")
	}
}

func TestApplyEmptyIsNoop(t *testing.T) {
	cfg := singbox.FromRoot(singbox.Obj{
		"outbounds": []any{singbox.Obj{"type": "vless", "tag": "a", "server": "1.2.3.4"}},
	})
	if n := Apply(cfg, ""); n != 0 {
		t.Fatalf("Apply with empty relay rewrote %d outbounds, want 0", n)
	}
	if got := singbox.Str(cfg.Outbounds()[0], "server"); got != "1.2.3.4" {
		t.Errorf("server = %q, want unchanged", got)
	}
}
