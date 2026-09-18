//go:build windows

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

type experimentalOut struct {
	Experimental map[string]json.RawMessage `json:"experimental"`
}

func readExperimental(t *testing.T, path string) experimentalOut {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out experimentalOut
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return out
}

// TestApplyClashAPIDisabled checks the controller is stripped while every other
// experimental entry survives.
func TestApplyClashAPIDisabled(t *testing.T) {
	p := writeTempConfig(t, `{
  "outbounds": [{"type": "direct", "tag": "direct"}],
  "experimental": {
    "clash_api": {"external_controller": "127.0.0.1:9090"},
    "cache_file": {"enabled": true, "path": "x.db"}
  }
}`)

	if err := ApplyClashAPI(p, ClashAPIConfig{Enabled: false}); err != nil {
		t.Fatalf("ApplyClashAPI: %v", err)
	}

	exp := readExperimental(t, p).Experimental
	if _, ok := exp["clash_api"]; ok {
		t.Error("clash_api must be removed when disabled")
	}
	if _, ok := exp["cache_file"]; !ok {
		t.Error("cache_file must be preserved")
	}
}

// TestApplyClashAPIEnabled checks the injected block reflects tray-config and
// that yacd only appears when asked for.
func TestApplyClashAPIEnabled(t *testing.T) {
	cases := []struct {
		name       string
		cfg        ClashAPIConfig
		wantUI     bool
		wantSecret bool
	}{
		{"api only", ClashAPIConfig{Enabled: true, Listen: "127.0.0.1:9090"}, false, false},
		{"with yacd", ClashAPIConfig{Enabled: true, Listen: "127.0.0.1:9091", Yacd: true}, true, false},
		{"with secret", ClashAPIConfig{Enabled: true, Listen: "127.0.0.1:9090", Secret: "s3cr3t"}, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTempConfig(t, `{
  "outbounds": [{"type": "direct", "tag": "direct"}],
  "experimental": {"cache_file": {"enabled": true}}
}`)

			if err := ApplyClashAPI(p, tc.cfg); err != nil {
				t.Fatalf("ApplyClashAPI: %v", err)
			}

			exp := readExperimental(t, p).Experimental
			raw, ok := exp["clash_api"]
			if !ok {
				t.Fatal("clash_api missing")
			}
			var block map[string]any
			if err := json.Unmarshal(raw, &block); err != nil {
				t.Fatalf("parse clash_api: %v", err)
			}
			if block["external_controller"] != tc.cfg.Listen {
				t.Errorf("external_controller = %v, want %q", block["external_controller"], tc.cfg.Listen)
			}
			if _, ok := block["external_ui"]; ok != tc.wantUI {
				t.Errorf("external_ui present = %v, want %v", ok, tc.wantUI)
			}
			if _, ok := block["secret"]; ok != tc.wantSecret {
				t.Errorf("secret present = %v, want %v", ok, tc.wantSecret)
			}
			if _, ok := exp["cache_file"]; !ok {
				t.Error("cache_file must be preserved")
			}
		})
	}
}

// TestCopyWithClashAPIDoesNotTouchSource is the Off-mode guarantee: the user's
// config must come back byte-identical.
func TestCopyWithClashAPIDoesNotTouchSource(t *testing.T) {
	body := `{"outbounds":[{"type":"direct","tag":"direct"}],"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090"}}}`
	p := writeTempConfig(t, body)

	tmp, err := CopyWithClashAPI(p, ClashAPIConfig{Enabled: false})
	if err != nil {
		t.Fatalf("CopyWithClashAPI: %v", err)
	}
	defer os.Remove(tmp)

	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(after) != body {
		t.Error("source config was modified")
	}

	exp := readExperimental(t, tmp).Experimental
	if _, ok := exp["clash_api"]; ok {
		t.Error("copy must have clash_api removed")
	}
}

// TestHasKey pins the absent-vs-false distinction the clash_api migration uses.
func TestHasKey(t *testing.T) {
	if hasKey([]byte(`{"a":1}`), "clash_api") {
		t.Error("absent key reported present")
	}
	if !hasKey([]byte(`{"clash_api":{"enabled":false}}`), "clash_api") {
		t.Error("present key reported absent")
	}
	if hasKey([]byte(`not json`), "clash_api") {
		t.Error("invalid JSON should report absent")
	}
}

func TestEnsureClashSecret(t *testing.T) {
	cases := []struct {
		name        string
		cfg         ClashAPIConfig
		wantChanged bool
		wantSecret  string // "" means "must stay empty"
	}{
		{"disabled, empty", ClashAPIConfig{Enabled: false}, false, ""},
		{"enabled, empty", ClashAPIConfig{Enabled: true}, true, "<generated>"},
		{"enabled, already set", ClashAPIConfig{Enabled: true, Secret: "keepme"}, false, "keepme"},
		{"disabled, already set", ClashAPIConfig{Enabled: false, Secret: "keepme"}, false, "keepme"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := TrayConfig{ClashAPI: tc.cfg}
			changed, err := cfg.EnsureClashSecret()
			if err != nil {
				t.Fatalf("EnsureClashSecret: %v", err)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}

			switch tc.wantSecret {
			case "<generated>":
				if len(cfg.ClashAPI.Secret) != 32 {
					t.Errorf("secret = %q, want 32 hex chars", cfg.ClashAPI.Secret)
				}
			default:
				if cfg.ClashAPI.Secret != tc.wantSecret {
					t.Errorf("secret = %q, want %q", cfg.ClashAPI.Secret, tc.wantSecret)
				}
			}
		})
	}
}

// TestEnsureClashSecretIsRandom guards against a constant or predictable value.
func TestEnsureClashSecretIsRandom(t *testing.T) {
	a := TrayConfig{ClashAPI: ClashAPIConfig{Enabled: true}}
	b := TrayConfig{ClashAPI: ClashAPIConfig{Enabled: true}}
	if _, err := a.EnsureClashSecret(); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := b.EnsureClashSecret(); err != nil {
		t.Fatalf("second: %v", err)
	}
	if a.ClashAPI.Secret == b.ClashAPI.Secret {
		t.Error("two generations produced the same secret")
	}
}

// TestLoadPersistsGeneratedSecret checks the first-run behaviour: the config
// written to disk carries the secret, so the injected clash_api block and any
// other client agree on it across restarts.
func TestLoadPersistsGeneratedSecret(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.ClashAPI.Enabled {
		t.Fatal("default config should have the clash api enabled")
	}
	if len(cfg.ClashAPI.Secret) != 32 {
		t.Fatalf("in-memory secret = %q", cfg.ClashAPI.Secret)
	}

	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.ClashAPI.Secret != cfg.ClashAPI.Secret {
		t.Errorf("secret not persisted: %q -> %q", cfg.ClashAPI.Secret, reloaded.ClashAPI.Secret)
	}
}
