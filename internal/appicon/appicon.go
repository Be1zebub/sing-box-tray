//go:build windows

// Package appicon provides the shared window icon for the tray's lxn/walk
// windows (Settings, Log, About) — distinct from the tray's own state icons
// in assets/icons.go, which are only used for the systray itself.
package appicon

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/lxn/walk"

	"github.com/Be1zebub/sing-box-tray/assets"
)

var (
	once sync.Once
	icon *walk.Icon
)

// Icon returns the shared window icon, or nil if it could not be loaded.
// walk.NewIconFromFile reloads from disk per DPI, so the backing file is
// written to os.TempDir() (not a file that gets removed) to persist for
// the process lifetime, and loaded once via sync.Once since the same
// *walk.Icon can be shared across multiple windows.
func Icon() *walk.Icon {
	once.Do(func() {
		path := filepath.Join(os.TempDir(), "sing-box-tray-window.ico")
		if err := os.WriteFile(path, assets.IconGrey, 0o644); err != nil {
			return
		}
		icon, _ = walk.NewIconFromFile(path)
	})
	return icon
}
