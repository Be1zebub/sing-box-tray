// Package assets embeds static resources (default config, tray icons,
// locale strings) built into the tray executable.
package assets

import _ "embed"

//go:embed tray-config.default.json
var DefaultTrayConfig []byte

//go:embed split-tun.default.json
var DefaultSplitTUN []byte
