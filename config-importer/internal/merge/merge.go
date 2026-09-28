// Package merge folds an imported config into an existing one.
//
// Outbounds and groups are additive (dedup by tag); everything else
// (inbounds, dns, route, experimental, log) is a replace operation and is
// only touched under PolicyFull, where differing keys are reported as
// conflicts so the caller can render a diff and ask the user.
package merge

import (
	"encoding/json"
	"fmt"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

// Policy selects how non-outbound top-level keys are handled.
type Policy string

const (
	// PolicyOutbounds keeps the destination's inbounds/dns/route/etc. as-is.
	PolicyOutbounds Policy = "outbounds"
	// PolicyFull replaces differing top-level keys with the imported ones.
	PolicyFull Policy = "full"
)

// replaceKeys are top-level keys handled as replace (not append).
var replaceKeys = []string{"inbounds", "dns", "route", "experimental", "log"}

// Report describes what a merge did.
type Report struct {
	Added     []string // new outbound tags
	Replaced  []string // existing outbound tags overwritten
	Groups    []string // group tags that received new members
	Conflicts []string // top-level keys that differ (full policy)
	Final     string   // route.final after the merge, if set
}

// Into merges src into dest (in place) and reports the changes.
func Into(dest, src *singbox.Config, policy Policy) *Report {
	rep := &Report{}

	destOut := dest.Outbounds()
	index := make(map[string]int, len(destOut))
	for i, ob := range destOut {
		if tag := singbox.Tag(ob); tag != "" {
			index[tag] = i
		}
	}

	hasDirect := false
	for _, ob := range destOut {
		if singbox.Tag(ob) == "direct" {
			hasDirect = true
		}
	}

	var srcGroups []singbox.Obj
	for _, ob := range src.Outbounds() {
		t := singbox.Type(ob)
		tag := singbox.Tag(ob)
		switch {
		case singbox.IsGroup(t):
			srcGroups = append(srcGroups, ob)
		case singbox.IsConcrete(t):
			if tag == "" {
				continue
			}
			if i, ok := index[tag]; ok {
				destOut[i] = ob
				rep.Replaced = append(rep.Replaced, tag)
			} else {
				index[tag] = len(destOut)
				destOut = append(destOut, ob)
				rep.Added = append(rep.Added, tag)
			}
		case tag == "direct":
			hasDirect = true
		}
	}

	if !hasDirect {
		destOut = append(destOut, singbox.Obj{"type": "direct", "tag": "direct"})
	}

	// Groups: union members with an existing group of the same tag, else append.
	groupIndex := make(map[string]int)
	for i, ob := range destOut {
		if singbox.IsGroup(singbox.Type(ob)) {
			groupIndex[singbox.Tag(ob)] = i
		}
	}
	for _, g := range srcGroups {
		tag := singbox.Tag(g)
		if i, ok := groupIndex[tag]; ok {
			merged := unionMembers(singbox.Members(destOut[i]), singbox.Members(g))
			singbox.SetMembers(destOut[i], merged)
			rep.Groups = append(rep.Groups, tag)
			continue
		}
		groupIndex[tag] = len(destOut)
		destOut = append(destOut, g)
		rep.Groups = append(rep.Groups, tag)
	}

	dest.SetOutbounds(destOut)

	// Keep route.final pointing at a real group.
	rep.Final = ensureFinal(dest, src)

	// Replace-policy keys are copied only under PolicyFull. PolicyOutbounds
	// leaves them alone, including keys the destination does not have yet.
	for _, key := range replaceKeys {
		srcVal, srcOK := src.Root()[key]
		if !srcOK {
			continue
		}
		destVal, destOK := dest.Root()[key]
		if !destOK {
			if policy == PolicyFull {
				dest.Root()[key] = srcVal
			}
			continue
		}
		if jsonEqual(destVal, srcVal) {
			continue
		}
		rep.Conflicts = append(rep.Conflicts, key)
		if policy == PolicyFull {
			dest.Root()[key] = srcVal
		}
	}

	return rep
}

// ensureFinal sets route.final to a selector tag when the destination has none.
func ensureFinal(dest *singbox.Config, src *singbox.Config) string {
	route, _ := dest.Root()["route"].(singbox.Obj)
	if route == nil {
		route = singbox.Obj{}
		dest.Root()["route"] = route
	}
	if s, _ := route["final"].(string); s != "" {
		return s
	}
	final := firstSelectorTag(dest)
	if final == "" {
		final = firstSelectorTag(src)
	}
	if final != "" {
		route["final"] = final
	}
	return final
}

func firstSelectorTag(c *singbox.Config) string {
	for _, ob := range c.Outbounds() {
		if singbox.Type(ob) == "selector" {
			return singbox.Tag(ob)
		}
	}
	return ""
}

func unionMembers(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, m := range list {
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// AddToGroups adds tags to every selector/urltest group in cfg (dedup).
// Used when augmenting a fetched config with extra outbounds (e.g. hysteria2)
// that must also become selectable.
func AddToGroups(cfg *singbox.Config, tags []string) {
	if len(tags) == 0 {
		return
	}
	out := cfg.Outbounds()
	for _, ob := range out {
		if !singbox.IsGroup(singbox.Type(ob)) {
			continue
		}
		singbox.SetMembers(ob, unionMembers(singbox.Members(ob), tags))
	}
	cfg.SetOutbounds(out)
}

// Conflict describes a replace-policy key whose destination and source values
// differ; the caller decides how to resolve it.
type Conflict struct {
	Key string
	A   any // destination value
	B   any // imported value
}

// Conflicts lists replace-policy keys that differ, without modifying anything.
// Call it before Into so the values are still the originals.
func Conflicts(dest, src *singbox.Config) []Conflict {
	var out []Conflict
	for _, key := range replaceKeys {
		b, ok := src.Root()[key]
		if !ok {
			continue
		}
		a, ok := dest.Root()[key]
		if !ok || jsonEqual(a, b) {
			continue
		}
		out = append(out, Conflict{Key: key, A: a, B: b})
	}
	return out
}

// EnsureFinal exposes the route.final fixup for callers that merge manually.
func EnsureFinal(dest, src *singbox.Config) string { return ensureFinal(dest, src) }

// Copy returns a deep copy of src as a standalone config (used when the
// destination file does not exist yet).
func Copy(src *singbox.Config) (*singbox.Config, error) {
	data, err := src.Marshal()
	if err != nil {
		return nil, fmt.Errorf("copy config: %w", err)
	}
	return singbox.Parse(data)
}
