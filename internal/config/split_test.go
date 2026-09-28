//go:build windows

package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadSplitTUNMissingWritesEmptyTemplate(t *testing.T) {
	dir := t.TempDir()

	got, err := LoadSplitTUN(dir)
	if err != nil {
		t.Fatalf("LoadSplitTUN: %v", err)
	}
	if !got.Empty() {
		t.Fatalf("missing file should load as empty, got %+v", got)
	}
	if !got.Enabled || got.Mode != SplitBlacklist {
		t.Fatalf("default mode = enabled:%v mode:%s", got.Enabled, got.Mode)
	}
	if _, err := os.Stat(filepath.Join(dir, splitTunFile)); err != nil {
		t.Fatalf("default split-tun.json was not written: %v", err)
	}
}

func TestLoadSplitTUNNormalizesEntries(t *testing.T) {
	dir := t.TempDir()
	body := `{
	  "ip_cidr": ["8.8.8.8", "1.2.3.4/32", "1.2.3.4/32", "  "],
	  "domain_suffix": ["*.Example.COM", "example.com", ""],
	  "process_name": ["Discord.exe", "discord.exe"],
	  "process_path": ["C:\\Games\\Game.exe", " C:\\Games\\Game.exe "]
	}`
	if err := os.WriteFile(filepath.Join(dir, splitTunFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := LoadSplitTUN(dir)
	if err != nil {
		t.Fatalf("LoadSplitTUN: %v", err)
	}
	if !slices.Equal(got.IPCIDR, []string{"8.8.8.8/32", "1.2.3.4/32"}) {
		t.Errorf("ip_cidr = %v", got.IPCIDR)
	}
	if !slices.Equal(got.DomainSuffix, []string{"example.com"}) {
		t.Errorf("domain_suffix = %v", got.DomainSuffix)
	}
	if !slices.Equal(got.ProcessName, []string{"Discord.exe", "discord.exe"}) {
		t.Errorf("process_name = %v", got.ProcessName)
	}
	if !slices.Equal(got.ProcessPath, []string{`C:\Games\Game.exe`}) {
		t.Errorf("process_path = %v", got.ProcessPath)
	}
	if !got.Enabled || got.Mode != SplitBlacklist {
		t.Errorf("enabled=%v mode=%s", got.Enabled, got.Mode)
	}
}

func TestLoadSplitTUNMode(t *testing.T) {
	dir := t.TempDir()
	body := `{
	  "enabled": true,
	  "mode": "Whitelist",
	  "process_name": ["chrome.exe"],
	  "_example": {"mode": "blacklist", "process_name": ["ignored.exe"]}
	}`
	if err := os.WriteFile(filepath.Join(dir, splitTunFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadSplitTUN(dir)
	if err != nil {
		t.Fatalf("LoadSplitTUN: %v", err)
	}
	if got.Mode != SplitWhitelist || !got.Enabled {
		t.Fatalf("mode = %s enabled = %v", got.Mode, got.Enabled)
	}
	if !slices.Equal(got.ProcessName, []string{"chrome.exe"}) {
		t.Fatalf("process_name = %v", got.ProcessName)
	}

	off := `{"enabled": false, "mode": "blacklist", "ip_cidr": ["1.1.1.1"]}`
	if err := os.WriteFile(filepath.Join(dir, splitTunFile), []byte(off), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err = LoadSplitTUN(dir)
	if err != nil {
		t.Fatalf("LoadSplitTUN: %v", err)
	}
	if got.Active() {
		t.Fatal("enabled false should be inactive")
	}
	if !slices.Equal(got.IPCIDR, []string{"1.1.1.1/32"}) {
		t.Fatalf("disabled file should still parse lists, got %v", got.IPCIDR)
	}
}

func TestLoadSplitTUNRejectsBadEntries(t *testing.T) {
	cases := []string{
		`{"ip_cidr":["not-an-ip"]}`,
		`{"domain_suffix":["https://example.com/a"]}`,
		`{"mode":"bypass"}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, splitTunFile), []byte(body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := LoadSplitTUN(dir); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestListConfigFilesSkipsSplitTUN(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"config.json", "tray-config.json", "split-tun.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested.json"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := ListConfigFiles(dir)
	if err != nil {
		t.Fatalf("ListConfigFiles: %v", err)
	}
	if !slices.Equal(got, []string{"config.json"}) {
		t.Fatalf("names = %v", got)
	}
}
