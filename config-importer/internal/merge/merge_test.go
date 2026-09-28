package merge

import (
	"testing"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

func TestIntoReplacesByTagAndUnionsGroups(t *testing.T) {
	dest := singbox.FromRoot(singbox.Obj{
		"outbounds": []any{
			singbox.Obj{"type": "vless", "tag": "X", "server": "old"},
			singbox.Obj{"type": "selector", "tag": "G", "outbounds": []any{"X"}},
		},
	})
	src := singbox.FromRoot(singbox.Obj{
		"outbounds": []any{
			singbox.Obj{"type": "vless", "tag": "X", "server": "new"},
			singbox.Obj{"type": "hysteria2", "tag": "H", "server": "h"},
			singbox.Obj{"type": "selector", "tag": "G", "outbounds": []any{"X", "H"}},
		},
	})

	rep := Into(dest, src, PolicyOutbounds)

	if len(rep.Added) != 1 || rep.Added[0] != "H" {
		t.Errorf("added = %v, want [H]", rep.Added)
	}
	if len(rep.Replaced) != 1 || rep.Replaced[0] != "X" {
		t.Errorf("replaced = %v, want [X]", rep.Replaced)
	}

	idx := map[string]singbox.Obj{}
	for _, ob := range dest.Outbounds() {
		idx[singbox.Tag(ob)] = ob
	}
	if got := singbox.Str(idx["X"], "server"); got != "new" {
		t.Errorf("X.server = %q, want new", got)
	}
	if idx["H"] == nil {
		t.Error("H was not appended")
	}
	members := singbox.Members(idx["G"])
	if len(members) != 2 || members[0] != "X" || members[1] != "H" {
		t.Errorf("group G members = %v, want [X H]", members)
	}
	if idx["direct"] == nil {
		t.Error("direct outbound was not ensured")
	}
}

func TestConflictsDetectsDifferingReplaceKeys(t *testing.T) {
	dest := singbox.FromRoot(singbox.Obj{
		"inbounds": []any{singbox.Obj{"type": "tun", "tag": "tun-in"}},
	})
	src := singbox.FromRoot(singbox.Obj{
		"inbounds": []any{singbox.Obj{"type": "tun", "tag": "other"}},
	})

	conflicts := Conflicts(dest, src)
	if len(conflicts) != 1 || conflicts[0].Key != "inbounds" {
		t.Fatalf("conflicts = %v, want [inbounds]", conflicts)
	}
	if len(Into(dest, src, PolicyOutbounds).Conflicts) != 1 {
		t.Error("outbounds policy should still report the conflict")
	}
}

func TestOutboundsPolicySkipsAbsentReplaceKeys(t *testing.T) {
	src := singbox.FromRoot(singbox.Obj{
		"dns":       singbox.Obj{"servers": []any{}},
		"inbounds":  []any{singbox.Obj{"type": "mixed", "tag": "in"}},
		"outbounds": []any{singbox.Obj{"type": "vless", "tag": "N", "server": "h"}},
	})
	dest := singbox.FromRoot(singbox.Obj{
		"outbounds": []any{singbox.Obj{"type": "direct", "tag": "direct"}},
	})
	Into(dest, src, PolicyOutbounds)
	if _, ok := dest.Root()["dns"]; ok {
		t.Error("outbounds policy copied dns")
	}
	if _, ok := dest.Root()["inbounds"]; ok {
		t.Error("outbounds policy copied inbounds")
	}
	found := false
	for _, ob := range dest.Outbounds() {
		if singbox.Tag(ob) == "N" {
			found = true
		}
	}
	if !found {
		t.Error("outbounds policy did not add the imported outbound")
	}

	full := singbox.FromRoot(singbox.Obj{
		"outbounds": []any{singbox.Obj{"type": "direct", "tag": "direct"}},
	})
	Into(full, src, PolicyFull)
	if _, ok := full.Root()["dns"]; !ok {
		t.Error("full policy left dns missing")
	}
	if _, ok := full.Root()["inbounds"]; !ok {
		t.Error("full policy left inbounds missing")
	}
}
