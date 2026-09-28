//go:build windows

package tray

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"github.com/go-toast/toast"
	"golang.org/x/sys/windows"

	"github.com/Be1zebub/sing-box-tray/assets"
	"github.com/Be1zebub/sing-box-tray/internal/abouttui"
	"github.com/Be1zebub/sing-box-tray/internal/autostart"
	"github.com/Be1zebub/sing-box-tray/internal/clashapi"
	"github.com/Be1zebub/sing-box-tray/internal/config"
	"github.com/Be1zebub/sing-box-tray/internal/elevation"
	"github.com/Be1zebub/sing-box-tray/internal/i18n"
	"github.com/Be1zebub/sing-box-tray/internal/logbuf"
	"github.com/Be1zebub/sing-box-tray/internal/process"
	"github.com/Be1zebub/sing-box-tray/internal/proxy"
	"github.com/Be1zebub/sing-box-tray/internal/state"
	"github.com/Be1zebub/sing-box-tray/internal/tun"
	"github.com/Be1zebub/sing-box-tray/internal/watcher"
)

const (
	appTitle = "sing-box-tray"

	// repoURL is this fork's own repository, shown in the About window.
	repoURL = "https://github.com/Be1zebub/sing-box-tray"

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
	openSplitTUN     *systray.MenuItem
	openImporter     *systray.MenuItem
	proxy            *systray.MenuItem

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

// proxyMenuGroup is one group inside the Proxy submenu, with its radio items
// keyed by member name.
type proxyMenuGroup struct {
	name   string
	parent *systray.MenuItem
	items  map[string]*systray.MenuItem
}

// App orchestrates the sing-box process, proxy settings, and tray UI.
type App struct {
	cfg          *config.TrayConfig
	exeDir       string
	st           *state.Manager
	proc         *process.Manager
	logBuf       *logbuf.Buffer
	logFile      io.Closer
	releaseMutex func()
	strs         i18n.Strings

	mu               sync.Mutex
	items            menuItems
	tempCfg          string
	pendingStart     bool
	configWatcher    *watcher.Watcher
	configDirWatcher *watcher.DirWatcher

	proxyParent *systray.MenuItem
	proxyStatus *systray.MenuItem
	proxyGroups []proxyMenuGroup
	proxyShape  string
	proxyLeaf   string
	proxyVia    string
	proxyPing   int
	pingGen     int
	pingBusy    bool
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
	if f, err := logbuf.OpenRolling(logPath); err == nil {
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
	a.log("split-tun:    %s", config.SplitTUNPath(exeDir))
	a.proxyPing = -1

	return a
}

func (a *App) log(format string, args ...any) {
	a.logBuf.Append(fmt.Sprintf("[tray] "+format, args...))
}

func (a *App) OnReady() {
	systray.SetIcon(assets.IconGrey)
	systray.SetTooltip(a.strs.TooltipStopped)

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
	mOpenSplitTUN := systray.AddMenuItem(a.strs.MenuOpenSplitTUN, "")
	mOpenImporter := systray.AddMenuItem(a.strs.MenuOpenImporter, "")
	mProxy := systray.AddMenuItem(a.strs.MenuProxy, "")
	mProxyStatus := mProxy.AddSubMenuItem("—", "")
	mProxyStatus.Disable()
	mProxyStatus.Hide()
	mProxy.Disable() // enabled once the Clash API answers
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

	a.proxyParent = mProxy
	a.proxyStatus = mProxyStatus

	a.items = menuItems{
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
		openSplitTUN:     mOpenSplitTUN,
		openImporter:     mOpenImporter,
		proxy:            mProxy,

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
	go a.proxyMenuLoop()
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

// rebuildConfigMenu re-scans dir and replaces the Config submenu's items. getlantern/systray has no API to
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
		split, err := config.LoadSplitTUN(a.exeDir)
		if err != nil {
			return "", fmt.Errorf("load split-tun.json: %w", err)
		}
		switch {
		case !split.Active():
			a.log("split-tun: disabled")
		case !split.Empty():
			a.log("split-tun: %s, %d ip, %d domain, %d process name, %d process path",
				split.Mode, len(split.IPCIDR), len(split.DomainSuffix), len(split.ProcessName), len(split.ProcessPath))
		}
		tmpPath, err := tun.InjectTUN(a.cfg.ActiveConfigPath(), a.cfg.TUN, a.cfg.SingBoxPath, split)
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

// showAbout opens the console splash in Windows Terminal. The tray binary is a
// GUI app, so the tab runs cmd and the splash attaches to that console.
// Without Terminal it falls back to conhost.
func (a *App) showAbout() {
	exe, err := os.Executable()
	if err != nil {
		a.log("about: %s", err)
		return
	}
	a.mu.Lock()
	leaf, via, ping := a.proxyLeaf, a.proxyVia, a.proxyPing
	a.mu.Unlock()
	_, mode := a.st.Get()
	snap := fmt.Sprintf(`{"mode":%q,"proxy":%q,"via":%q,"ping":%d,"config":%q}`,
		a.tooltipMode(mode), leaf, via, ping, a.cfg.SelectedConfig)
	// Two cmd layers (the start launcher and the tab shell) each expand %.
	tab := fmt.Sprintf(`cmd.exe /c ""%s" %s %s"`,
		escapeCmdPercent(exe, 2), abouttui.FlagWT, abouttui.EncodeSnapshot(snap))
	if err := a.startWindowsTerminal(appTitle, tab); err != nil {
		a.log("about: windows terminal: %s", err)
	} else {
		return
	}
	// conhost has no OSC 8 clicks. Quick Edit is turned off in that process so
	// a click does not freeze it in mark mode. Empty title: start treats the
	// first quoted string as a window title. The snapshot rides on argv because
	// a de-elevated child does not inherit this process's environment.
	fallback := fmt.Sprintf(`cmd.exe /c start "" %s %s %s`,
		cmdQuote(escapeCmdPercent(exe, 1)), abouttui.Flag, abouttui.EncodeSnapshot(snap))
	if _, err := a.startDropAdmin(fallback); err != nil {
		a.log("about: %s", err)
	}
}

// startWindowsTerminal opens a tab running tabCommand. The launch goes through
// cmd's start: wt.exe in WindowsApps is an app-execution alias, and
// CreateProcess on that alias fails from an elevated tray. start uses
// ShellExecute, which can activate it. tabCommand is the raw tail after
// --title, for example a cmd.exe /c line or powershell.exe.
func (a *App) startWindowsTerminal(title, tabCommand string) error {
	line := `cmd.exe /c start "" wt.exe new-tab --title ` + cmdQuote(title) + ` ` + tabCommand
	_, err := a.startDropAdmin(line)
	return err
}

func (a *App) startDropAdmin(commandLine string) (bool, error) {
	elevated, err := elevation.StartDropAdmin(commandLine)
	if elevated {
		a.log("terminal: could not drop admin rights")
	}
	return elevated, err
}

func cmdQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// escapeCmdPercent doubles % once per cmd.exe that will parse the string.
func escapeCmdPercent(s string, layers int) string {
	for i := 0; i < layers; i++ {
		s = strings.ReplaceAll(s, "%", "%%")
	}
	return s
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

// openSplitTUN opens split-tun.json. LoadSplitTUN recreates the empty
// template first if the file was deleted, so the editor always has something
// to show.
func (a *App) openSplitTUN() {
	if _, err := config.LoadSplitTUN(a.exeDir); err != nil {
		a.log("open split-tun.json failed: %s", err)
		return
	}
	cmd := exec.Command("cmd", "/c", "start", "", config.SplitTUNPath(a.exeDir))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		a.log("open split-tun.json failed: %s", err)
	}
}

// openConfigImporter starts config-importer.exe from the tray directory.
// It is a console TUI, so it opens in Windows Terminal.
func (a *App) openConfigImporter() {
	path := filepath.Join(a.exeDir, "config-importer.exe")
	if _, err := os.Stat(path); err != nil {
		a.log("config-importer missing at %s", path)
		infoBox(fmt.Sprintf(a.strs.DialogMissingImporterFmt, path), appTitle)
		return
	}
	tab := cmdQuote(escapeCmdPercent(path, 1))
	if err := a.startWindowsTerminal("config-importer", tab); err != nil {
		a.log("config-importer: windows terminal: %s", err)
		fallback := fmt.Sprintf(`cmd.exe /c start "" %s`, cmdQuote(escapeCmdPercent(path, 1)))
		if _, err := a.startDropAdmin(fallback); err != nil {
			a.log("open config-importer failed: %s", err)
			infoBox(fmt.Sprintf(a.strs.DialogErrorFmt, err), appTitle)
		}
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
	script := fmt.Sprintf("Get-Content -Wait -Tail %d -LiteralPath '%s'",
		a.cfg.LogLines, strings.ReplaceAll(escapeCmdPercent(logPath, 1), "'", "''"))
	tab := fmt.Sprintf(`powershell.exe -NoExit -NoLogo -Command "%s"`, script)
	if err := a.startWindowsTerminal(a.strs.LogWindowTitle, tab); err != nil {
		a.log("open log terminal: windows terminal: %s", err)
		fallback := fmt.Sprintf(`cmd.exe /c start %s powershell -NoExit -NoLogo -Command "%s"`,
			cmdQuote(a.strs.LogWindowTitle), script)
		if _, err := a.startDropAdmin(fallback); err != nil {
			a.log("open log terminal failed: %s", err)
			infoBox(fmt.Sprintf(a.strs.DialogErrorFmt, err), appTitle)
		}
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
	if a.cfg.ClashAPI.Enabled {
		if changed, err := a.cfg.EnsureClashSecret(); err != nil {
			a.log("generate clash api secret: %s", err)
		} else if changed {
			a.log("clash api: generated a new secret")
		}
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

// proxyClient builds a Clash API client from the current settings.
func (a *App) proxyClient() *clashapi.Client {
	return clashapi.New(a.cfg.ClashAPI.Listen, a.cfg.ClashAPI.Secret)
}

// proxyMenuLoop keeps the Proxy submenu in sync with the Clash API. It is cheap
// to run: a signature comparison decides whether the menu has to be rebuilt,
// and otherwise only the radio checks are moved.
func (a *App) proxyMenuLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		a.refreshProxyMenu()
	}
}

func (a *App) refreshProxyMenu() {
	if !a.proc.IsRunning() || !a.cfg.ClashAPI.Enabled {
		a.clearProxyMenu()
		return
	}
	groups, defaultTag, err := a.proxyClient().Groups()
	if err != nil {
		// Unreachable (still starting, wrong port, secret mismatch): just
		// leave the submenu disabled rather than logging every tick.
		a.clearProxyMenu()
		return
	}
	if a.proxySignature(groups) != a.proxyShape {
		a.buildProxyMenu(groups)
	} else {
		a.syncProxyChecks(groups)
	}
	leaf, via := clashapi.ActiveRoute(defaultTag, groups)
	go a.measurePing(leaf, via)
}

// proxySignature captures everything the submenu renders, so a new group, a new
// member or a moved selection triggers a rebuild.
func (a *App) proxySignature(groups []clashapi.Group) string {
	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "%s|%s|%s|%s;", g.Name, g.Type, g.Now, strings.Join(g.All, ","))
	}
	return b.String()
}

// clearProxyMenu hides the submenu's items and disables its parent. Used
// whenever the Clash API is off or unreachable.
func (a *App) clearProxyMenu() {
	if a.proxyParent == nil {
		return
	}
	for _, g := range a.proxyGroups {
		for _, item := range g.items {
			item.Hide()
		}
		if g.parent != nil {
			g.parent.Hide()
		}
	}
	a.proxyGroups = nil
	a.proxyShape = ""
	a.mu.Lock()
	a.pingGen++
	a.proxyLeaf = ""
	a.proxyVia = ""
	a.proxyPing = -1
	a.mu.Unlock()
	if a.proxyStatus != nil {
		a.proxyStatus.Hide()
	}
	a.proxyParent.Disable()
	appState, mode := a.st.Get()
	a.applyTooltip(appState, mode)
}

// buildProxyMenu rebuilds the Proxy submenu from the Clash API. Selector groups
// get clickable radio items; URLTest groups are listed disabled, because
// sing-box rejects selection there with HTTP 400 — their current pick is still
// worth showing, since that is what the tunnel actually uses.
//
// ponytail: one goroutine per item per rebuild, and hiding an item never closes
// its ClickedCh, so rebuilds leak a few blocked goroutines. Rebuilds only happen
// when the API's group shape actually changes, so the ceiling is a handful per
// mode/config switch; introduce a cancellable watcher if that ever matters.
func (a *App) buildProxyMenu(groups []clashapi.Group) {
	if a.proxyParent == nil {
		return
	}
	a.clearProxyMenu()

	for _, g := range groups {
		parent := a.proxyParent.AddSubMenuItem(g.Name, "")
		entry := proxyMenuGroup{name: g.Name, parent: parent, items: map[string]*systray.MenuItem{}}

		for _, member := range g.All {
			item := parent.AddSubMenuItemCheckbox(member, "", member == g.Now)

			if g.Type != "Selector" {
				item.Disable()
				entry.items[member] = item
				continue
			}

			group, selected := g.Name, member
			go func() {
				for range item.ClickedCh {
					a.selectProxy(group, selected)
				}
			}()
			entry.items[member] = item
		}

		a.proxyGroups = append(a.proxyGroups, entry)
	}

	if len(a.proxyGroups) == 0 {
		a.clearProxyMenu()
		return
	}
	a.proxyShape = a.proxySignature(groups)
	a.proxyParent.Enable()
	if a.proxyStatus != nil {
		a.proxyStatus.Show()
		a.proxyStatus.Disable()
	}
}

// selectProxy switches a selector group to member and refreshes the checks.
func (a *App) selectProxy(group, member string) {
	if err := a.proxyClient().Select(group, member); err != nil {
		a.log("proxy select %s -> %s failed: %s", group, member, err)
		return
	}
	a.log("proxy %s -> %s", group, member)
	a.refreshProxyMenu()
}

// syncProxyChecks moves the radio checks to match the current Clash API state.
func (a *App) syncProxyChecks(groups []clashapi.Group) {
	byName := make(map[string]clashapi.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	for _, entry := range a.proxyGroups {
		g, ok := byName[entry.name]
		if !ok {
			continue
		}
		for member, item := range entry.items {
			checkOrUncheck(item, member == g.Now)
		}
	}
}

// applyLanguage recomputes a.strs for langCode, retitles the menu, and
// refreshes the dynamic tooltip/icon.
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
	a.items.openSplitTUN.SetTitle(a.strs.MenuOpenSplitTUN)
	a.items.openImporter.SetTitle(a.strs.MenuOpenImporter)
	a.items.proxy.SetTitle(a.strs.MenuProxy)
	a.items.autostart.SetTitle(a.strs.MenuAutostart)
	a.items.autostart.SetTooltip(a.strs.MenuAutostartTip)
	a.items.viewLogs.SetTitle(a.strs.MenuViewLogs)
	a.items.about.SetTitle(a.strs.MenuAbout)
	a.items.quit.SetTitle(a.strs.MenuExit)
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
	paths := []string{a.cfg.ActiveConfigPath(), a.cfg.FilePath()}
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
// running. Called from OnReady. The previous watcher is stopped first, so a
// later caller can point it at a new folder.
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
	case state.StateCrashed:
		systray.SetIcon(assets.IconRed)
	default:
		systray.SetIcon(assets.IconGrey)
	}
	a.applyTooltip(appState, mode)

	setModeChecks(a.items.modeOff, a.items.modeProxy, a.items.modeTUN, mode)
}

// measurePing records the active node immediately, then probes its delay.
// A probe already in flight is left alone; when it finishes a newer generation
// starts the probe for whatever is current.
func (a *App) measurePing(leaf, via string) {
	a.mu.Lock()
	same := leaf == a.proxyLeaf && via == a.proxyVia
	if same && leaf != "" && a.proxyPing >= 0 {
		a.mu.Unlock()
		return
	}
	if !same {
		a.proxyPing = -1
	}
	a.pingGen++
	gen := a.pingGen
	a.proxyLeaf = leaf
	a.proxyVia = via
	if a.proxyPing < 0 || leaf == "" {
		a.proxyPing = -1
	}
	shown := a.proxyPing
	startProbe := clashapi.Probeable(leaf, via) && !a.pingBusy
	if startProbe {
		a.pingBusy = true
	}
	a.mu.Unlock()

	a.paintProxyStatus(leaf, shown)
	appState, mode := a.st.Get()
	a.applyTooltip(appState, mode)
	if !startProbe {
		return
	}

	ping := -1
	if ms, err := a.proxyClient().Delay(leaf); err != nil {
		a.log("proxy delay %s: %s", leaf, err)
	} else {
		ping = ms
	}

	a.mu.Lock()
	a.pingBusy = false
	stale := a.pingGen != gen
	if !stale {
		a.proxyPing = ping
		leaf, via = a.proxyLeaf, a.proxyVia
	} else {
		leaf, via = a.proxyLeaf, a.proxyVia
	}
	a.mu.Unlock()

	if stale {
		if leaf != "" {
			a.measurePing(leaf, via)
		}
		return
	}
	a.paintProxyStatus(leaf, ping)
	appState, mode = a.st.Get()
	a.applyTooltip(appState, mode)
}

func (a *App) paintProxyStatus(leaf string, ping int) {
	if a.proxyStatus == nil || leaf == "" {
		return
	}
	title := leaf
	if ping >= 0 {
		title = fmt.Sprintf("%s · %dms", leaf, ping)
	}
	a.proxyStatus.SetTitle(title)
	a.proxyStatus.Show()
	a.proxyStatus.Disable()
}

func (a *App) applyTooltip(appState state.AppState, mode state.ProxyMode) {
	a.mu.Lock()
	leaf, via, ping := a.proxyLeaf, a.proxyVia, a.proxyPing
	a.mu.Unlock()

	var status string
	switch appState {
	case state.StateRunning:
		status = a.strs.StatusRunning
	case state.StateCrashed:
		status = a.strs.StatusCrashed
	case state.StateStarting:
		status = a.strs.StatusStarting
	case state.StateStopping:
		status = a.strs.StatusStopping
	default:
		status = a.strs.StatusStopped
	}
	modeName := a.tooltipMode(mode)
	if appState != state.StateRunning || leaf == "" {
		systray.SetTooltip(status + ", " + modeName)
		return
	}
	tip := fmt.Sprintf("%s, %s, %s", status, modeName, leaf)
	if via != "" && via != leaf {
		tip += " | " + via
	}
	if ping >= 0 {
		tip += fmt.Sprintf(", %dms", ping)
	}
	systray.SetTooltip(tip)
}

func (a *App) tooltipMode(mode state.ProxyMode) string {
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
		case <-a.items.openSplitTUN.ClickedCh:
			go a.openSplitTUN()
		case <-a.items.openImporter.ClickedCh:
			go a.openConfigImporter()
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
