package main

import (
	"os"
	"path/filepath"
	"strings"
)

// defaultDest is the destination field prefilled when the tray passes
// --config-dir. A trailing separator leaves the cursor ready for a file name.
func defaultDest(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, `\`) {
		return dir
	}
	return dir + string(os.PathSeparator)
}

// writeSavedNote records the saved config path for the tray. An empty note
// path means the importer was not launched from the tray.
func writeSavedNote(note, dest string) error {
	if strings.TrimSpace(note) == "" {
		return nil
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	return os.WriteFile(note, []byte(abs+"\n"), 0o644)
}
