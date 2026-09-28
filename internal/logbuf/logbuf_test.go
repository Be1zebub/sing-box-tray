package logbuf

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRollingDropsOldLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sing-box-tray.log")
	w, err := openRolling(path, 120, 60)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 40; i++ {
		if _, err := fmt.Fprintf(w, "line-%02d\n", i); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if int64(len(data)) > 120 {
		t.Fatalf("size = %d, want <= 120\n%s", len(data), data)
	}
	if !bytes.Contains(data, []byte("line-39\n")) {
		t.Fatalf("newest line missing:\n%s", data)
	}
	if bytes.Contains(data, []byte("line-00\n")) {
		t.Fatalf("oldest line kept:\n%s", data)
	}
	if len(data) == 0 || data[0] == '\n' || !bytes.HasPrefix(data, []byte("line-")) {
		t.Fatalf("trim did not land on a line boundary:\n%s", data)
	}
}

func TestRollingTrimsOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sing-box-tray.log")
	var body []byte
	for i := 0; i < 40; i++ {
		body = append(body, []byte(fmt.Sprintf("old-%02d\n", i))...)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := openRolling(path, 80, 40)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if int64(len(data)) > 80 {
		t.Fatalf("size = %d, want <= 80\n%s", len(data), data)
	}
	if !bytes.Contains(data, []byte("old-39\n")) {
		t.Fatalf("newest line missing:\n%s", data)
	}
	if bytes.Contains(data, []byte("old-00\n")) {
		t.Fatalf("oldest line kept:\n%s", data)
	}
}
