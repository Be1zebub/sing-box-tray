package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDest(t *testing.T) {
	if got := defaultDest(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
	got := defaultDest(`C:\apps\configs`)
	if got != `C:\apps\configs\` {
		t.Fatalf("got %q", got)
	}
	if got := defaultDest(`C:\apps\configs\`); got != `C:\apps\configs\` {
		t.Fatalf("already slashed = %q", got)
	}
}

func TestWriteSavedNote(t *testing.T) {
	if err := writeSavedNote("", "anywhere"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	note := filepath.Join(dir, "importer-saved.txt")
	dest := filepath.Join(dir, "a.json")
	if err := writeSavedNote(note, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(note)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != abs {
		t.Fatalf("note = %q", data)
	}
}
