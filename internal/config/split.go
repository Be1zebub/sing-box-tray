//go:build windows

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/Be1zebub/sing-box-tray/assets"
)

const splitTunFile = "split-tun.json"

const (
	// SplitBlacklist sends listed destinations and processes to direct.
	SplitBlacklist = "blacklist"
	// SplitWhitelist sends only the listed entries to route.final; everything
	// else goes direct.
	SplitWhitelist = "whitelist"
)

// SplitTUNPath is split-tun.json next to the tray executable.
func SplitTUNPath(exeDir string) string {
	return filepath.Join(exeDir, splitTunFile)
}

// SplitTUN is the list from split-tun.json, next to the tray executable.
// Blacklist entries leave the tunnel: ip_cidr is also added to the tun
// inbound's route_exclude_address, and all four lists become route rules to
// direct. Whitelist entries are routed to route.final instead, and a final
// 0.0.0.0/0 rule sends everything else direct; those IPs are not excluded
// from the adapter. Domains only match after a sniff action, which InjectTUN
// prepends when DomainSuffix is non-empty.
type SplitTUN struct {
	// Enabled is false only when the file sets "enabled": false. A missing
	// field, and a SplitTUN built in code, stays active.
	Enabled      bool     `json:"enabled"`
	Mode         string   `json:"mode"`
	IPCIDR       []string `json:"ip_cidr"`
	DomainSuffix []string `json:"domain_suffix"`
	ProcessName  []string `json:"process_name"`
	ProcessPath  []string `json:"process_path"`
}

// Active reports whether the lists should be injected. Mode "" is the
// in-code value used by tests and older callers; a file always has a mode.
func (s SplitTUN) Active() bool {
	if s.Mode == "" {
		return true
	}
	return s.Enabled
}

func (s SplitTUN) Whitelist() bool {
	return s.Mode == SplitWhitelist && s.Active()
}

// Empty reports whether the file adds nothing on top of the built-in bypasses.
func (s SplitTUN) Empty() bool {
	return len(s.IPCIDR) == 0 && len(s.DomainSuffix) == 0 && len(s.ProcessName) == 0 && len(s.ProcessPath) == 0
}

// LoadSplitTUN reads split-tun.json from exeDir. A missing file is created
// from the embedded empty template and loads as an empty list. A bare IP is
// accepted and stored as a host prefix (/32 or /128).
func LoadSplitTUN(exeDir string) (SplitTUN, error) {
	if err := ensureSplitTUNFile(exeDir); err != nil {
		return SplitTUN{}, err
	}
	data, err := os.ReadFile(filepath.Join(exeDir, splitTunFile))
	if err != nil {
		return SplitTUN{}, fmt.Errorf("read split-tun.json: %w", err)
	}
	var raw splitFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return SplitTUN{}, fmt.Errorf("parse split-tun.json: %w", err)
	}
	return normalizeSplit(raw)
}

// splitFile keeps Enabled as a pointer so a missing field means "on".
type splitFile struct {
	Enabled      *bool    `json:"enabled"`
	Mode         string   `json:"mode"`
	IPCIDR       []string `json:"ip_cidr"`
	DomainSuffix []string `json:"domain_suffix"`
	ProcessName  []string `json:"process_name"`
	ProcessPath  []string `json:"process_path"`
}

func ensureSplitTUNFile(exeDir string) error {
	path := filepath.Join(exeDir, splitTunFile)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat split-tun.json: %w", err)
	}
	if err := os.WriteFile(path, assets.DefaultSplitTUN, 0644); err != nil {
		return fmt.Errorf("write default split-tun.json: %w", err)
	}
	return nil
}

func normalizeSplit(in splitFile) (SplitTUN, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = SplitBlacklist
	}
	if mode != SplitBlacklist && mode != SplitWhitelist {
		return SplitTUN{}, fmt.Errorf("split-tun.json: mode must be %q or %q, got %q", SplitBlacklist, SplitWhitelist, in.Mode)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	cidrs, err := normalizeCIDRs(in.IPCIDR)
	if err != nil {
		return SplitTUN{}, err
	}
	domains, err := normalizeDomains(in.DomainSuffix)
	if err != nil {
		return SplitTUN{}, err
	}
	return SplitTUN{
		Enabled:      enabled,
		Mode:         mode,
		IPCIDR:       cidrs,
		DomainSuffix: domains,
		ProcessName:  dedupeTrim(in.ProcessName),
		ProcessPath:  dedupeTrim(in.ProcessPath),
	}, nil
}

func normalizeCIDRs(in []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		norm, err := normalizeCIDR(s)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out, nil
}

func normalizeCIDR(s string) (string, error) {
	if _, _, err := net.ParseCIDR(s); err == nil {
		return s, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return "", fmt.Errorf("split-tun.json: invalid ip_cidr %q", s)
	}
	if ip.To4() != nil {
		return ip.String() + "/32", nil
	}
	return ip.String() + "/128", nil
}

func normalizeDomains(in []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		s = strings.TrimPrefix(s, "*.")
		s = strings.Trim(s, ".")
		if s == "" || strings.ContainsAny(s, " /\\:@") {
			return nil, fmt.Errorf("split-tun.json: invalid domain_suffix %q", strings.TrimSpace(raw))
		}
		s = strings.ToLower(s)
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}

func dedupeTrim(in []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
