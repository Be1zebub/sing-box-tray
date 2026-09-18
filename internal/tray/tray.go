//go:build windows

package tray

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"github.com/go-toast/toast"
	"golang.org/x/sys/windows"

	"github.com/Be1zebub/sing-box-tray-runner/assets"
	"github.com/Be1zebub/sing-box-tray-runner/internal/aboutwin"
	"github.com/Be1zebub/sing-box-tray-runner/internal/autostart"
	"github.com/Be1zebub/sing-box-tray-runner/internal/config"
	"github.com/Be1zebub/sing-box-tray-runner/internal/elevation"
	"github.com/Be1zebub/sing-box-tray-runner/internal/i18n"
	"github.com/Be1zebub/sing-box-tray-runner/internal/logbuf"
	"github.com/Be1zebub/sing-box-tray-runner/internal/process"
	"github.com/Be1zebub/sing-box-tray-runner/internal/proxy"
	"github.com/Be1zebub/sing-box-tray-runner/internal/settings"
	"github.com/Be1zebub/sing-box-tray-runner/internal/state"
	"github.com/Be1zebub/sing-box-tray-runner/internal/tun"
	"github.com/Be1zebub/sing-box-tray-runner/internal/version"
	"github.com/Be1zebub/sing-box-tray-runner/internal/watcher"
)

const (
	appTitle = "sing-box-tray"

	// repoURL is this fork's own repository, shown in the About window.
	repoURL = "https://github.com/Be1zebub/sing-box-tray-runner"

	// languagesMenuTitle is deliberately not translated — it's the control
	// that changes the language, so it must stay findable regardless of the
	// current UI language. Same reasoning for the language names themselves:
	// each is shown in its own script, not translated.
	languagesMenuTitle = "Languages"
	langLabelAuto      = "Auto"
	langLabelEN        = "English"
	langLabelRU        = "Русский"
	langLabelUA        = "Українська"

	// Proper nouns, kept literal for the same reason as the language names.
	clashAPILabel = "Clash API"
	yacdLabel     = "YACD dashboard"
)

var (
	user32     = windows.NewLazySystemDLL("user32.dll")
	procMsgBox = user32.NewProc("MessageBoxW")
)

type menuItems struct {
	settings  *systray.MenuItem
	start     *systray.MenuItem
	stop      *systray.MenuItem
	restart   *systray.MenuItem
	mode      *systray.MenuItem
	modeOff   *systray.MenuItem
	modeProxy *systray.MenuItem
	modeTUN   *systray.MenuItem

	config      *systray.MenuItem
	configItems []*systray.MenuItem
	configNames []string

	openConfigFile   *systray.MenuItem
	openConfigFolder *systray.MenuItem

	langAuto *systray.MenuItem
	langEN   *systray.MenuItem
	langRU   *systray.MenuItem
	langUA   *systray.MenuItem

	autostart *systray.MenuItem
	clashAPI  *systray.MenuItem
	clashYacd *systray.MenuItem
	viewLogs  *systray.MenuItem
	about     *systray.MenuItem
	quit      *systray.MenuItem
}

// App orchestrates the sing-box process, proxy settings, and tray UI.
type App struct {
	cfg          *config.TrayConfig
	exeDir       string
	st           *state.Manager
	proc         *process.Manager
	logBuf       *logbuf.Buffer
	logFile      *os.File
	releaseMutex func()
	strs         i18n.Strings

	mu               sync.Mutex
	items            menuItems
	tempCfg          string
	pendingStart     bool
	configWatcher    *watcher.Watcher
	configDirWatcher *watcher.DirWatcher
}

func NewApp(cfg *config.TrayConfig, exeDir string, initialMode state.ProxyMode, releaseMutex func(), strs i18n.Strings) *App {
	a := &App{
		cfg:          cfg,
		exeDir:       exeDir,
		st:           state.NewManager(initialMode),
		logBuf:       logbuf.New(cfg.LogLines),
		releaseMutex: releaseMutex,
		strs:         strs,
	}

	logPath := filepath.Join(exeDir, "sing-box-tray.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		a.logFile = f
		a.logBuf.SetFileOutput(f)
	}

	a.proc = process.NewManager(cfg.SingBoxPath, a.logBuf, a.onCrash)

	a.log("--- sing-box-tray started ---")
	a.log("exe dir:      %s", exeDir)
	a.log("sing-box:     %s", cfg.SingBoxPath)
	a.log("config:       %s", cfg.ActiveConfigPath())
	a.log("default mode: %s", cfg.DefaultMode)
	a.log("initial mode: %s", initialMode)

	return a
}

func (a *App) log(format string, args ...any) {
	a.logBuf.Append(fmt.Sprintf("[tray] "+format, args...))
}

func (a *App) OnReady() {
	systray.SetIcon(assets.IconGrey)
	systray.SetTooltip(a.strs.TooltipStopped)

	mSettings := systray.AddMenuItem(a.strs.MenuSettings, a.strs.MenuSettingsTip)
	systray.AddSeparator()
	mStart := systray.AddMenuItem(a.strs.MenuStart, a.strs.MenuStartTip)
	mStop := systray.AddMenuItem(a.strs.MenuStop, a.strs.MenuStopTip)
	mRestart := systray.AddMenuItem(a.strs.MenuRestart, a.strs.MenuRestartTip)
	systray.AddSeparator()

	mMode := systray.AddMenuItem(a.strs.MenuMode, "")
	mModeOff := mMode.AddSubMenuItem(a.strs.ModeOff, "")
	mModeProxy := mMode.AddSubMenuItem(a.strs.ModeSystemProxy, "")
	mModeTUN := mMode.AddSubMenuItem(a.strs.ModeTUN, "")
	systray.AddSeparator()

	mConfig := systray.AddMenuItem(a.strs.MenuConfig, "")
	configItems, configNames := a.buildConfigItems(mConfig, a.cfg.ConfigDir)
	mOpenConfigFile := systray.AddMenuItem(a.strs.MenuOpenConfigFile, "")
	mOpenConfigFolder := systray.AddMenuItem(a.strs.MenuOpenConfigFolder, "")
	systray.AddSeparator()

	mLanguages := systray.AddMenuItem(languagesMenuTitle, "")
	mLangAuto := mLanguages.AddSubMenuItem(langLabelAuto, "")
	mLangEN := mLanguages.AddSubMenuItem(langLabelEN, "")
	mLangRU := mLanguages.AddSubMenuItem(langLabelRU, "")
	mLangUA := mLanguages.AddSubMenuItem(langLabelUA, "")
	systray.AddSeparator()

	mAuto := systray.AddMenuItemCheckbox(a.strs.MenuAutostart, a.strs.MenuAutostartTip, autostart.IsEnabled())
	mClashAPI := systray.AddMenuItemCheckbox(clashAPILabel, "", a.cfg.ClashAPI.Enabled)
	mYacd := systray.AddMenuItemCheckbox(yacdLabel, "", a.cfg.ClashAPI.Yacd)
	setClashMenuState(mClashAPI, mYacd, a.cfg.ClashAPI)
	systray.AddSeparator()

	mLogs := systray.AddMenuItem(a.strs.MenuViewLogs, "")
	systray.AddSeparator()

	mAbout := systray.AddMenuItem(a.strs.MenuAbout, "")
	systray.AddSeparator()

	mQuit := systray.AddMenuItem(a.strs.MenuExit, "")

	mStop.Disable()
	mRestart.Disable()

	_, mode := a.st.Get()
	setModeChecks(mModeOff, mModeProxy, mModeTUN, mode)
	setLanguageChecks(mLangAuto, mLangEN, mLangRU, mLangUA, a.cfg.Language)

	a.items = menuItems{
		settings:  mSettings,
		start:     mStart,
		stop:      mStop,
		restart:   mRestart,
		mode:      mMode,
		modeOff:   mModeOff,
		modeProxy: mModeProxy,
		modeTUN:   mModeTUN,

		config:      mConfig,
		configItems: configItems,
		configNames: configNames,

		openConfigFile:   mOpenConfigFile,
		openConfigFolder: mOpenConfigFolder,

		langAuto: mLangAuto,
		langEN:   mLangEN,
		langRU:   mLangRU,
		langUA:   mLangUA,

		autostart: mAuto,
		clashAPI:  mClashAPI,
		clashYacd: mYacd,
		viewLogs:  mLogs,
		about:     mAbout,
		quit:      mQuit,
	}

	go a.watchState()
	go a.handleClicks()
	a.restartConfigWatcher()
	a.restartConfigDirWatcher()

	go func() {
		a.checkFirstRunDeps()
		if a.cfg.StartOnLaunch {
			a.log("start_on_launch=true, starting...")
			a.start()
		}
	}()
}

func (a *App) OnExit() {
	_, mode := a.st.Get()
	if a.proc.IsRunning() {
		a.log("exit: stopping process")
		a.proc.Stop(5 * time.Second)
	}
	a.cleanup(mode)
	a.log("--- sing-box-tray stopped ---")
	if a.logFile != nil {
		a.logFile.Close()
	}
}

// onCrash is called by the process manager when sing-box exits unexpectedly.
func (a *App) onCrash() {
	a.mu.Lock()
	_, mode := a.st.Get()
	tmp := a.tempCfg
	a.tempCfg = ""
	a.mu.Unlock()

	a.log("sing-box crashed (mode=%s)", mode)

	if mode == state.ModeSystemProxy {
		a.log("clearing system proxy after crash")
		_ = proxy.Clear()
	}
	if tmp != "" {
		_ = os.Remove(tmp)
	}

	a.st.Set(state.StateCrashed, mode)

	n := toast.Notification{
		AppID:   appTitle,
		Title:   a.strs.ToastCrashedTitle,
		Message: a.strs.ToastCrashedMsg,
	}
	_ = n.Push()
}

func (a *App) start() {
	a.mu.Lock()
	appState, mode := a.st.Get()
	if appState == state.StateStopping {
		// Will be started by stop() after it finishes.
		a.pendingStart = true
		a.mu.Unlock()
		return
	}
	if appState != state.StateStopped && appState != state.StateCrashed {
		a.mu.Unlock()
		return
	}
	a.st.Set(state.StateStarting, mode)
	a.mu.Unlock()

	a.log("starting (mode=%s)", mode)

	if mode == state.ModeTUN && !elevation.IsElevated() {
		a.log("not elevated, re-launching as admin")
		if err := elevation.RelaunchAsAdmin(fmt.Sprintf("--force-mode=%s", mode)); err != nil {
			a.log("elevation failed: %s", err)
			a.st.Set(state.StateCrashed, mode)
			return
		}
		a.releaseMutex() // Release mutex before exit so the elevated instance can acquire it.
		os.Exit(0)
	}

	cfgPath, err := a.prepareConfig(mode)
	if err != nil {
		a.log("prepare config failed: %s", err)
		a.cleanup(mode)
		a.st.Set(state.StateCrashed, mode)
		return
	}

	a.log("launching: %s run -c %s", a.cfg.SingBoxPath, cfgPath)

	if err := a.proc.Start(cfgPath); err != nil {
		a.log("process start failed: %s", err)
		a.cleanup(mode)
		a.st.Set(state.StateCrashed, mode)
		return
	}

	a.log("process started successfully")
	a.st.Set(state.StateRunning, mode)
}

func (a *App) stop() {
	a.mu.Lock()
	appState, mode := a.st.Get()
	if appState != state.StateRunning && appState != state.StateStarting && appState != state.StateCrashed {
		a.mu.Unlock()
		return
	}
	a.st.Set(state.StateStopping, mode)
	a.mu.Unlock()

	a.log("stopping (mode=%s)", mode)
	a.proc.Stop(5 * time.Second)
	a.cleanup(mode)
	a.st.Set(state.StateStopped, mode)
	a.log("stopped")

	a.mu.Lock()
	pending := a.pendingStart
	a.pendingStart = false
	a.mu.Unlock()

	if pending {
		a.log("pending start detected, starting...")
		a.start()
	}
}

func (a *App) switchMode(newMode state.ProxyMode) {
	a.mu.Lock()
	appState, curMode := a.st.Get()
	if curMode == newMode {
		a.mu.Unlock()
		return
	}
	wasRunning := appState == state.StateRunning
	a.mu.Unlock()

	a.log("switching mode: %s -> %s", curMode, newMode)

	if wasRunning {
		a.mu.Lock()
		a.st.Set(state.StateStopping, curMode)
		a.mu.Unlock()
		a.proc.Stop(5 * time.Second)
		a.cleanup(curMode)
	}

	a.st.Set(state.StateStopped, newMode)

	if wasRunning {
		a.start()
	}
}

// switchConfig applies a newly picked sing-box config file from the tray's
// Config submenu: persists the choice, restarts file-change watching to
// track the new file, and — mirroring switchMode — stops and restarts
// sing-box live if it's currently running.
func (a *App) switchConfig(name string) {
	a.mu.Lock()
	if a.cfg.SelectedConfig == name {
		a.mu.Unlock()
		return
	}
	appState, mode := a.st.Get()
	wasRunning := appState == state.StateRunning
	a.mu.Unlock()

	a.log("switching config: %s -> %s", a.cfg.SelectedConfig, name)

	if wasRunning {
		a.mu.Lock()
		a.st.Set(state.StateStopping, mode)
		a.mu.Unlock()
		a.proc.Stop(5 * time.Second)
		a.cleanup(mode)
	}

	a.cfg.SelectedConfig = name
	if err := a.cfg.Save(a.exeDir); err != nil {
		a.log("save config after config switch: %s", err)
	}
	setConfigChecks(a.items.configItems, a.items.configNames, name)
	a.restartConfigWatcher()

	a.st.Set(state.StateStopped, mode)

	if wasRunning {
		a.start()
	}
}

// buildConfigItems scans dir and adds one checkable submenu item per config
// file found under parent, each wired to switchConfig via its own click loop
// (the item count is dynamic, so it can't be folded into the fixed select in
// handleClicks like every other submenu). Enables or disables parent
// depending on whether anything was found, and logs the scan so a folder
// that unexpectedly yields zero files is diagnosable from the log instead of
// just showing up as a disabled menu.
func (a *App) buildConfigItems(parent *systray.MenuItem, dir string) (items []*systray.MenuItem, names []string) {
	names, err := config.ListConfigFiles(dir)
	if err != nil {
		a.log("list config files (%s): %s", dir, err)
	}
	a.log("config dir %s: found %d config file(s): %v", dir, len(names), names)

	items = make([]*systray.MenuItem, len(names))
	for i, name := range names {
		item := parent.AddSubMenuItemCheckbox(name, "", name == a.cfg.SelectedConfig)
		items[i] = item
		go func(name string, item *systray.MenuItem) {
			for range item.ClickedCh {
				go a.switchConfig(name)
			}
		}(name, item)
	}
	if len(names) == 0 {
		parent.Disable()
	} else {
		parent.Enable()
	}
	return items, names
}

// rebuildConfigMenu re-scans dir (called when Settings changes ConfigDir)
// and replaces the Config submenu's items. getlantern/systray has no API to
// remove a menu item, so the old ones are just hidden rather than reused.
func (a *App) rebuildConfigMenu(dir string) {
	a.mu.Lock()
	oldItems := a.items.configItems
	a.mu.Unlock()

	for _, item := range oldItems {
		item.Hide()
	}

	items, names := a.buildConfigItems(a.items.config, dir)

	a.mu.Lock()
	a.items.configItems = items
	a.items.configNames = names
	a.mu.Unlock()
}

func (a *App) prepareConfig(mode state.ProxyMode) (string, error) {
	switch mode {
	case state.ModeTUN:
		a.log("injecting TUN inbound into temp config")
		tmpPath, err := tun.InjectTUN(a.cfg.ActiveConfigPath(), a.cfg.TUN, a.cfg.SingBoxPath)
		if err != nil {
			return "", fmt.Errorf("inject TUN config: %w", err)
		}
		a.log("temp config written: %s", tmpPath)
		if err := config.ApplyClashAPI(tmpPath, a.cfg.ClashAPI); err != nil {
			return "", fmt.Errorf("apply clash api config: %w", err)
		}

		if err := tun.EnsureWintunDll(a.cfg.WintunDllPath, filepath.Dir(a.cfg.SingBoxPath)); err != nil {
			a.log("wintun.dll warning: %s", err)
		}
		a.mu.Lock()
		a.tempCfg = tmpPath
		a.mu.Unlock()
		return tmpPath, nil

	case state.ModeSystemProxy:
		a.log("injecting system-proxy inbound into temp config")
		tmpPath, err := config.InjectSystemProxy(a.cfg.ActiveConfigPath(), a.cfg.SystemProxy)
		if err != nil {
			return "", fmt.Errorf("inject system-proxy config: %w", err)
		}
		a.log("temp config written: %s", tmpPath)
		if err := config.ApplyClashAPI(tmpPath, a.cfg.ClashAPI); err != nil {
			return "", fmt.Errorf("apply clash api config: %w", err)
		}

		tag := a.cfg.SystemProxyInbound
		a.log("looking for http/mixed inbound (tag=%q) in %s", tag, tmpPath)
		host, port, err := config.FindInboundAddr(tmpPath, tag)
		if err != nil {
			return "", fmt.Errorf("find proxy inbound: %w", err)
		}
		a.log("found inbound: %s:%d", host, port)
		if err := proxy.Set(host, fmt.Sprintf("%d", port)); err != nil {
			return "", fmt.Errorf("set system proxy: %w", err)
		}
		a.log("system proxy set to %s:%d", host, port)

		a.mu.Lock()
		a.tempCfg = tmpPath
		a.mu.Unlock()
		return tmpPath, nil

	default:
		// Off mode normally hands the user's config straight to sing-box, but
		// the clash_api override still has to apply — and the original file
		// must stay untouched, so run a patched temp copy instead.
		tmpPath, err := config.CopyWithClashAPI(a.cfg.ActiveConfigPath(), a.cfg.ClashAPI)
		if err != nil {
			return "", fmt.Errorf("copy config with clash api: %w", err)
		}
		a.log("temp config written: %s", tmpPath)
		a.mu.Lock()
		a.tempCfg = tmpPath
		a.mu.Unlock()
		return tmpPath, nil
	}
}

func (a *App) cleanup(mode state.ProxyMode) {
	if mode == state.ModeSystemProxy {
		a.log("clearing system proxy")
		if err := proxy.Clear(); err != nil {
			a.log("proxy.Clear error: %s", err)
		}
	}
	a.mu.Lock()
	tmp := a.tempCfg
	a.tempCfg = ""
	a.mu.Unlock()
	if tmp != "" {
		a.log("removing temp config: %s", tmp)
		_ = os.Remove(tmp)
	}
}

func (a *App) toggleAutostart() {
	_, mode := a.st.Get()
	if autostart.IsEnabled() {
		if err := autostart.Disable(); err != nil {
			a.log("disable autostart: %s", err)
			infoBox(fmt.Sprintf(a.strs.DialogErrorFmt, err), appTitle)
			return
		}
		a.items.autostart.Uncheck()
		a.cfg.Autostart = false
		a.log("autostart disabled")
	} else {
		elevated := mode == state.ModeTUN
		if err := autostart.Enable(elevated); err != nil {
			a.log("enable autostart: %s", err)
			infoBox(fmt.Sprintf(a.strs.DialogErrorFmt, err), appTitle)
			return
		}
		a.items.autostart.Check()
		a.cfg.Autostart = true
		a.log("autostart enabled (elevated=%v)", elevated)
	}
	_ = a.cfg.Save(a.exeDir)
}

// checkFirstRunDeps first reconciles sing_box_path, wintun_dll_path, and the
// active config against exeDir (see config.ReconcilePaths — this recovers
// automatically if the tray was moved to a new folder alongside its
// companion files, e.g. after a manual reinstall), then warns about whichever
// of sing-box.exe/wintun.dll is still missing, naming the exact path the file
// must be placed at, and warns if no usable config was found. Nothing is ever
// downloaded — installing these files is a manual step. Runs on every
// startup, but is only ever actionable on a fresh install or after a path
// went stale, since all checks are no-ops once the files exist.
func (a *App) checkFirstRunDeps() {
	changed, missingSingBox, missingWintun, missingConfig := a.cfg.ReconcilePaths(a.exeDir)
	if changed {
		if err := a.cfg.Save(a.exeDir); err != nil {
			a.log("save config after path reconciliation: %s", err)
		} else {
			a.log("reconciled config paths against exe directory: sing-box=%s wintun=%s config=%s",
				a.cfg.SingBoxPath, a.cfg.WintunDllPath, a.cfg.ActiveConfigPath())
		}
	}
	if missingConfig {
		infoBox(fmt.Sprintf(a.strs.DialogMissingConfigFmt, a.cfg.ActiveConfigPath()), appTitle)
	}
	if missingSingBox {
		a.log("sing-box.exe missing at %s", a.cfg.SingBoxPath)
		infoBox(fmt.Sprintf(a.strs.DialogMissingSingBoxFmt, a.cfg.SingBoxPath), appTitle)
	}
	if missingWintun {
		a.log("wintun.dll missing at %s", a.cfg.WintunDllPath)
		infoBox(fmt.Sprintf(a.strs.DialogMissingWintunFmt, a.cfg.WintunDllPath), appTitle)
	}
}

// showAbout displays the tray launcher version plus a clickable link to the
// project repository. A plain MessageBox can't have a clickable link, so this
// opens a small walk window (aboutwin) instead of using infoBox.
func (a *App) showAbout() {
	aboutwin.Show(a.strs, appTitle, version.Version, repoURL)
}

// openActiveConfig opens the currently selected sing-box config in whatever
// application is registered for .json files.
//
// It goes through cmd's `start` rather than ShellExecuteW: the tray runs
// elevated for TUN, and a high-integrity process cannot hand a shell request to
// the medium-integrity explorer, so ShellExecuteW fails with
// SE_ERR_ACCESSDENIED (5). explorer.exe can't stand in either — given a file it
// opens the containing folder instead of the file.
func (a *App) openActiveConfig() {
	cmd := exec.Command("cmd", "/c", "start", "", a.cfg.ActiveConfigPath())
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		a.log("open config file failed: %s", err)
	}
}

// openConfigDir opens the config folder in Explorer. explorer.exe is spawned
// directly instead of via ShellExecuteW for the same elevated-shell reason as
// above; when handed a directory it does exactly what "explore" would.
func (a *App) openConfigDir() {
	cmd := exec.Command("explorer.exe", a.cfg.ConfigDir)
	if err := cmd.Start(); err != nil {
		a.log("open config dir failed: %s", err)
	}
}

// openLogTerminal shows the log in a console window that keeps tailing the log
// file. A console renders sing-box's ANSI-colored output as intended, and
// unlike the walk window it replaced it doesn't repaint every line on each
// update (which flickered). The tail length reuses the log_lines setting.
func (a *App) openLogTerminal() {
	logPath := filepath.Join(a.exeDir, "sing-box-tray.log")
	script := fmt.Sprintf("Get-Content -Wait -Tail %d -LiteralPath '%s'", a.cfg.LogLines, logPath)
	cmd := exec.Command("cmd", "/c", "start", a.strs.LogWindowTitle,
		"powershell", "-NoExit", "-NoLogo", "-Command", script)
	if err := cmd.Start(); err != nil {
		a.log("open log terminal failed: %s", err)
		infoBox(fmt.Sprintf(a.strs.DialogErrorFmt, err), appTitle)
	}
}

// setClashMenuState syncs the Clash API / YACD checkboxes with cfg. YACD is
// served by the Clash API, so its item is disabled while the API is off.
func setClashMenuState(apiItem, yacdItem *systray.MenuItem, cfg config.ClashAPIConfig) {
	checkOrUncheck(apiItem, cfg.Enabled)
	checkOrUncheck(yacdItem, cfg.Yacd)
	if cfg.Enabled {
		yacdItem.Enable()
	} else {
		yacdItem.Disable()
	}
}

// toggleClashAPI flips the Clash API. Disabling it also drops YACD, which is
// served by the API. The API is baked into the config at process start, so a
// running sing-box is restarted to apply the change.
func (a *App) toggleClashAPI() {
	a.cfg.ClashAPI.Enabled = !a.cfg.ClashAPI.Enabled
	if !a.cfg.ClashAPI.Enabled {
		a.cfg.ClashAPI.Yacd = false
	}
	setClashMenuState(a.items.clashAPI, a.items.clashYacd, a.cfg.ClashAPI)
	if err := a.cfg.Save(a.exeDir); err != nil {
		a.log("save config after clash api toggle: %s", err)
	}
	a.log("clash api: %v (yacd=%v)", a.cfg.ClashAPI.Enabled, a.cfg.ClashAPI.Yacd)
	a.restartIfRunning()
}

// toggleYacd flips the yacd web dashboard. It lives inside the Clash API, so
// the menu item is only enabled while the API is on.
func (a *App) toggleYacd() {
	a.cfg.ClashAPI.Yacd = !a.cfg.ClashAPI.Yacd
	checkOrUncheck(a.items.clashYacd, a.cfg.ClashAPI.Yacd)
	if err := a.cfg.Save(a.exeDir); err != nil {
		a.log("save config after yacd toggle: %s", err)
	}
	a.log("yacd: %v", a.cfg.ClashAPI.Yacd)
	a.restartIfRunning()
}

// restartIfRunning restarts sing-box so a config-level change takes effect.
func (a *App) restartIfRunning() {
	if a.proc.IsRunning() {
		a.log("restarting sing-box to apply the config change")
		go func() { a.stop(); a.start() }()
	}
}

// applyLanguage recomputes a.strs for langCode, retitles the menu, and
// refreshes the dynamic tooltip/icon. Shared by the tray Languages submenu
// and the Settings window's Save handler.
func (a *App) applyLanguage(langCode string) {
	a.strs = i18n.Get(i18n.Resolve(langCode))
	a.refreshMenuTexts()
	setLanguageChecks(a.items.langAuto, a.items.langEN, a.items.langRU, a.items.langUA, langCode)
	appState, mode := a.st.Get()
	a.updateUI(appState, mode)
}

// switchLanguage persists langCode as the new UI language and applies it live.
func (a *App) switchLanguage(langCode string) {
	if a.cfg.Language == langCode {
		return
	}
	a.cfg.Language = langCode
	if err := a.cfg.Save(a.exeDir); err != nil {
		a.log("save config after language switch: %s", err)
	}
	a.log("language switched to %s", langCode)
	a.applyLanguage(langCode)
}

// refreshMenuTexts re-applies a.strs to every menu item with translated text,
// after a live language switch. Items with literal (untranslated) labels —
// proper nouns and the Languages picker itself — are left alone.
func (a *App) refreshMenuTexts() {
	a.items.settings.SetTitle(a.strs.MenuSettings)
	a.items.settings.SetTooltip(a.strs.MenuSettingsTip)
	a.items.start.SetTitle(a.strs.MenuStart)
	a.items.start.SetTooltip(a.strs.MenuStartTip)
	a.items.stop.SetTitle(a.strs.MenuStop)
	a.items.stop.SetTooltip(a.strs.MenuStopTip)
	a.items.restart.SetTitle(a.strs.MenuRestart)
	a.items.restart.SetTooltip(a.strs.MenuRestartTip)
	a.items.mode.SetTitle(a.strs.MenuMode)
	a.items.modeOff.SetTitle(a.strs.ModeOff)
	a.items.modeProxy.SetTitle(a.strs.ModeSystemProxy)
	a.items.modeTUN.SetTitle(a.strs.ModeTUN)
	a.items.config.SetTitle(a.strs.MenuConfig)
	a.items.openConfigFile.SetTitle(a.strs.MenuOpenConfigFile)
	a.items.openConfigFolder.SetTitle(a.strs.MenuOpenConfigFolder)
	a.items.autostart.SetTitle(a.strs.MenuAutostart)
	a.items.autostart.SetTooltip(a.strs.MenuAutostartTip)
	a.items.viewLogs.SetTitle(a.strs.MenuViewLogs)
	a.items.about.SetTitle(a.strs.MenuAbout)
	a.items.quit.SetTitle(a.strs.MenuExit)
}

func (a *App) openSettings() {
	prevLang := a.cfg.Language
	prevConfigDir := a.cfg.ConfigDir
	prevSelectedConfig := a.cfg.SelectedConfig
	prevAutostart := autostart.IsEnabled()
	settings.Show(a.cfg, a.strs, a.items.configNames, prevAutostart, func(updated *config.TrayConfig) {
		a.log("settings saved: sing-box=%s config=%s", updated.SingBoxPath, updated.ActiveConfigPath())
		a.proc.SetSingBoxPath(updated.SingBoxPath)
		if updated.Autostart != prevAutostart {
			// toggleAutostart flips whatever autostart.IsEnabled() currently
			// reports, which is still prevAutostart here — nothing else can
			// have changed it between the two reads.
			a.toggleAutostart()
		}
		// Re-read the real Task Scheduler state rather than trusting the
		// checkbox value: if toggleAutostart's schtasks call failed, it
		// already showed an error dialog, but updated.Autostart still holds
		// the user's requested (unapplied) value — persisting that would
		// silently drift tray-config.json away from reality.
		updated.Autostart = autostart.IsEnabled()
		checkOrUncheck(a.items.autostart, updated.Autostart)
		if err := updated.Save(a.exeDir); err != nil {
			a.log("save settings: %s", err)
		}
		if updated.Language != prevLang {
			a.applyLanguage(updated.Language)
		}
		switch {
		case updated.ConfigDir != prevConfigDir:
			// The folder itself changed, so the submenu's items no longer
			// match what's on disk — rescan and rebuild it.
			a.rebuildConfigMenu(updated.ConfigDir)
			a.restartConfigWatcher()
			a.restartConfigDirWatcher()
		case updated.SelectedConfig != prevSelectedConfig:
			setConfigChecks(a.items.configItems, a.items.configNames, updated.SelectedConfig)
			a.restartConfigWatcher()
		}
	})
}

func checkOrUncheck(item *systray.MenuItem, checked bool) {
	if checked {
		item.Check()
	} else {
		item.Uncheck()
	}
}

// restartConfigWatcher (re)starts a watcher that shows a restart prompt when
// the active sing-box config or tray-config.json changes. Called from
// OnReady and again whenever the active config is switched, since the
// previous watcher was still watching the old file.
func (a *App) restartConfigWatcher() {
	a.mu.Lock()
	if a.configWatcher != nil {
		a.configWatcher.Stop()
	}
	paths := []string{a.cfg.ActiveConfigPath(), filepath.Join(a.exeDir, "tray-config.json")}
	w := watcher.New(paths, func(path string) {
		a.log("file changed: %s", path)
		appState, _ := a.st.Get()
		if appState != state.StateRunning {
			return
		}
		msg := fmt.Sprintf(a.strs.DialogConfigChangedFmt, filepath.Base(path))
		if msgBox(msg, appTitle) {
			go func() { a.stop(); a.start() }()
		}
	})
	w.Start()
	a.configWatcher = w
	a.mu.Unlock()
}

// restartConfigDirWatcher (re)starts a watcher that rebuilds the Config
// submenu whenever the set of *.json files in ConfigDir changes — e.g. the
// user drops a new sing-box config into the folder while the tray is
// running. Called from OnReady and again whenever ConfigDir is changed via
// Settings, since the previous watcher was still watching the old folder.
func (a *App) restartConfigDirWatcher() {
	a.mu.Lock()
	if a.configDirWatcher != nil {
		a.configDirWatcher.Stop()
	}
	dir := a.cfg.ConfigDir
	w := watcher.NewDir(dir, config.ListConfigFiles, func() {
		a.log("config dir changed, rebuilding menu: %s", dir)
		a.rebuildConfigMenu(dir)
	})
	w.Start()
	a.configDirWatcher = w
	a.mu.Unlock()
}

func (a *App) watchState() {
	for range a.st.Subscribe() {
		appState, mode := a.st.Get()
		a.updateUI(appState, mode)
	}
}

func (a *App) updateUI(appState state.AppState, mode state.ProxyMode) {
	isRunning := appState == state.StateRunning || appState == state.StateStarting
	isBusy := appState == state.StateStarting || appState == state.StateStopping

	if isRunning {
		a.items.start.Disable()
		a.items.stop.Enable()
		a.items.restart.Enable()
	} else {
		a.items.start.Enable()
		a.items.stop.Disable()
		a.items.restart.Disable()
	}

	if isBusy {
		a.items.modeOff.Disable()
		a.items.modeProxy.Disable()
		a.items.modeTUN.Disable()
	} else {
		a.items.modeOff.Enable()
		a.items.modeProxy.Enable()
		a.items.modeTUN.Enable()
	}

	switch appState {
	case state.StateRunning:
		systray.SetIcon(assets.IconGreen)
		systray.SetTooltip(fmt.Sprintf(a.strs.TooltipRunningFmt, a.modeLabel(mode)))
	case state.StateCrashed:
		systray.SetIcon(assets.IconRed)
		systray.SetTooltip(a.strs.TooltipCrashed)
	default:
		systray.SetIcon(assets.IconGrey)
		systray.SetTooltip(a.strs.TooltipStopped)
	}

	setModeChecks(a.items.modeOff, a.items.modeProxy, a.items.modeTUN, mode)
}

// modeLabel returns the localized display name for mode.
func (a *App) modeLabel(mode state.ProxyMode) string {
	switch mode {
	case state.ModeSystemProxy:
		return a.strs.ModeSystemProxy
	case state.ModeTUN:
		return a.strs.ModeTUN
	default:
		return a.strs.ModeOff
	}
}

func (a *App) handleClicks() {
	for {
		select {
		case <-a.items.settings.ClickedCh:
			a.openSettings()
		case <-a.items.start.ClickedCh:
			go a.start()
		case <-a.items.stop.ClickedCh:
			go a.stop()
		case <-a.items.restart.ClickedCh:
			go func() { a.stop(); a.start() }()
		case <-a.items.modeOff.ClickedCh:
			go a.switchMode(state.ModeOff)
		case <-a.items.modeProxy.ClickedCh:
			go a.switchMode(state.ModeSystemProxy)
		case <-a.items.modeTUN.ClickedCh:
			go a.switchMode(state.ModeTUN)
		case <-a.items.openConfigFile.ClickedCh:
			go a.openActiveConfig()
		case <-a.items.openConfigFolder.ClickedCh:
			go a.openConfigDir()
		case <-a.items.langAuto.ClickedCh:
			go a.switchLanguage("auto")
		case <-a.items.langEN.ClickedCh:
			go a.switchLanguage("en")
		case <-a.items.langRU.ClickedCh:
			go a.switchLanguage("ru")
		case <-a.items.langUA.ClickedCh:
			go a.switchLanguage("ua")
		case <-a.items.autostart.ClickedCh:
			go a.toggleAutostart()
		case <-a.items.clashAPI.ClickedCh:
			go a.toggleClashAPI()
		case <-a.items.clashYacd.ClickedCh:
			go a.toggleYacd()
		case <-a.items.viewLogs.ClickedCh:
			go a.openLogTerminal()
		case <-a.items.about.ClickedCh:
			go a.showAbout()
		case <-a.items.quit.ClickedCh:
			systray.Quit()
		}
	}
}

func setModeChecks(mOff, mProxy, mTUN *systray.MenuItem, mode state.ProxyMode) {
	mOff.Uncheck()
	mProxy.Uncheck()
	mTUN.Uncheck()
	switch mode {
	case state.ModeOff:
		mOff.Check()
	case state.ModeSystemProxy:
		mProxy.Check()
	case state.ModeTUN:
		mTUN.Check()
	}
}

func setLanguageChecks(mAuto, mEN, mRU, mUA *systray.MenuItem, langCode string) {
	mAuto.Uncheck()
	mEN.Uncheck()
	mRU.Uncheck()
	mUA.Uncheck()
	switch langCode {
	case "en":
		mEN.Check()
	case "ru":
		mRU.Check()
	case "ua":
		mUA.Check()
	default:
		mAuto.Check()
	}
}

// setConfigChecks checks the item matching selected and unchecks the rest.
// items and names are parallel slices, as built in OnReady.
func setConfigChecks(items []*systray.MenuItem, names []string, selected string) {
	for i, item := range items {
		if names[i] == selected {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
}

// msgBox shows a Yes/No dialog and returns true if the user clicked Yes.
func msgBox(text, title string) bool {
	titlePtr, _ := windows.UTF16PtrFromString(title)
	textPtr, _ := windows.UTF16PtrFromString(text)
	const mbYesNo = 0x04
	const idYes = 6
	ret, _, _ := procMsgBox.Call(0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		mbYesNo,
	)
	return int(ret) == idYes
}

// infoBox shows an OK-only informational dialog.
func infoBox(text, title string) {
	titlePtr, _ := windows.UTF16PtrFromString(title)
	textPtr, _ := windows.UTF16PtrFromString(text)
	const mbOK = 0x00
	procMsgBox.Call(0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		mbOK,
	)
}
