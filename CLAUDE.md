# sing-box-tray

Windows-only tray launcher for sing-box. Fork of `soksanichenko/sing-box-tray-runner`.
Module path: `github.com/Be1zebub/sing-box-tray`. No Linux port.

The fork exists to have **no updater and no network**. Do not add `net/http`, GitHub calls,
or self-replacement of the running exe. `sing-box.exe` and `wintun.dll` are installed by hand.
A missing dependency is reported, never downloaded. `tun.EnsureWintunDll` only copies a local file.

## Build

```sh
make build
# or: ./scripts/build.sh | scripts\build.ps1
```

`VERSION`, when set, is passed as
`-X github.com/Be1zebub/sing-box-tray/internal/version.Version=$VERSION`.
A `-X` for a symbol that does not exist is ignored and the build still succeeds.
`rsrc.syso` embeds `app.manifest` (Common Controls v6) and the exe icon. Without the manifest,
`lxn/walk` windows fail silently. Regenerate with
`rsrc -manifest app.manifest -ico assets/icons/working.ico -o rsrc.syso`.

## Do not break

**Single instance.** Mutex `Global\SingBoxTray`. On UAC relaunch the old process must
`CloseHandle` the mutex *before* `os.Exit` (`os.Exit` skips defers). `releaseMutex` is passed
`main` → `tray.NewApp`. TUN without admin calls `RelaunchAsAdmin --force-mode=tun`, then releases
the mutex and exits. No sleep.

**Icons.** Tray icons are ICO (`assets/icons/{idling,working,error}.ico`). systray loads them with
`LoadImage(LR_LOADFROMFILE)`; PNG is ignored. `lxn/walk` windows need `runtime.LockOSThread` on the
creating goroutine. Set each window's `Icon` from `appicon.Icon()` via a `walk.Image` variable:
a typed-nil `*walk.Icon` does not hit `declarative.ImageFrom`'s `case nil` and can panic.

**Configs on disk.** New installs use `config.json` next to the exe (tray settings, not a
sing-box config). `config.Load` still reads `tray-config.json` when `config.json` is missing
or is a sing-box document, and never overwrites that sing-box file. Paths are made absolute
in `config.Load` (Go rejects relative paths in `exec.Command`). Defaults are `deps/sing-box.exe`,
`deps/wintun.dll`, and `config_dir` `configs`. The user's sing-box config is never modified; TUN
and system-proxy modes rewrite a temp file and delete it on stop. `ListConfigFiles` skips
`tray-config.json` and `split-tun.json`, not `config.json`. `config.Load` migrates a legacy
`config_path` into `config_dir`/`selected_config`. `sing-box-tray.log` is capped at 1 MiB;
past that the oldest bytes are dropped. `scripts/install.ps1` and `scripts/install.sh` download
releases and copy `assets/tray-config.default.json` and `assets/split-tun.default.json` (from the
checkout, or from raw `main` when the script is piped). The tray itself still has no network.

**Config submenu.** systray cannot remove items; `rebuildConfigMenu` hides the old ones. Those items
own their `ClickedCh` goroutines.

**Opening files from an elevated tray.** `ShellExecuteW` fails with access denied. Open a file with
`cmd /c start`, a folder with `explorer.exe`.

**Injection.** `prepareConfig` keeps only the inbound type for the active mode. An existing matching
inbound is kept; a default is appended only when none exists.
- TUN (`InjectTUN`): also sets `route.auto_detect_interface: true` (without it Windows routes are
  wrong and browsers miss the tunnel) and prepends route rules. Direct rules use `action: "route"`;
  a bare top-level `outbound` is deprecated.
- The injected tun address must include IPv6 (default `fdfe:dcba:9876::1/126`). With `strict_route`
  and no IPv6 address, sing-tun blocks all outbound IPv6, including `::1`. `route_address` stays
  IPv4-only (`0.0.0.0/1`, `128.0.0.0/1`); an IPv6 prefix there installs an IPv6 default route and
  direct IPv6 can loop back into the tunnel.
- System proxy (`InjectSystemProxy`): `http`/`mixed` only.

**Split tunnel** (`split-tun.json`, next to the exe, empty template on first `config.Load`). Read
again on every TUN start. Not read in system-proxy mode. WinTun does not filter; it is only the
packet device.
- Blacklist `ip_cidr` is appended to the tun inbound's `route_exclude_address` (those
  destinations never enter the adapter) and also emitted as an `ip_cidr` → `direct` rule.
  Whitelist `ip_cidr` is a route rule to `route.final`. Any `route_exclude_address` prefix
  that overlaps a whitelist CIDR is removed so the packet can enter the adapter. An empty
  whitelist still installs the direct catch-all and does not require `route.final`. A bare
  IP becomes `/32` or `/128`.
- `process_name`, `process_path`, and `domain_suffix` are route rules to `direct` in blacklist
  mode and to `route.final` in whitelist mode. The packet enters the adapter, then leaves via
  the physical NIC. Domains need a prepended `{action: "sniff"}`. `*.example.com` is stored as
  `example.com`. `process_path` is case-sensitive and must match the path sing-box reports
  (`C:\Windows\System32\curl.exe`, not `C:\WINDOWS\...`).
- Blacklist prepend order: optional sniff, `ip_is_private` → direct, `sing-box.exe` → direct,
  then the split rules. Whitelist prepend order: optional sniff, `sing-box.exe` → direct, the
  listed rules → `route.final`, then `0.0.0.0/0` and `::/0` → direct. That catch-all is in
  front of the user's own rules, so they do not match IP traffic. `route_exclude_address` is
  the only TUN-specific part; the route rules would also match on a mixed inbound.

**Autostart.** `toggleAutostart` is the only writer. It acts on `autostart.IsEnabled()`, not on a
requested bool. `cfg.Autostart` is a write-only mirror and can be stale; startup and the
Autostart menu item read `IsEnabled()`. Non-elevated uses `HKCU\...\Run`. Elevated (TUN) uses Task Scheduler
`/RL HIGHEST`. Enabling one removes the other if present. `schtasks.exe` is spawned with
`CREATE_NO_WINDOW` (`0x08000000`).

**Clash API.** `tray-config.json` overwrites whatever `experimental.clash_api` the sing-box config
had. An absent `clash_api` key keeps it enabled; `"enabled": false` turns it off. An empty secret
is generated on load and saved. The active node is `proxies.GLOBAL.now` (type `Fallback`) walked
through Selector/URLTest `now`. A missing `GLOBAL` does not fall back to the first selector.
`direct`/`block`/`dns` are not delay-probed when they are the default outbound itself.

**Other races and conventions.** `pendingStart`: `start()` during `StateStopping` sets the flag;
`stop()` calls `start()` when it finishes. UI strings live in `assets/locales/{en,ru,ua}.json`.
Language names and the Languages label are literals, not translated. `a.log(...)` stays English.
Every retitled menu item must be stored on `App.items`. CI lint needs `GOOS=windows`. Do not
"fix" the `errcheck` ignores for `Close`/`Call`, or enable `ST1001` (walk's dot-import is intended).
