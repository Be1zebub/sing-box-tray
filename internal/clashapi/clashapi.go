//go:build windows

// Package clashapi is a minimal client for the sing-box Clash API
// (experimental.clash_api): enough to list proxy groups and switch the
// selection of a selector/urltest group.
//
// Requests are written by hand over a loopback TCP socket instead of going
// through a standard HTTP client, because this fork forbids HTTP client imports
// anywhere in the tree (see CLAUDE.md). The peer is sing-box itself on
// 127.0.0.1, always plain HTTP/1.1 — every request sends Connection: close, so
// the response is read to EOF and chunked bodies are decoded by hand. This
// loopback socket is the only network the tray ever touches, and only while the
// user has the Clash API enabled.
package clashapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultTimeout = 5 * time.Second

// Client talks to one Clash API endpoint.
type Client struct {
	addr    string
	secret  string
	timeout time.Duration
}

// New returns a client for the given host:port and optional bearer secret.
func New(listen, secret string) *Client {
	return &Client{addr: listen, secret: secret, timeout: defaultTimeout}
}

// Group is a Clash API proxy group of a selectable kind.
type Group struct {
	Name string
	Type string // "Selector" or "URLTest"
	Now  string // currently selected member
	All  []string
}

func (c *Client) do(method, path string, body []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))

	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\n", method, path)
	fmt.Fprintf(&req, "Host: %s\r\n", c.addr)
	req.WriteString("Accept: application/json\r\n")
	req.WriteString("Connection: close\r\n")
	if c.secret != "" {
		fmt.Fprintf(&req, "Authorization: Bearer %s\r\n", c.secret)
	}
	if body != nil {
		req.WriteString("Content-Type: application/json\r\n")
		fmt.Fprintf(&req, "Content-Length: %d\r\n", len(body))
	}
	req.WriteString("\r\n")
	if body != nil {
		req.Write(body)
	}

	if _, err := conn.Write(req.Bytes()); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return parseResponse(raw)
}

// parseResponse validates the status line and returns the decoded body,
// de-chunking it when the server used Transfer-Encoding: chunked.
func parseResponse(raw []byte) ([]byte, error) {
	head, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		return nil, errors.New("malformed HTTP response")
	}
	lines := strings.Split(string(head), "\r\n")
	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("malformed status line %q", lines[0])
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("malformed status code %q", parts[1])
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("clash api returned HTTP %d", status)
	}

	chunked := false
	for _, l := range lines[1:] {
		name, value, found := strings.Cut(l, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), "Transfer-Encoding") &&
			strings.Contains(strings.ToLower(value), "chunked") {
			chunked = true
		}
	}
	if !chunked {
		return body, nil
	}
	return dechunk(body)
}

// dechunk decodes an HTTP/1.1 chunked body.
func dechunk(body []byte) ([]byte, error) {
	var out bytes.Buffer
	for {
		lineEnd := bytes.Index(body, []byte("\r\n"))
		if lineEnd < 0 {
			return nil, errors.New("truncated chunk header")
		}
		sizeField, _, _ := bytes.Cut(body[:lineEnd], []byte(";"))
		size, err := strconv.ParseInt(strings.TrimSpace(string(sizeField)), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("malformed chunk size %q", sizeField)
		}
		body = body[lineEnd+2:]
		if size == 0 {
			return out.Bytes(), nil
		}
		if int64(len(body)) < size {
			return nil, errors.New("truncated chunk")
		}
		out.Write(body[:size])
		body = body[size:]
		if len(body) < 2 {
			return nil, errors.New("truncated chunk terminator")
		}
		body = body[2:]
	}
}

// Groups returns the Selector and URLTest groups, sorted by name.
func (c *Client) Groups() ([]Group, error) {
	body, err := c.do("GET", "/proxies", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Proxies map[string]struct {
			Type string   `json:"type"`
			Now  string   `json:"now"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse /proxies: %w", err)
	}

	groups := make([]Group, 0, len(payload.Proxies))
	for name, p := range payload.Proxies {
		if p.Type != "Selector" && p.Type != "URLTest" {
			continue
		}
		groups = append(groups, Group{Name: name, Type: p.Type, Now: p.Now, All: p.All})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return groups, nil
}

// Select switches the selection of group to name.
func (c *Client) Select(group, name string) error {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return fmt.Errorf("marshal selection: %w", err)
	}
	if _, err := c.do("PUT", "/proxies/"+url.PathEscape(group), body); err != nil {
		return err
	}
	return nil
}
