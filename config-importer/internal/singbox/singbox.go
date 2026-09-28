// Package singbox holds a minimal, schema-agnostic representation of a sing-box
// config: plain JSON objects, so merging/rewriting never needs the full schema.
package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Obj is a generic JSON object.
type Obj = map[string]any

// Config wraps the root JSON object of a sing-box config.
type Config struct {
	root Obj
}

// New returns an empty config.
func New() *Config { return &Config{root: Obj{}} }

// FromRoot wraps an existing object.
func FromRoot(root Obj) *Config { return &Config{root: root} }

// Parse decodes a JSON object. Numbers are kept as json.Number so they round-trip
// exactly (ports, etc.).
func Parse(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root Obj
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return &Config{root: root}, nil
}

// Root returns the underlying object.
func (c *Config) Root() Obj { return c.root }

// Marshal pretty-prints the config (4-space indent, no HTML escaping).
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "    ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c.root); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return buf.Bytes(), nil
}

// Outbounds returns the outbounds as objects (skipping malformed entries).
func (c *Config) Outbounds() []Obj {
	raw, ok := c.root["outbounds"].([]any)
	if !ok {
		return nil
	}
	out := make([]Obj, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(Obj); ok {
			out = append(out, m)
		}
	}
	return out
}

// SetOutbounds replaces the outbounds array.
func (c *Config) SetOutbounds(list []Obj) {
	arr := make([]any, len(list))
	for i, m := range list {
		arr[i] = m
	}
	c.root["outbounds"] = arr
}

// Str reads a string field.
func Str(m Obj, key string) string {
	s, _ := m[key].(string)
	return s
}

// Type returns the outbound type.
func Type(m Obj) string { return Str(m, "type") }

// Tag returns the outbound tag.
func Tag(m Obj) string { return Str(m, "tag") }

// IsGroup reports whether the outbound is a selector/urltest group.
func IsGroup(t string) bool { return t == "selector" || t == "urltest" }

// IsConcrete reports whether the outbound is a real proxy (not a group/direct/block).
func IsConcrete(t string) bool {
	switch t {
	case "", "selector", "urltest", "direct", "block", "dns":
		return false
	}
	return true
}

// Members returns the outbounds list of a group.
func Members(m Obj) []string {
	raw, ok := m["outbounds"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// SetMembers replaces a group's outbounds list.
func SetMembers(m Obj, list []string) {
	arr := make([]any, len(list))
	for i, s := range list {
		arr[i] = s
	}
	m["outbounds"] = arr
}
