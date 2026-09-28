package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/merge"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/relay"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/remnawave"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/testrun"
)

// runTUI wraps the interactive flow so the terminal stays open on both success
// and failure, instead of closing instantly (which looks like a crash).
func runTUI() error {
	err := runTUIFlow()
	tuiHandled = true
	if err != nil {
		fmt.Println(failStyle.Render("Failed: " + err.Error()))
	} else {
		fmt.Println(okStyle.Render("Done."))
	}
	pauseWithMessage("Press any key to exit...")
	return err
}

// runTUIFlow is the interactive flow:
// source -> relay -> build/validate/test -> destination path -> merge & save.
func runTUIFlow() error {
	var source string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Source").
			Options(
				huh.NewOption("Remnawave", "remnawave"),
				huh.NewOption("Key url (vless/ss/trojan/...)", "key-url"),
			).
			Value(&source),
	)).Run(); err != nil {
		return err
	}

	var (
		url       string
		linksText string
		relayAddr string
	)
	switch source {
	case "remnawave":
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("Subscription URL").
				Placeholder("https://host/api/sub/XXXX").
				Value(&url),
			relayInput(&relayAddr),
		)).Run(); err != nil {
			return err
		}
		if strings.TrimSpace(url) == "" {
			return errors.New("subscription URL is required")
		}
	case "key-url":
		if err := huh.NewForm(huh.NewGroup(
			huh.NewText().
				Title("Links (vless:// ss:// trojan:// hy2:// ... , one per line)").
				Value(&linksText),
			relayInput(&relayAddr),
		)).Run(); err != nil {
			return err
		}
		if strings.TrimSpace(linksText) == "" {
			return errors.New("at least one link is required")
		}
	default:
		return fmt.Errorf("unknown source %q", source)
	}

	ctx := context.Background()
	client := remnawave.DefaultHTTPClient()

	src, err := buildSource(ctx, client, source, url, linksText)
	if err != nil {
		return err
	}
	if relayAddr != "" {
		fmt.Printf("relay: %s applied to %d outbound(s)\n", relayAddr, relay.Apply(src, relayAddr))
	} else {
		fmt.Println("relay: none (direct)")
	}

	sbPath := resolveSingbox("")
	if sbPath == "" {
		fmt.Println("sing-box not found; skipping validation and tests")
	} else {
		tmp, err := writeTemp(src)
		if err != nil {
			return err
		}
		defer os.Remove(tmp)
		if err := testrun.Validate(ctx, sbPath, tmp); err != nil {
			fmt.Println(failStyle.Render("validate: FAILED"))
			fmt.Println(err)
			return err
		}
		fmt.Println(okStyle.Render("validate: ok"))

		if _, err := runTestView(src.Outbounds(), testrun.Options{
			SingboxPath:   sbPath,
			DownloadLimit: 1 << 20,
		}); err != nil {
			fmt.Println(failStyle.Render("tests: " + err.Error()))
		}
	}

	dest := defaultDest(flagConfigDir)
	dform := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Destination config path").
			Placeholder(`C:\...\config.json`).
			Value(&dest).
			Validate(func(s string) error {
				s = strings.TrimSpace(s)
				if s == "" || strings.HasSuffix(s, "/") || strings.HasSuffix(s, `\`) {
					return errors.New("file name is required")
				}
				return nil
			}),
	))
	if err := dform.Run(); err != nil {
		return err
	}
	return saveInteractive(dest, src)
}

func relayInput(value *string) *huh.Input {
	return huh.NewInput().
		Title("Relay (host or IP, empty = direct)").
		Placeholder("relay.example.com").
		Value(value)
}

// saveInteractive merges into an existing config (resolving replace-key
// conflicts via a diff) or writes the config as-is when the file is new.
func saveInteractive(dest string, src *singbox.Config) error {
	if !fileExists(dest) {
		final, err := merge.Copy(src)
		if err != nil {
			return err
		}
		if err := writeWithBackup(dest, final); err != nil {
			return err
		}
		fmt.Println("saved:", dest)
		return noteSaved(dest)
	}

	raw, err := os.ReadFile(dest)
	if err != nil {
		return err
	}
	dst, err := singbox.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse destination: %w", err)
	}

	conflicts := merge.Conflicts(dst, src)
	rep := merge.Into(dst, src, merge.PolicyOutbounds)
	fmt.Printf("merge: +%d added, %d replaced, groups %v\n", len(rep.Added), len(rep.Replaced), rep.Groups)

	for _, c := range conflicts {
		d, err := resolveConflict(c)
		if err != nil {
			return err
		}
		switch d {
		case acceptB:
			dst.Root()[c.Key] = c.B
		case editManually:
			edited, err := editInEditor(dst)
			if err != nil {
				return err
			}
			dst = edited
			if err := writeWithBackup(dest, dst); err != nil {
				return err
			}
			fmt.Println("saved (edited):", dest)
			return noteSaved(dest)
		}
	}

	if err := writeWithBackup(dest, dst); err != nil {
		return err
	}
	fmt.Println("saved:", dest)
	return noteSaved(dest)
}

func noteSaved(dest string) error {
	if err := writeSavedNote(flagSavedNote, dest); err != nil {
		return fmt.Errorf("saved, but the tray was not notified: %w", err)
	}
	return nil
}
