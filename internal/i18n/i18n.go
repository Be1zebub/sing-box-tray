//go:build windows

package i18n

import (
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/Be1zebub/sing-box-tray/assets"
)

type Lang string

const (
	EN Lang = "en"
	RU Lang = "ru"
	UA Lang = "ua"
)

// Strings holds every user-facing UI string (tray menu, dialogs, toast
// notifications, log window title). Log messages are deliberately
// not covered here — they stay in English regardless of UI language.
type Strings struct {
	MenuStart              string `json:"menu_start"`
	MenuStartTip           string `json:"menu_start_tip"`
	MenuStop               string `json:"menu_stop"`
	MenuStopTip            string `json:"menu_stop_tip"`
	MenuRestart            string `json:"menu_restart"`
	MenuRestartTip         string `json:"menu_restart_tip"`
	MenuMode               string `json:"menu_mode"`
	ModeOff                string `json:"mode_off"`
	ModeSystemProxy        string `json:"mode_system_proxy"`
	ModeTUN                string `json:"mode_tun"`
	MenuConfig             string `json:"menu_config"`
	MenuOpenConfig         string `json:"menu_open_config"`
	MenuOpenTrayConfig     string `json:"menu_open_tray_config"`
	MenuOpenSplitTUN       string `json:"menu_open_split_tun"`
	MenuOpenSingboxConfigs string `json:"menu_open_singbox_configs"`
	MenuOpenImporter       string `json:"menu_open_importer"`
	MenuProxy              string `json:"menu_proxy"`
	MenuOpenYacd           string `json:"menu_open_yacd"`
	MenuProxyTest          string `json:"menu_proxy_test"`
	MenuProxyTesting       string `json:"menu_proxy_testing"`
	MenuAutostart          string `json:"menu_autostart"`
	MenuAutostartTip       string `json:"menu_autostart_tip"`
	MenuViewLogs           string `json:"menu_view_logs"`
	MenuAbout              string `json:"menu_about"`
	MenuExit               string `json:"menu_exit"`

	TooltipStopped string `json:"tooltip_stopped"`

	StatusRunning  string `json:"status_running"`
	StatusStopped  string `json:"status_stopped"`
	StatusCrashed  string `json:"status_crashed"`
	StatusStarting string `json:"status_starting"`
	StatusStopping string `json:"status_stopping"`

	ToastCrashedTitle string `json:"toast_crashed_title"`
	ToastCrashedMsg   string `json:"toast_crashed_msg"`

	DialogConfigChangedFmt   string `json:"dialog_config_changed_fmt"`
	DialogErrorFmt           string `json:"dialog_error_fmt"`
	DialogMissingSingBoxFmt  string `json:"dialog_missing_sing_box_fmt"`
	DialogMissingWintunFmt   string `json:"dialog_missing_wintun_fmt"`
	DialogMissingConfigFmt   string `json:"dialog_missing_config_fmt"`
	DialogMissingImporterFmt string `json:"dialog_missing_importer_fmt"`
	DialogImportSavedFmt     string `json:"dialog_import_saved_fmt"`
	DialogYacdNotRunning     string `json:"dialog_yacd_not_running"`

	LogWindowTitle string `json:"log_window_title"`

	StartupErrMutexFmt   string `json:"startup_err_mutex_fmt"`
	StartupErrExePathFmt string `json:"startup_err_exe_path_fmt"`
	StartupErrConfigFmt  string `json:"startup_err_config_fmt"`
	StartupErrElevateFmt string `json:"startup_err_elevate_fmt"`
}

var catalog map[Lang]Strings

func init() {
	catalog = make(map[Lang]Strings, 3)
	for lang, file := range map[Lang]string{EN: "en", RU: "ru", UA: "ua"} {
		data, err := assets.LocaleFS.ReadFile("locales/" + file + ".json")
		if err != nil {
			panic(fmt.Sprintf("i18n: missing locale file %s.json: %s", file, err))
		}
		var s Strings
		if err := json.Unmarshal(data, &s); err != nil {
			panic(fmt.Sprintf("i18n: invalid locale file %s.json: %s", file, err))
		}
		catalog[lang] = s
	}
}

// Resolve maps a tray-config.json "language" value to a Lang, falling back
// to OS auto-detection for "auto", empty, or unrecognized values.
func Resolve(configValue string) Lang {
	switch strings.ToLower(configValue) {
	case "en":
		return EN
	case "ru":
		return RU
	case "ua":
		return UA
	default:
		return Detect()
	}
}

// Detect maps the Windows UI language to a supported Lang, defaulting to EN.
func Detect() Lang {
	switch getUserDefaultUILanguage() & 0x3FF { // primary language ID
	case 0x19: // LANG_RUSSIAN
		return RU
	case 0x22: // LANG_UKRAINIAN
		return UA
	default:
		return EN
	}
}

// Get returns the string catalog for lang, falling back to English.
func Get(lang Lang) Strings {
	if s, ok := catalog[lang]; ok {
		return s
	}
	return catalog[EN]
}

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")
)

func getUserDefaultUILanguage() uint16 {
	ret, _, _ := procGetUserDefaultUILanguage.Call()
	return uint16(ret)
}
