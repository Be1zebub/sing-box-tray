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

	"github.com/Be1zebub/sing-box-tray-runner/assets"
)

const splitTunFile = "split-tun.json"

// SplitTUNPath is split-tun.json next to the tray executable.
func SplitTUNPath(exeDir string) string {
	return filepath.Join(exeDir, splitTunFile)
}

// SplitTUN is the bypass list from split-tun.json, next to the tray executable.
// Every entry leaves the tunnel: ip_cidr is also added to the tun inbound's
// route_exclude_address (Windows never delivers those destinations to WinTun),
// and all four lists become sing-box route rules to the direct outbound.
// Domains only match after a sniff action, which InjectTUN prepends when
// DomainSuffix is non-empty.
type SplitTUN struct {
	IPCIDR       []string `json:"ip_cidr"`
	DomainSuffix []string `json:"domain_suffix"`
	ProcessName  []string `json:"process_name"`
	ProcessPath  []string `json:"process_path"`
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
	var split SplitTUN
	if err := json.Unmarshal(data, &split); err != nil {
		return SplitTUN{}, fmt.Errorf("parse split-tun.json: %w", err)
	}
	return normalizeSplit(split)
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

func normalizeSplit(in SplitTUN) (SplitTUN, error) {
	cidrs, err := normalizeCIDRs(in.IPCIDR)
	if err != nil {
		return SplitTUN{}, err
	}
	domains, err := normalizeDomains(in.DomainSuffix)
	if err != nil {
		return SplitTUN{}, err
	}
	return SplitTUN{
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
