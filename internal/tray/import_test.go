//go:build windows

package tray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportedConfigName(t *testing.T) {
	dir := filepath.Join(`C:\apps`, "configs")
	name, ok := importedConfigName(dir, filepath.Join(dir, "a.json"))
	if !ok || name != "a.json" {
		t.Fatalf("got %q %v", name, ok)
	}
	if _, ok := importedConfigName(dir, filepath.Join(dir, "sub", "a.json")); ok {
		t.Fatal("nested file must not prompt")
	}
	if _, ok := importedConfigName(dir, `D:\other\a.json`); ok {
		t.Fatal("outside dir must not prompt")
	}
	if _, ok := importedConfigName(dir, filepath.Join(dir, "notes.txt")); ok {
		t.Fatal("non-json must not prompt")
	}
	if _, ok := importedConfigName(dir, "  \n"); ok {
		t.Fatal("empty note must not prompt")
	}
}

func TestInstallYacdBoot(t *testing.T) {
	dir := t.TempDir()
	if err := installYacdBoot(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, yacdBootFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "yacd.metacubex.one") {
		t.Fatal("boot page must write YACD's localStorage key")
	}
}
