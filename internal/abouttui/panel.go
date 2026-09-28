//go:build windows

package abouttui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Be1zebub/sing-box-tray/internal/config"
	"github.com/Be1zebub/sing-box-tray/internal/version"
)

// Env carries the live tray snapshot into the About process when it is
// opened in conhost. The Windows Terminal path passes the same JSON as a
// base64 argument, because wt does not forward the launcher's environment.
const Env = "SING_BOX_TRAY_ABOUT"

// Snapshot is the runtime line the tray already shows in the tooltip.
type Snapshot struct {
	Mode   string `json:"mode"`
	Proxy  string `json:"proxy"`
	Via    string `json:"via"`
	Ping   int    `json:"ping"`
	Config string `json:"config"`
}

type panel struct {
	lines []string
}

func readSnapshot() (Snapshot, bool) {
	snap := Snapshot{Ping: -1, Mode: "Off"}
	raw, ok := snapshotRaw(os.Args)
	if !ok {
		return snap, false
	}
	if json.Unmarshal([]byte(raw), &snap) != nil {
		return Snapshot{Ping: -1, Mode: "Off"}, false
	}
	return snap, true
}

// EncodeSnapshot is the argv payload for --about-wt.
func EncodeSnapshot(snap string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(snap))
}

func snapshotRaw(args []string) (string, bool) {
	if len(args) > 2 && (args[1] == Flag || args[1] == FlagWT) {
		b, err := base64.RawURLEncoding.DecodeString(args[2])
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	if raw := os.Getenv(Env); raw != "" {
		return raw, true
	}
	return "", false
}

func loadPanelFast() panel {
	snap, _ := readSnapshot()
	selected := snap.Config
	if selected == "" {
		selected = "…"
	}
	return renderPanel(snap, "…", "…", selected, "")
}

func loadPanel() panel {
	snap, explicit := readSnapshot()

	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	cfg, err := config.Load(exeDir)
	singBox, wintun, selected, configPath := "—", "—", snap.Config, ""
	if err == nil {
		singBox = singBoxVersion(cfg.SingBoxPath)
		wintun = dllVersion(cfg.WintunDllPath)
		if selected == "" {
			selected = cfg.SelectedConfig
		}
		if cfg.ConfigDir != "" && selected != "" && selected != "—" {
			configPath = filepath.Join(cfg.ConfigDir, selected)
		}
		if !explicit && cfg.DefaultMode != "" {
			snap.Mode = cfg.DefaultMode
		}
	}
	if selected == "" {
		selected = "—"
	}

	return renderPanel(snap, singBox, wintun, selected, configPath)
}

func renderPanel(snap Snapshot, singBox, wintun, selected, configPath string) panel {
	if selected == "" {
		selected = "—"
	}
	proxy := snap.Proxy
	if proxy == "" {
		proxy = "—"
	} else if snap.Via != "" && snap.Via != proxy {
		proxy = proxy + " | " + snap.Via
	}
	ping := "—"
	if snap.Ping >= 0 {
		ping = fmt.Sprintf("%dms", snap.Ping)
	}

	repo := repoURL
	configVal := selected
	if selected != "—" && selected != "…" {
		if configPath != "" {
			configVal = osc8(fileURL(configPath), filepath.Base(configPath))
		}
	}
	if repoURL != "" {
		repo = osc8(repoURL, repoURL)
	}

	rows := [][2]string{
		{"version", version.Version},
		{"repo", repo},
		{"sing-box", singBox},
		{"wintun", wintun},
		{"config", configVal},
		{"mode", snap.Mode},
		{"proxy", proxy},
		{"ping", ping},
	}

	const labelW = 8
	lines := []string{
		styleTitle(appName),
		styleDim(strings.Repeat("─", 36)),
	}
	for _, row := range rows {
		label := row[0]
		if len(label) < labelW {
			label += strings.Repeat(" ", labelW-len(label))
		}
		lines = append(lines, styleLabel(label)+"  "+styleValue(row[1]))
	}
	lines = append(lines, "", styleDim("any key to close"))
	return panel{lines: lines}
}

func singBoxVersion(path string) string {
	if path == "" {
		return "—"
	}
	if _, err := os.Stat(path); err != nil {
		return "missing"
	}
	cmd := exec.Command(path, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		return filepath.Base(path)
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	line = strings.TrimPrefix(line, "sing-box version ")
	if line == "" {
		return "—"
	}
	return line
}

func dllVersion(path string) string {
	if path == "" {
		return "—"
	}
	if _, err := os.Stat(path); err != nil {
		return "missing"
	}
	if v := fileVersion(path); v != "" {
		return v
	}
	return "present"
}

func fileVersion(path string) string {
	versionDLL := windows.NewLazySystemDLL("version.dll")
	sizeProc := versionDLL.NewProc("GetFileVersionInfoSizeW")
	infoProc := versionDLL.NewProc("GetFileVersionInfoW")
	queryProc := versionDLL.NewProc("VerQueryValueW")

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	size, _, _ := sizeProc.Call(uintptr(unsafe.Pointer(p)), 0)
	if size == 0 {
		return ""
	}
	buf := make([]byte, size)
	r, _, _ := infoProc.Call(uintptr(unsafe.Pointer(p)), 0, size, uintptr(unsafe.Pointer(&buf[0])))
	if r == 0 {
		return ""
	}
	type trans struct{ Lang, CP uint16 }
	sub, _ := windows.UTF16PtrFromString(`\VarFileInfo\Translation`)
	var block uintptr
	var n uint32
	r, _, _ = queryProc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(sub)), uintptr(unsafe.Pointer(&block)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || n < 4 || block == 0 {
		return ""
	}
	tr := (*trans)(unsafe.Pointer(block))
	key := fmt.Sprintf(`\StringFileInfo\%04x%04x\FileVersion`, tr.Lang, tr.CP)
	sub, _ = windows.UTF16PtrFromString(key)
	block = 0
	n = 0
	r, _, _ = queryProc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(sub)), uintptr(unsafe.Pointer(&block)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || block == 0 {
		return ""
	}
	return strings.TrimSpace(windows.UTF16PtrToString((*uint16)(unsafe.Pointer(block))))
}

func styleTitle(s string) string { return "\x1b[1;38;2;120;230;90m" + s + "\x1b[0m" }
func styleDim(s string) string   { return "\x1b[38;2;120;110;130m" + s + "\x1b[0m" }
func styleLabel(s string) string { return "\x1b[38;2;180;160;190m" + s + "\x1b[0m" }
func styleValue(s string) string { return "\x1b[38;2;235;230;220m" + s + "\x1b[0m" }

// osc8 marks text as a clickable hyperlink (OSC 8). Windows Terminal opens it
// on Ctrl+Click. An empty url leaves the text plain.
func osc8(rawURL, text string) string {
	if rawURL == "" || text == "" {
		return text
	}
	return "\x1b]8;;" + rawURL + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func fileURL(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
