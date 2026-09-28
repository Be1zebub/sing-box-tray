package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/merge"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

type decision int

const (
	keepA decision = iota
	acceptB
	editManually
)

// resolveConflict renders a diff and asks the user how to resolve it.
func resolveConflict(c merge.Conflict) (decision, error) {
	fmt.Println()
	fmt.Println(titleStyle.Render("Conflict: " + c.Key))
	for _, line := range diffLines(prettyJSON(c.A), prettyJSON(c.B)) {
		switch {
		case strings.HasPrefix(line, "- "):
			fmt.Println("  " + failStyle.Render(line))
		case strings.HasPrefix(line, "+ "):
			fmt.Println("  " + okStyle.Render(line))
		default:
			fmt.Println("  " + dimStyle.Render(line))
		}
	}

	var choice string
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("Resolve %q", c.Key)).
			Options(
				huh.NewOption("Keep current (A)", "a"),
				huh.NewOption("Accept imported (B)", "b"),
				huh.NewOption("Open in editor", "e"),
			).
			Value(&choice),
	))
	if err := form.Run(); err != nil {
		return keepA, err
	}
	switch choice {
	case "b":
		return acceptB, nil
	case "e":
		return editManually, nil
	default:
		return keepA, nil
	}
}

// editInEditor opens cfg in $EDITOR and returns the re-parsed result.
func editInEditor(cfg *singbox.Config) (*singbox.Config, error) {
	data, err := cfg.Marshal()
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "config-importer-edit-*.json")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "notepad"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("editor: %w", err)
	}

	raw, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return nil, err
	}
	return singbox.Parse(raw)
}

func prettyJSON(v any) []string {
	data, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return []string{fmt.Sprintf("%v", v)}
	}
	return strings.Split(string(data), "\n")
}

// diffLines is a minimal LCS diff over two line slices.
func diffLines(a, b []string) []string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, "  "+a[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+a[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+b[j])
	}
	return out
}
