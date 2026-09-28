package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/links"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/merge"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/remnawave"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

const importedTag = "Imported"

// buildSource produces the imported config for the chosen source.
//
// remnawave: the sing-box subscription (SS/VLESS/Trojan) plus hysteria2
// appended from the Happ response, since the sing-box template excludes it.
//
// key-url: sing-box outbounds parsed from raw proxy links, grouped under a
// selector so they behave like the Remnawave groups.
func buildSource(ctx context.Context, client *http.Client, source, url, linksText string) (*singbox.Config, error) {
	switch source {
	case "remnawave":
		cfg, err := remnawave.SingboxConfig(ctx, client, url)
		if err != nil {
			return nil, err
		}
		hy, err := remnawave.HysteriaOutbounds(ctx, client, url)
		if err != nil {
			fmt.Printf("hysteria2: not available (%v)\n", err)
			return cfg, nil
		}
		tags := appendOutbounds(cfg, hy)
		merge.AddToGroups(cfg, tags)
		fmt.Printf("hysteria2: added %v\n", tags)
		return cfg, nil

	case "key-url":
		outbounds, err := links.Parse(linksText)
		if err != nil {
			return nil, err
		}
		cfg := singbox.New()
		tags := appendOutbounds(cfg, outbounds)
		out := cfg.Outbounds()
		out = append(out,
			singbox.Obj{"type": "selector", "tag": importedTag, "outbounds": stringsToAny(tags)},
			singbox.Obj{"type": "direct", "tag": "direct"},
		)
		cfg.SetOutbounds(out)
		cfg.Root()["route"] = singbox.Obj{"final": importedTag}
		fmt.Printf("links: parsed %d outbound(s)\n", len(tags))
		return cfg, nil

	default:
		return nil, fmt.Errorf("unknown source %q", source)
	}
}

// appendOutbounds adds outbounds to cfg and returns their tags.
func appendOutbounds(cfg *singbox.Config, add []singbox.Obj) []string {
	out := cfg.Outbounds()
	tags := make([]string, 0, len(add))
	for _, ob := range add {
		out = append(out, ob)
		tags = append(tags, singbox.Tag(ob))
	}
	cfg.SetOutbounds(out)
	return tags
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
