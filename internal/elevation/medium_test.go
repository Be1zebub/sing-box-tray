//go:build windows

package elevation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartDropAdminNotElevated(t *testing.T) {
	if IsElevated() {
		t.Skip("shell is elevated")
	}
	out := filepath.Join(t.TempDir(), "ok.txt")
	line := `cmd.exe /c echo hi>"` + out + `"`
	elevated, err := StartDropAdmin(line)
	if err != nil {
		t.Fatal(err)
	}
	if elevated {
		t.Fatal("non-elevated launch reported admin")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}
