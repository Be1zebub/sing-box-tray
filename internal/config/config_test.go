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
