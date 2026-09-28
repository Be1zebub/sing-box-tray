// Package links turns raw proxy links (vless://, vmess://, trojan://, ss://,
// hysteria2://, socks://) into sing-box outbounds.
package links

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

// Parse converts a blob of links (separated by whitespace or newlines) into
// sing-box outbounds. Any malformed or unsupported line aborts the whole parse.
func Parse(text string) ([]singbox.Obj, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil, fmt.Errorf("no links provided")
	}
	out := make([]singbox.Obj, 0, len(fields))
	for _, raw := range fields {
		ob, err := parseOne(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, ob)
	}
	return out, nil
}

func parseOne(link string) (singbox.Obj, error) {
	scheme := strings.ToLower(strings.SplitN(link, "://", 2)[0])
	switch scheme {
	case "vless":
		return parseVless(link)
	case "vmess":
		return parseVmess(link)
	case "trojan":
		return parseTrojan(link)
	case "ss":
		return parseShadowsocks(link)
	case "hysteria2", "hy2":
		return parseHysteria2(link)
	case "socks", "socks5":
		return parseSocks(link)
	case "http", "https":
		return parseHTTP(link)
	default:
		return nil, fmt.Errorf("unsupported link scheme %q", scheme)
	}
}

// addrOf returns the host and port of a parsed link (port may be 0).
func addrOf(u *url.URL) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("missing server address")
	}
	port := 0
	if p := u.Port(); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, fmt.Errorf("invalid port %q", p)
		}
		port = v
	}
	return host, port, nil
}

// tagOf builds an outbound tag from the link fragment, falling back to host:port.
func tagOf(u *url.URL, host string, port int) string {
	if u.Fragment != "" {
		return u.Fragment
	}
	return host + ":" + strconv.Itoa(port)
}

// buildTLS maps the common TLS query parameters. def is the security to assume
// when the link does not specify one ("none" for vless, "tls" for trojan).
func buildTLS(q url.Values, host, def string) singbox.Obj {
	security := q.Get("security")
	if security == "" {
		security = def
	}
	if security != "tls" && security != "reality" {
		return nil
	}
	tls := singbox.Obj{"enabled": true}

	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	if sni == "" {
		sni = host
	}
	tls["server_name"] = sni

	if alpn := q.Get("alpn"); alpn != "" {
		tls["alpn"] = splitCSV(alpn)
	}
	if fp := q.Get("fp"); fp != "" {
		tls["utls"] = singbox.Obj{"enabled": true, "fingerprint": fp}
	}
	if truthy(q.Get("allowInsecure")) {
		tls["insecure"] = true
	}
	if security == "reality" {
		pbk := q.Get("pbk")
		if pbk == "" {
			return nil
		}
		reality := singbox.Obj{"enabled": true, "public_key": pbk}
		if sid := q.Get("sid"); sid != "" {
			reality["short_id"] = sid
		}
		tls["reality"] = reality
	}
	return tls
}

// buildTransport maps the "type" query parameter to a sing-box transport.
func buildTransport(q url.Values) singbox.Obj {
	switch strings.ToLower(q.Get("type")) {
	case "ws":
		tr := singbox.Obj{"type": "ws", "path": defaultPath(q.Get("path"))}
		if h := q.Get("host"); h != "" {
			tr["headers"] = singbox.Obj{"Host": h}
		}
		return tr
	case "grpc":
		return singbox.Obj{"type": "grpc", "service_name": q.Get("serviceName")}
	case "http", "h2":
		tr := singbox.Obj{"type": "http", "path": defaultPath(q.Get("path"))}
		if h := q.Get("host"); h != "" {
			tr["host"] = splitCSV(h)
		}
		return tr
	case "httpupgrade":
		tr := singbox.Obj{"type": "httpupgrade", "path": defaultPath(q.Get("path"))}
		if h := q.Get("host"); h != "" {
			tr["host"] = h
		}
		return tr
	default:
		return nil
	}
}

func defaultPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func splitCSV(s string) []any {
	parts := strings.Split(s, ",")
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truthy(v string) bool {
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// b64Decode decodes standard or URL-safe base64 with optional padding.
func b64Decode(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// splitUserPass splits "user:pass" on the first colon.
func splitUserPass(s string) (string, string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}
