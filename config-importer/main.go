// Command config-importer builds a sing-box config from a Remnawave
// subscription (or, later, raw proxy links), optionally routes it through an
// L4 relay, validates and tests it over a local SOCKS inbound, then merges the
// result into a destination config file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/merge"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/relay"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/remnawave"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/testrun"
)

// tuiHandled suppresses the duplicate stderr line when the interactive flow
// has already shown the error on screen.
var tuiHandled bool

// Set when the tray launches the importer. configDir prefills the save path;
// savedNote receives the absolute path after a successful save.
var (
	flagConfigDir string
	flagSavedNote string
)

func main() {
	if err := run(); err != nil {
		if !tuiHandled {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func run() error {
	var (
		source        = flag.String("source", "", "source: remnawave | key-url")
		subURL        = flag.String("url", "", "Remnawave subscription URL")
		linksFlag     = flag.String("links", "", "raw proxy links for key-url (space/newline separated)")
		linksFile     = flag.String("links-file", "", "file with proxy links for key-url")
		relayAddr     = flag.String("relay", "", "relay host to route through (empty = direct)")
		dest          = flag.String("dest", "", "destination config path")
		policy        = flag.String("policy", "outbounds", "merge policy: outbounds | full")
		singboxPath   = flag.String("sing-box", "", "path to the sing-box binary")
		noTest        = flag.Bool("no-test", false, "skip connectivity/speed tests")
		latencyURL    = flag.String("latency-url", "https://www.gstatic.com/generate_204", "latency probe URL")
		downloadURL   = flag.String("download-url", "https://proof.ovh.net/files/10Mb.dat", "download test URL")
		downloadLimit = flag.Int64("download-limit", 0, "max bytes downloaded per outbound (0 = whole file)")
	)
	flag.StringVar(&flagConfigDir, "config-dir", "", "directory prefilled as the save location")
	flag.StringVar(&flagSavedNote, "saved-note", "", "file that receives the saved config path")
	flag.Parse()

	if *source == "" {
		return runTUI()
	}
	if *dest == "" {
		return errors.New("--dest is required")
	}

	linksText := *linksFlag
	if *linksFile != "" {
		data, err := os.ReadFile(*linksFile)
		if err != nil {
			return err
		}
		linksText = string(data)
	}

	switch *source {
	case "remnawave":
		if *subURL == "" {
			return errors.New("--url is required for source remnawave")
		}
	case "key-url":
		if strings.TrimSpace(linksText) == "" {
			return errors.New("--links or --links-file is required for source key-url")
		}
	default:
		return fmt.Errorf("unknown source %q", *source)
	}

	ctx := context.Background()
	client := remnawave.DefaultHTTPClient()

	src, err := buildSource(ctx, client, *source, *subURL, linksText)
	if err != nil {
		return err
	}

	if n := relay.Apply(src, *relayAddr); *relayAddr != "" {
		fmt.Printf("relay: %s applied to %d outbound(s)\n", *relayAddr, n)
	} else {
		fmt.Println("relay: none (direct)")
	}

	final, err := applyDest(*dest, src, *policy)
	if err != nil {
		return err
	}

	sbPath := resolveSingbox(*singboxPath)
	if sbPath != "" {
		tmp, err := writeTemp(final)
		if err != nil {
			return err
		}
		defer os.Remove(tmp)
		if err := testrun.Validate(ctx, sbPath, tmp); err != nil {
			fmt.Println("validate: FAILED")
			return err
		}
		fmt.Println("validate: ok")
	} else {
		fmt.Println("validate: skipped (sing-box binary not found)")
	}

	if !*noTest {
		if sbPath == "" {
			return errors.New("cannot run tests: sing-box binary not found")
		}
		fmt.Println("testing outbounds (SOCKS only, system route untouched):")
		_, err := testrun.Run(ctx, final.Outbounds(), testrun.Options{
			SingboxPath:   sbPath,
			LatencyURL:    *latencyURL,
			DownloadURL:   *downloadURL,
			DownloadLimit: *downloadLimit,
		}, printResult)
		if err != nil {
			return err
		}
	}

	if err := writeWithBackup(*dest, final); err != nil {
		return err
	}
	fmt.Printf("saved: %s\n", *dest)
	return noteSaved(*dest)
}

// applyDest merges src into an existing destination file, or copies it when the
// destination does not exist yet.
func applyDest(dest string, src *singbox.Config, policy string) (*singbox.Config, error) {
	if !fileExists(dest) {
		fmt.Println("dest: new file, writing full config")
		return merge.Copy(src)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		return nil, err
	}
	dst, err := singbox.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse destination: %w", err)
	}
	rep := merge.Into(dst, src, merge.Policy(policy))
	fmt.Printf("merge: +%d added, %d replaced, groups %v, final %q\n",
		len(rep.Added), len(rep.Replaced), rep.Groups, rep.Final)
	if len(rep.Conflicts) > 0 {
		fmt.Printf("merge: conflicting keys kept from destination (policy %q): %v\n", policy, rep.Conflicts)
	}
	return dst, nil
}

func printResult(r testrun.Result) {
	if r.OK {
		fmt.Printf("  %-40s %6dms  %8s/s  %s\n",
			r.Tag, r.Latency.Milliseconds(), humanBytes(int64(r.Speed)), humanBytes(r.Bytes))
		return
	}
	fmt.Printf("  %-40s FAIL  %s\n", r.Tag, r.Err)
}
