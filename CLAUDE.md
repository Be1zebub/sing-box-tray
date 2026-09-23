# sing-box-tray — project notes for Claude

Windows-only system tray launcher for sing-box.

**This is a fork.** The defining difference from upstream (`soksanichenko/sing-box-tray-runner`):
**there is no updater and no network code at all.** `sing-box.exe` and `wintun.dll` are installed
by hand. Do not reintroduce `net/http`, GitHub API calls, or self-replacement of the running exe —
that was the whole point of forking. The Go module path is
`github.com/Be1zebub/sing-box-tray-runner`.

There is no Linux port planned; the code is Windows-only by design.

## Build

Windows-only project, cross-compiled from Linux:

```sh
make build
# or, without `make`:
./scripts/build.sh    # Linux/macOS/WSL
scripts\build.ps1     # native Windows (PowerShell)
# or manually:
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -ldflags="-H windowsgui -s -w" \
  -o build/sing_box_tray_runner.exe .
```

`scripts/build.sh`/`build.ps1` accept an optional `VERSION` env var, appended as
`-ldflags -X github.com/Be1zebub/sing-box-tray-runner/internal/version.Version=$VERSION` —
`release.yml` sets it from the pushed tag; unset (dev builds) leaves `version.Version` at its
`"dev"` default. **A `-X` against a symbol that doesn't exist is silently ignored** (the build
still succeeds), so if a version ever stops showing up, check that this path matches the real
module path.

`rsrc.syso` in the repo root is a pre-generated Windows resource object that embeds `app.manifest`
(Common Controls v6 dependency) and `assets/icons/working.ico` (the exe's own icon, shown by
Explorer/taskbar pinning — separate from any individual window's icon, see **Window icons** below).
The Go toolchain links it automatically. Without the manifest, `lxn/walk` windows fail silently.
Regenerate with:

```sh
~/go/bin/rsrc -manifest app.manifest -ico assets/icons/working.ico -o rsrc.syso
```

## Architecture

```
main.go               — mutex, UAC elevation for TUN, systray.Run
internal/
  tray/tray.go        — App struct: orchestrates everything, handles menu clicks, opens config file/folder
  state/state.go      — thread-safe AppState × ProxyMode state machine with subscriptions
  process/process.go  — sing-box child process: Start/Stop/watch (crash detection)
  config/config.go    — tray-config.json load/save; FindInboundAddr parses sing-box config
  tun/tun.go          — InjectTUN: builds temp config with TUN inbound + route rules injected
  proxy/proxy.go      — Windows system proxy via registry + WinInet flush
  elevation/          — IsElevated, RelaunchAsAdmin (ShellExecuteW "runas")
  autostart/          — HKCU Run key for normal autostart, Task Scheduler (schtasks.exe, CREATE_NO_WINDOW) for elevated (TUN) autostart
  logbuf/             — circular log buffer, file mirror with timestamps, subscriptions
  appicon/            — shared *walk.Icon for the lxn/walk windows (Settings/Log/About), loaded from a temp-file copy of assets.IconGrey
  logwin/             — lxn/walk log viewer window
  settings/           — lxn/walk settings window
  aboutwin/           — lxn/walk About window (version info + clickable repo link)
  watcher/            — polls os.Stat every 2s for config file changes, and the config folder's file listing for added/removed configs
  version/            — Version var, overridden via -ldflags at release build time
  i18n/               — UI string catalog (en/ru/ua), OS locale detection, live menu retitling
assets/
  icons.go            — embeds grey/green/red tray-state ICO files (assets/icons/*.ico) via go:embed
  defaults.go         — embeds tray-config.default.json
  locales.go          — embeds locales/*.json (loaded by internal/i18n)
scripts/
  build.sh            — build wrapper for Linux/macOS/WSL hosts
  build.ps1           — build wrapper for native Windows hosts (no `make` needed)
.github/
  workflows/ci.yml      — golangci-lint + build matrix (ubuntu-latest, windows-latest)
  workflows/release.yml — on `v*` tag push: builds and publishes a GitHub Release
  workflows/codeql.yml  — CodeQL static analysis (CI only, not shipped)
  dependabot.yml        — weekly gomod + github-actions update PRs
```

## Key decisions and constraints

**No network, manual dependencies**: nothing in the tree imports `net/http` or performs outbound
requests. This is enforced by review, not by tooling — a regression here defeats the fork. The
first-run check only *tells* the user where a missing `sing-box.exe`/`wintun.dll` has to go
(`checkFirstRunDeps` in `tray.go`); it never downloads anything.

**Single instance**: named kernel mutex `Global\SingBoxTray`. On UAC re-launch, the existing
process must call `CloseHandle` on the mutex _before_ `os.Exit(0)` — `os.Exit` skips defers. The
`releaseMutex` func is passed from `main` → `tray.NewApp`.

**Tray icons**: `getlantern/systray` on Windows calls `LoadImage(LR_LOADFROMFILE, IMAGE_ICON)`.
PNG bytes are silently ignored. Icons must be ICO format — `assets/icons/{idling,working,error}.ico`
are real multi-resolution (16/32/48/256) artwork, embedded via `go:embed` in `assets/icons.go`.

**lxn/walk windows**: require `runtime.LockOSThread()` on the goroutine that creates them, and a
Windows manifest with Common Controls v6 (`rsrc.syso`). Without the manifest the window creation
fails silently.

**Window icons**: `lxn/walk`'s window-class registration does `LoadIcon(hInst, MAKEINTRESOURCE(7))`
as its class-icon fallback — a convention from an older `rsrc` default — but this project's
`rsrc.syso` assigns resource IDs in embed order (manifest gets 1, then the icon group gets 2), so
ID 7 never resolves and Windows falls back to the generic system icon for every `lxn/walk` window.
Fix: `internal/appicon` (`Icon() *walk.Icon`) explicitly sets each window's `Icon` property
(`MainWindow{Icon: ...}`), which drives `WM_SETICON` directly. `walk.NewIconFromFile` re-reads from
disk per DPI, so it can't take embedded `[]byte` — `appicon.Icon()` writes `assets.IconGrey` to a
fixed path in `os.TempDir()` once (`sync.Once`) and loads from there; the resulting `*walk.Icon` is
shared across all three windows. Callers assign into a `var winIcon walk.Image` (interface-typed)
guarded by `if ic := appicon.Icon(); ic != nil`, not a bare `*walk.Icon`: `declarative.ImageFrom`'s
`case nil:` only catches a true untyped nil interface, so a typed-nil `*walk.Icon` would fall
through to `case Image` and could panic.

**TUN mode**: requires elevation. When TUN is selected without admin rights, `RelaunchAsAdmin`
spawns an elevated instance with `--force-mode=tun`. The non-elevated instance releases the mutex
immediately then exits (no sleep).

**Per-mode inbound injection**: `prepareConfig` (`internal/tray/tray.go`) filters the user's
sing-box `config.json` down to only the inbound type relevant to the selected mode, so a config
that defines both a proxy and a TUN inbound doesn't run both at once. Both paths use the shared
helpers in `internal/config/config.go` (`LoadRawSingBoxConfig`, `FilterInbounds`,
`WriteRawSingBoxConfig`) to rewrite a temp file — the original `config.json` is never modified. If
a matching inbound already exists it's kept (split-tun IP excludes are still appended to its
`route_exclude_address`); only if none is found is a default one built and appended.
- **TUN** (`internal/tun/tun.go`, `InjectTUN`): keeps only `tun`-type inbounds, appending a default
  one built from `tray-config.json`'s `tun.*` fields if none exists. Also injects
  `route.auto_detect_interface: true` (required — without it sing-box cannot build Windows routing
  table entries and TUN captures no browser traffic) and prepends route rules (see **TUN IPv6 /
  routing** and **Split tunnel** below). All three address lists are overridable from `tun.*`.
- **System proxy** (`config.InjectSystemProxy`): keeps only `http`/`mixed`-type inbounds, appending
  a default `mixed` inbound from `tray-config.json`'s `system_proxy.*` fields if none exists.
- Both write the result to `os.TempDir()`; the temp file is deleted on stop/crash.

**TUN IPv6 / routing** (`internal/tun/tun.go`): the injected tun inbound always carries an **IPv6
address** alongside the IPv4 one (`tun.address` defaults to
`["172.19.0.1/30", "fdfe:dcba:9876::1/126"]`). This is load-bearing, not cosmetic: with
`strict_route: true` and **no** IPv6 address, sing-tun installs an unconditional WFP block filter
on `FWPM_LAYER_ALE_AUTH_CONNECT_V6`, which blocks all outbound IPv6 including `::1` — and anything
that resolves `localhost` to IPv6 (Node/Vite, Next, …) becomes unreachable while TUN is up. Related
rules, all in the injected config:
- `route_address` is deliberately **IPv4-only** (`0.0.0.0/1`, `128.0.0.0/1`). Putting IPv6 here
  would make `auto_route` install an IPv6 default route, and direct IPv6 traffic could then re-enter
  the tunnel (sing-box#4178 loop).
- `route_exclude_address` covers loopback, RFC1918 and link-local (`127.0.0.0/8`, `::1/128`,
  `10/8`, `172.16/12`, `192.168/16`, `169.254/16`, `fc00::/7`, `fe80::/10`, `ff00::/8`), so LAN and
  loopback traffic never enter the tunnel at all.
- `route.rules` gets prepended entries, in this order: optional `{action: "sniff"}` (only when
  `split-tun.json` lists domains), then `{ip_is_private: true, action: "route", outbound: "direct"}`,
  then `{process_name: ["sing-box.exe"], action: "route", outbound: "direct"}` (keeps sing-box's own
  connections from being re-captured by the TUN interface), then the split-tun bypasses. Direct
  rules use the explicit `action: "route"` form — the bare top-level `outbound` form is deprecated.

**Split tunnel** (`split-tun.json`, next to the exe, written empty on first `config.Load` if
missing): bypass list read again on every TUN start. WinTun itself does none of this — it is only
the packet device. `ip_cidr` is appended to the tun inbound's `route_exclude_address` (Windows
routing, those destinations never enter the adapter) and also emitted as an `ip_cidr` → `direct`
rule. `process_name` / `process_path` / `domain_suffix` are sing-box route rules to `direct`
(process owner and sniffed SNI/Host — the packet does enter the adapter, then leaves via the
physical NIC). A bare IP is stored as `/32` or `/128`. `*.example.com` is stored as `example.com`.
`ListConfigFiles` ignores this file the same way it ignores `tray-config.json`. The tray menu
item `menu_open_split_tun` opens it (recreating the empty template if it was deleted). Empty lists add
nothing. Covered by `internal/config/split_test.go` and `internal/tun/tun_test.go`.

All of the above is covered by `internal/tun/tun_test.go` (pure JSON transform, no admin needed).

**Absolute paths**: Go 1.19+ refuses relative paths in `exec.Command`. All paths from
`tray-config.json` are resolved to absolute at load time in `config.Load`.

**`schtasks.exe`**: all calls use `CREATE_NO_WINDOW` (`SysProcAttr{CreationFlags: 0x08000000}`) to
prevent console flash on startup.

**Stop→Start race**: `pendingStart bool` in `App`. If `start()` is called while state is
`StateStopping`, it sets the flag and returns; `stop()` checks the flag after completing and calls
`start()`.

**Config directory + Config submenu**: `config_dir`/`selected_config` (replacing the old single
`config_path`) let a folder hold multiple sing-box configs, switchable without touching Settings.
`config.Load` migrates a legacy `config_path` into `config_dir`/`selected_config` on first load, so
existing installs keep pointing at their real config instead of silently falling back to the exe
directory. `config.ListConfigFiles` scans `config_dir` non-recursively for `*.json`, excluding
`tray-config.json` and `split-tun.json` (relevant since the default `config_dir` is `.`, the exe
directory, where both also live). `a.buildConfigItems(parent, dir)` (`tray.go`) does the
scan-and-populate: it logs `config dir %s: found %d config file(s): %v` unconditionally so a folder
that unexpectedly yields zero files is diagnosable, adds one checkable submenu item per file found,
each running its own `for range item.ClickedCh` goroutine (`getlantern/systray`'s
`AddSubMenuItemCheckbox`/`ClickedCh` are safe from any goroutine — unlike every other fixed-size
submenu (Mode, Languages), which are folded into the single `select` in `handleClicks`; Config
can't be, since the item count depends on the folder), and enables/disables the parent based on
whether anything was found. `OnReady` calls it once; `rebuildConfigMenu` (called from `openSettings`
when `ConfigDir` changed) calls it again against the new folder — old items are `Hide()`-den first
since `getlantern/systray` has no item-removal API, then a fresh set is built and re-enables the
parent if the new folder has files. Clicking a config entry calls `switchConfig`, which mirrors
`switchMode`: persists the choice, restarts sing-box live if it was running, and restarts the
config-file watcher (`restartConfigWatcher`) so it tracks the newly active file. The Settings window
config picker is fed the *same* `configNames` slice the tray submenu was last built from (passed
into `settings.Show`), rather than re-scanning disk, so the two can never disagree about what's
available *at the moment Settings was opened*. `ListConfigFiles` matches `*.json` and excludes
`tray-config.json` case-insensitively (`strings.EqualFold`). The folder itself is live-watched too:
`restartConfigDirWatcher` wraps a `watcher.DirWatcher` that polls `config.ListConfigFiles(dir)` every
2s and calls `rebuildConfigMenu` when the name list changed — so dropping in a new `*.json` (or
removing one) updates the tray submenu without a restart. `DirWatcher` diffs sorted name slices, not
mtimes, which is why it's a separate type from `Watcher`.
- **Gotcha**: `settings.configIndex` must return `-1`, not `0`, when the scanned config list is
  empty. `lxn/walk`'s declarative `ComboBox{CurrentIndex: 0}` on a model with zero items makes
  `walk.ComboBox.SetCurrentIndex(0)` fail (Win32 `CB_SETCURSEL` on an empty combo can't select
  index 0), which fails the whole `MainWindow{}.Create()` call — and `runWindow` swallows that error
  silently, so the entire Settings window fails to open with no error shown. `-1` is always valid.

**Open config file / folder** (`tray.go`): two tray items — `openActiveConfig` (verb `"open"`) and
`openConfigDir` (verb `"explore"`), both through the shared `a.shellOpen(path, verb)` helper, which
calls `ShellExecuteW` from `shell32.dll` and only logs a failure (a stale path simply opens nothing).
Same call shape `aboutwin` uses for its repo link, just different verbs.

**First-run dependency check** (`tray.go`'s `checkFirstRunDeps`, called synchronously in `OnReady`
before `start_on_launch` is honored): first calls `config.ReconcilePaths(exeDir)`, which re-checks
`SingBoxPath`, `WintunDllPath`, and the active config against disk and, for any that no longer
exist, looks for a same-named file directly in `exeDir` — this recovers automatically when the tray
(and its companion files) were moved/reinstalled into a new folder while `tray-config.json` still
carries absolute paths from the old one; any field it repoints gets saved back. Whatever
`ReconcilePaths` couldn't resolve is then **reported, never downloaded**: a missing
`sing_box_path`/`wintun_dll_path` logs the path and shows a dialog naming exactly where the file has
to be placed; a missing config shows a warning naming the expected path. `tun.EnsureWintunDll` (used
at TUN start) copies a **local** file next to `sing-box.exe` — no network.

**Linting** (`.golangci.yml`): CI runs `golangci-lint` with `GOOS=windows` (the whole codebase is
behind `//go:build windows`, so without it nothing gets analyzed). `errcheck` excludes ignoring the
error from `Close()`/`Call()` on file handles, registry keys, `*walk.FormBase`, and Win32 `LazyProc`
calls — established convention throughout this codebase, not something to "fix" file-by-file.
`staticcheck`'s `ST1001` (no dot-imports) is disabled because `. "github.com/lxn/walk/declarative"`
is that library's intended usage; `ST1000` (package doc comments) is disabled because it isn't this
project's convention.

**About menu item**: `showAbout` (`tray.go`) shows `version.Version` (this build) plus a clickable
link to `repoURL` — a single constant in `tray.go` (`https://github.com/Be1zebub/sing-box-tray-runner`),
so the link can't drift. Displayed via `internal/aboutwin`, a small `lxn/walk` window (same
`MainWindow`/singleton-instance pattern as `logwin`/`settings`) rather than `infoBox`, since a plain
`MessageBoxW` can't contain a clickable link: the repo URL is a `walk.LinkLabel` (Win32 SysLink)
whose `OnLinkActivated` opens it via `ShellExecuteW` with the `"open"` verb.

**Autostart backend** (`internal/autostart/autostart.go`): `Enable(elevated bool)` picks between two
mechanisms depending on whether the current proxy mode needs admin rights. Non-elevated autostart
writes an `HKCU\...\Run` value (`registry.CURRENT_USER`, same package `internal/proxy` already uses
for the system-proxy registry keys) — a standard user always has write access to their own HKCU
hive, so this can't hit the "Access is denied" failure `schtasks /Create` can produce in locked-down
environments (group policy or EDR software restricting Task Scheduler). Elevated autostart (TUN
default mode) still goes through Task Scheduler with `/RL HIGHEST`, since that's the only mechanism
that can launch with an administrator token at logon without popping an interactive UAC prompt —
`HKCU\Run` always launches at the user's normal integrity level. `Enable` opportunistically removes
whichever mechanism it's *not* using (only if that one is present) so toggling never leaves two
autostart entries launching the app twice; `IsEnabled`/`Disable` check and act on both mechanisms.

**Autostart toggle** (`tray.go`): `toggleAutostart` is the single source of truth for calling
`autostart.Enable`/`Disable` and syncing `a.items.autostart`'s checkbox + `cfg.Autostart`; it always
acts on whatever `autostart.IsEnabled()` currently reports, never a passed-in target state, and
surfaces a failed call via `infoBox`. Both the tray checkbox (checked at `OnReady` from
`autostart.IsEnabled()`, since `cfg.Autostart` is a write-only mirror that can go stale) and the
Settings checkbox call this same function: `openSettings` captures `prevAutostart :=
autostart.IsEnabled()` before showing the dialog, passes it into `settings.Show` so the checkbox
reflects real state, and in the save callback calls `a.toggleAutostart()` exactly once if
`updated.Autostart != prevAutostart`. After that call the callback overwrites `updated.Autostart`
with a fresh `autostart.IsEnabled()` read and syncs the tray checkbox from it before saving — so a
failed enable/disable can't leave `tray-config.json` claiming a state that was never applied.

**Localization** (`internal/i18n/i18n.go`): `Strings` covers every UI-facing string (tray menu,
tooltips, toast/dialog text, Settings/Log window chrome), embedded per-language as
`assets/locales/{en,ru,ua}.json` and loaded at `init()`. `tray-config.json`'s `language` field is
resolved at `main.go` startup (`i18n.Resolve`, falling back to `i18n.Detect()` which reads
`GetUserDefaultUILanguage` via kernel32) and passed into `tray.NewApp`. The tray **Languages**
submenu and the Settings language dropdown both switch it live (no restart) via `App.applyLanguage`,
which recomputes `a.strs` and calls `refreshMenuTexts()` — this relies on `getlantern/systray`'s
`(*MenuItem).SetTitle`/`SetTooltip`, so every menu item whose text is translated must be stored on
`App.items` even if never clicked (e.g. the "Mode" submenu parent), specifically so it can be
retitled later. Proper nouns (`sing-box-tray`, `sing-box`) and the language names/picker label
itself are literal constants, never routed through `Strings` — a language picker translated into a
language the user doesn't want is unfindable. Per the global logging convention, `a.log(...)` calls
are deliberately **not** localized and stay in English always.

## tray-config.json fields

| Field | Default | Notes |
|---|---|---|
| `sing_box_path` | `sing-box.exe` | resolved to absolute on load |
| `wintun_dll_path` | `wintun.dll` | copied to sing-box dir if missing |
| `config_dir` | `.` | folder scanned for `*.json` sing-box configs; resolved to absolute on load |
| `selected_config` | `config.json` | base sing-box config file name inside `config_dir`, not modified — see `TrayConfig.ActiveConfigPath` |
| `system_proxy_inbound` | `""` | tag of the http/mixed inbound; empty = first found |
| `autostart` | `false` | write-only mirror of the autostart entry (registry Run key or Task Scheduler task), set by `toggleAutostart`; actual state on startup comes from `autostart.IsEnabled()`, not this field |
| `default_mode` | `tun` | `off` / `system_proxy` / `tun` |
| `start_on_launch` | `false` | auto-start sing-box when tray starts |
| `log_lines` | `200` | circular log buffer size |
| `language` | `auto` | `auto` / `en` / `ru` / `ua` — UI language |
| `system_proxy.tag` | `mixed-in` | tag for the auto-generated default mixed inbound |
| `system_proxy.listen` | `127.0.0.1` | |
| `system_proxy.listen_port` | `2080` | |
| `tun.interface_name` | `singbox-tun` | |
| `tun.address` | `["172.19.0.1/30", "fdfe:dcba:9876::1/126"]` | must include an IPv6 CIDR — see **TUN IPv6 / routing** |
| `tun.route_address` | `["0.0.0.0/1", "128.0.0.0/1"]` | IPv4-only on purpose |
| `tun.route_exclude_address` | loopback + RFC1918 + link-local | kept out of the tunnel |
| `tun.mtu` | `9000` | |
