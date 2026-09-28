package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// resolveSingbox finds the sing-box binary: explicit flag, then next to this
// executable, then PATH.
func resolveSingbox(explicit string) string {
	if explicit != "" {
		if fileExists(explicit) {
			return explicit
		}
		return ""
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "sing-box.exe")
		if fileExists(cand) {
			return cand
		}
	}
	if p, err := exec.LookPath("sing-box"); err == nil {
		return p
	}
	return ""
}

// writeTemp writes the config to a throwaway file and returns its path.
func writeTemp(cfg *singbox.Config) (string, error) {
	data, err := cfg.Marshal()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "config-importer-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// writeWithBackup writes the config atomically, keeping a .bak of the previous
// file when one exists.
func writeWithBackup(path string, cfg *singbox.Config) error {
	data, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if fileExists(path) {
		if err := copyFile(path, path+".bak"); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
