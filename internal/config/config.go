//go:build windows

package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Be1zebub/sing-box-tray/assets"
)

const (
	// trayConfigFile is the tray settings file created for a new install.
	// It is not a sing-box config. Sing-box configs live in config_dir
	// (default "configs"), so the shared name does not collide.
	trayConfigFile = "config.json"
	// legacyTrayConfigFile is the pre-layout name. It is still read when
	// config.json is absent or is a sing-box config rather than tray settings.
	legacyTrayConfigFile = "tray-config.json"
)

type TrayConfig struct {
	SingBoxPath        string            `json:"sing_box_path"`
	WintunDllPath      string            `json:"wintun_dll_path"`
	ConfigDir          string            `json:"config_dir"`
	SelectedConfig     string            `json:"selected_config"`
	SystemProxyInbound string            `json:"system_proxy_inbound"`
	Autostart          bool              `json:"autostart"`
	StartOnLaunch      bool              `json:"start_on_launch"`
	DefaultMode        string            `json:"default_mode"`
	LogLines           int               `json:"log_lines"`
	Language           string            `json:"language"`
	SystemProxy        SystemProxyConfig `json:"system_proxy"`
	TUN                TUNConfig         `json:"tun"`
	ClashAPI           ClashAPIConfig    `json:"clash_api"`

	// filePath is the tray settings file Load read or created. Save writes
	// back to it. Not serialized.
	filePath string
}

// SystemProxyConfig describes the default mixed inbound to inject into the
// sing-box config when system-proxy mode is selected and the base config has
// no http/mixed inbound of its own.
type SystemProxyConfig struct {
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`
}

type TUNConfig struct {
	InterfaceName       string   `json:"interface_name"`
	Address             []string `json:"address"`
	RouteAddress        []string `json:"route_address"`
	RouteExcludeAddress []string `json:"route_exclude_address"`
	MTU                 int      `json:"mtu"`
}

// ClashAPIConfig controls the sing-box Clash API (experimental.clash_api).
// tray-config.json is the source of truth: whatever the sing-box config itself
// carries is overwritten (or removed) when the run config is prepared, so a
// panel template can't silently enable a controller the user turned off.
type ClashAPIConfig struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"` // host:port -> external_controller
	Secret  string `json:"secret"` // optional, but recommended
	Yacd    bool   `json:"yacd"`   // serve the yacd web dashboard
}

func Load(exeDir string) (*TrayConfig, error) {
	path, err := TrayConfigPath(exeDir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if writeErr := os.WriteFile(path, assets.DefaultTrayConfig, 0644); writeErr != nil {
			return nil, fmt.Errorf("write default %s: %w", filepath.Base(path), writeErr)
		}
		data = assets.DefaultTrayConfig
	} else if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var cfg TrayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	cfg.filePath = path
	if cfg.LogLines <= 0 {
		cfg.LogLines = 200
	}
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = "off"
	}
	if cfg.Language == "" {
		cfg.Language = "auto"
	}
	if cfg.ClashAPI.Listen == "" {
		cfg.ClashAPI.Listen = "127.0.0.1:9090"
	}
	if !hasKey(data, "clash_api") {
		// Before clash_api became tray-config-owned, whatever the sing-box
		// config carried was used as-is. An absent key keeps that behaviour
		// instead of silently stripping the controller from a working setup;
		// setting "enabled": false explicitly turns it off.
		cfg.ClashAPI.Enabled = true
		cfg.ClashAPI.Yacd = true
	}
	if cfg.ConfigDir == "" && cfg.SelectedConfig == "" {
		// Migrate the pre-multi-config "config_path" field (a single file
		// path) so existing installs keep pointing at their real config
		// instead of falling back to the exe directory.
		var legacy struct {
			ConfigPath string `json:"config_path"`
		}
		if err := json.Unmarshal(data, &legacy); err == nil && legacy.ConfigPath != "" {
			cfg.ConfigDir = filepath.Dir(legacy.ConfigPath)
			cfg.SelectedConfig = filepath.Base(legacy.ConfigPath)
		}
	}
	if cfg.ConfigDir == "" {
		cfg.ConfigDir = "configs"
	}
	if cfg.SelectedConfig == "" {
		cfg.SelectedConfig = "config.json"
	}
	// Resolve relative paths against the exe directory so exec.Command
	// receives an absolute path (required since Go 1.19).
	cfg.SingBoxPath = absPath(exeDir, cfg.SingBoxPath)
	cfg.WintunDllPath = absPath(exeDir, cfg.WintunDllPath)
	cfg.ConfigDir = absPath(exeDir, cfg.ConfigDir)

	// Clash API secrets are generated here, on the load that first needs one,
	// and persisted immediately: the endpoint is loopback but machine-wide, so
	// without a secret any local process of any user could read connections or
	// switch the active outbound.
	if changed, err := cfg.EnsureClashSecret(); err != nil {
		return nil, err
	} else if changed {
		if err := cfg.Save(exeDir); err != nil {
			return nil, fmt.Errorf("save generated clash api secret: %w", err)
		}
	}

	if err := ensureSplitTUNFile(exeDir); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// EnsureClashSecret fills ClashAPI.Secret with a fresh random value when the
// API is enabled and no secret is set yet, and reports whether it changed
// anything so the caller can persist it. A secret already present is left
// alone — it is shared with whatever else the user points at the API (yacd).
func (c *TrayConfig) EnsureClashSecret() (bool, error) {
	if !c.ClashAPI.Enabled || c.ClashAPI.Secret != "" {
		return false, nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return false, fmt.Errorf("generate clash api secret: %w", err)
	}
	c.ClashAPI.Secret = hex.EncodeToString(buf)
	return true, nil
}

// hasKey reports whether the raw tray-config.json carries the given top-level
// key. Used to tell "field absent" apart from "field set to its zero value".
func hasKey(data []byte, key string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe[key]
	return ok
}

// FilePath is the tray settings file Load read or created.
func (c *TrayConfig) FilePath() string {
	return c.filePath
}

// TrayConfigPath picks the tray settings file. config.json wins when it
// actually contains tray settings (sing_box_path). An existing
// tray-config.json is used otherwise. A sing-box document that already
// occupies config.json is left alone, and the legacy filename is used so
// Load does not overwrite it.
func TrayConfigPath(exeDir string) (string, error) {
	next := filepath.Join(exeDir, trayConfigFile)
	legacy := filepath.Join(exeDir, legacyTrayConfigFile)
	nextIsTray, err := isTrayConfig(next)
	if err != nil {
		return "", err
	}
	if nextIsTray {
		return next, nil
	}
	legacyExists, err := fileExists(legacy)
	if err != nil {
		return "", err
	}
	if legacyExists {
		return legacy, nil
	}
	nextExists, err := fileExists(next)
	if err != nil {
		return "", err
	}
	if nextExists {
		return legacy, nil
	}
	return next, nil
}

func fileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !info.IsDir(), nil
}

func isTrayConfig(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return hasKey(data, "sing_box_path"), nil
}

// ActiveConfigPath returns the full path to the currently selected sing-box
// config file inside ConfigDir.
func (c *TrayConfig) ActiveConfigPath() string {
	return filepath.Join(c.ConfigDir, c.SelectedConfig)
}

// ListConfigFiles returns the base names of every *.json file directly inside
// dir (non-recursive), sorted alphabetically. tray-config.json and
// split-tun.json are excluded. config.json is not: inside config_dir it is
// a sing-box config. The tray settings file of that name lives next to the
// exe, outside config_dir.
func ListConfigFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read config dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.EqualFold(name, legacyTrayConfigFile) || strings.EqualFold(name, splitTunFile) ||
			!strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// absPath returns p as-is if it is already absolute, otherwise joins it
// with base. Empty strings are passed through unchanged.
func absPath(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// ReconcilePaths checks whether SingBoxPath, WintunDllPath, and the active
// sing-box config still exist on disk. A path can go stale if the install
// folder was moved while the tray settings still carry absolute paths from
// the old location. A missing path is repointed at the same file name next
// to the exe, then at deps/ (binaries) or configs/ (the selected sing-box
// config). changed reports whether any field was repointed (the caller
// should persist the config). missingSingBox, missingWintun, and
// missingConfig report whether that path is still unresolved after the
// fallback attempt (WintunDllPath is optional, so an empty value is never
// reported missing).
func (c *TrayConfig) ReconcilePaths(exeDir string) (changed, missingSingBox, missingWintun, missingConfig bool) {
	reconcile := func(current *string, extras ...string) bool {
		if *current == "" {
			return true
		}
		if _, err := os.Stat(*current); err == nil {
			return true
		}
		candidates := append([]string{filepath.Join(exeDir, filepath.Base(*current))}, extras...)
		for _, fallback := range candidates {
			if fallback == *current {
				continue
			}
			if _, err := os.Stat(fallback); err != nil {
				continue
			}
			*current = fallback
			changed = true
			return true
		}
		return false
	}

	missingSingBox = !reconcile(&c.SingBoxPath, filepath.Join(exeDir, "deps", filepath.Base(c.SingBoxPath)))
	missingWintun = !reconcile(&c.WintunDllPath, filepath.Join(exeDir, "deps", filepath.Base(c.WintunDllPath)))

	activePath := c.ActiveConfigPath()
	if _, err := os.Stat(activePath); err != nil {
		found := ""
		for _, fallback := range []string{
			filepath.Join(exeDir, c.SelectedConfig),
			filepath.Join(exeDir, "configs", c.SelectedConfig),
		} {
			if fallback == activePath {
				continue
			}
			if _, err := os.Stat(fallback); err != nil {
				continue
			}
			found = fallback
			break
		}
		if found == "" {
			missingConfig = true
		} else {
			c.ConfigDir = filepath.Dir(found)
			changed = true
		}
	}

	return changed, missingSingBox, missingWintun, missingConfig
}

func (c *TrayConfig) Save(exeDir string) error {
	path := c.filePath
	if path == "" {
		var err error
		path, err = TrayConfigPath(exeDir)
		if err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	return os.WriteFile(path, data, 0644)
}

// singBoxConfig is a partial representation of sing-box's config.json,
// used only to locate the proxy inbound address.
type singBoxConfig struct {
	Inbounds []singBoxInbound `json:"inbounds"`
}

type singBoxInbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`
}

// FindInboundAddr parses the sing-box config at path and returns the listen
// host and port for the inbound matching tag. If tag is empty, returns the
// first http or mixed inbound.
func FindInboundAddr(sbConfigPath, tag string) (host string, port int, err error) {
	data, err := os.ReadFile(sbConfigPath)
	if err != nil {
		return "", 0, fmt.Errorf("read sing-box config: %w", err)
	}
	var cfg singBoxConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", 0, fmt.Errorf("parse sing-box config: %w", err)
	}
	for _, ib := range cfg.Inbounds {
		if ib.Type != "http" && ib.Type != "mixed" {
			continue
		}
		if tag == "" || ib.Tag == tag {
			h := ib.Listen
			if h == "" {
				h = "127.0.0.1"
			}
			return h, ib.ListenPort, nil
		}
	}
	return "", 0, fmt.Errorf("no http/mixed inbound found (tag=%q)", tag)
}

// LoadRawSingBoxConfig reads the sing-box config at path as a generic map,
// preserving all fields so it can be selectively modified and re-marshaled.
func LoadRawSingBoxConfig(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sing-box config: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse sing-box config: %w", err)
	}
	return root, nil
}

func marshalConfig(root map[string]json.RawMessage) ([]byte, error) {
	merged, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal merged config: %w", err)
	}
	return merged, nil
}

// WriteRawSingBoxConfig marshals root to a temp file and returns its path.
func WriteRawSingBoxConfig(root map[string]json.RawMessage) (string, error) {
	merged, err := marshalConfig(root)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "sing-box-tray-*.json")
	if err != nil {
		return "", fmt.Errorf("create temp config: %w", err)
	}
	defer tmp.Close()
	if _, err := tmp.Write(merged); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp config: %w", err)
	}
	return tmp.Name(), nil
}

// patchClashAPI sets or removes experimental.clash_api in root according to
// cfg, leaving every other key (including other experimental entries such as
// cache_file) untouched.
func patchClashAPI(root map[string]json.RawMessage, cfg ClashAPIConfig) error {
	experimental := map[string]json.RawMessage{}
	if raw, ok := root["experimental"]; ok {
		if err := json.Unmarshal(raw, &experimental); err != nil {
			return fmt.Errorf("parse experimental: %w", err)
		}
	}

	if !cfg.Enabled {
		delete(experimental, "clash_api")
	} else {
		block := map[string]any{
			"external_controller": cfg.Listen,
			"default_mode":        "rule",
		}
		if cfg.Secret != "" {
			block["secret"] = cfg.Secret
		}
		if cfg.Yacd {
			block["external_ui"] = "yacd"
			block["external_ui_download_url"] = "https://github.com/MetaCubeX/Yacd-meta/archive/gh-pages.zip"
			block["external_ui_download_detour"] = "direct"
		}
		raw, err := json.Marshal(block)
		if err != nil {
			return fmt.Errorf("marshal clash_api: %w", err)
		}
		experimental["clash_api"] = json.RawMessage(raw)
	}

	if len(experimental) == 0 {
		delete(root, "experimental")
		return nil
	}
	raw, err := json.Marshal(experimental)
	if err != nil {
		return fmt.Errorf("marshal experimental: %w", err)
	}
	root["experimental"] = json.RawMessage(raw)
	return nil
}

// ApplyClashAPI rewrites the sing-box config at path in place so that its
// experimental.clash_api section matches cfg. tray-config.json is the source
// of truth here: a panel template may ship its own clash_api block, and it gets
// replaced or removed.
func ApplyClashAPI(path string, cfg ClashAPIConfig) error {
	root, err := LoadRawSingBoxConfig(path)
	if err != nil {
		return err
	}
	if err := patchClashAPI(root, cfg); err != nil {
		return err
	}
	merged, err := marshalConfig(root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// CopyWithClashAPI writes a temp copy of the sing-box config at srcPath with
// experimental.clash_api patched from cfg. Used in Off mode, which otherwise
// hands the user's config to sing-box untouched: the original must never be
// modified, but the clash_api override still has to apply.
func CopyWithClashAPI(srcPath string, cfg ClashAPIConfig) (string, error) {
	root, err := LoadRawSingBoxConfig(srcPath)
	if err != nil {
		return "", err
	}
	if err := patchClashAPI(root, cfg); err != nil {
		return "", err
	}
	return WriteRawSingBoxConfig(root)
}

// FilterInbounds parses the inbounds JSON array and returns only the entries
// whose "type" is one of keepTypes.
func FilterInbounds(raw json.RawMessage, keepTypes ...string) ([]json.RawMessage, error) {
	keep := make(map[string]bool, len(keepTypes))
	for _, t := range keepTypes {
		keep[t] = true
	}
	if raw == nil {
		return []json.RawMessage{}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse inbounds: %w", err)
	}
	out := items[:0]
	for _, item := range items {
		var t struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(item, &t)
		if keep[t.Type] {
			out = append(out, item)
		}
	}
	return out, nil
}

// InjectSystemProxy reads the sing-box config at sbConfigPath and strips any
// inbound that is not http/mixed (so a TUN inbound left over from the base
// config isn't run alongside the system proxy). If the base config already
// has an http/mixed inbound, it is kept as-is; otherwise a default mixed
// inbound built from cfg is appended. Writes the result to a temp file and
// returns its path.
func InjectSystemProxy(sbConfigPath string, cfg SystemProxyConfig) (string, error) {
	root, err := LoadRawSingBoxConfig(sbConfigPath)
	if err != nil {
		return "", err
	}

	inbounds, err := FilterInbounds(root["inbounds"], "http", "mixed")
	if err != nil {
		return "", err
	}

	if len(inbounds) == 0 {
		defaultRaw, err := json.Marshal(buildDefaultProxyInbound(cfg))
		if err != nil {
			return "", fmt.Errorf("marshal default proxy inbound: %w", err)
		}
		inbounds = append(inbounds, json.RawMessage(defaultRaw))
	}

	inboundsRaw, err := json.Marshal(inbounds)
	if err != nil {
		return "", fmt.Errorf("marshal inbounds: %w", err)
	}
	root["inbounds"] = inboundsRaw

	return WriteRawSingBoxConfig(root)
}

func buildDefaultProxyInbound(cfg SystemProxyConfig) map[string]any {
	tag := cfg.Tag
	if tag == "" {
		tag = "mixed-in"
	}
	listen := cfg.Listen
	if listen == "" {
		listen = "127.0.0.1"
	}
	port := cfg.ListenPort
	if port == 0 {
		port = 2080
	}
	return map[string]any{
		"type":        "mixed",
		"tag":         tag,
		"listen":      listen,
		"listen_port": port,
	}
}
