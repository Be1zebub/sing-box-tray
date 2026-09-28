package links

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

func parseVless(link string) (singbox.Obj, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("parse vless link: %w", err)
	}
	host, port, err := addrOf(u)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}
	if port == 0 {
		port = 443
	}
	uuid := ""
	if u.User != nil {
		uuid = u.User.Username()
	}
	if uuid == "" {
		return nil, fmt.Errorf("vless: missing uuid")
	}

	q := u.Query()
	ob := singbox.Obj{
		"type":        "vless",
		"tag":         tagOf(u, host, port),
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
	}
	if flow := q.Get("flow"); flow != "" {
		ob["flow"] = flow
	}
	if tls := buildTLS(q, host, "none"); tls != nil {
		ob["tls"] = tls
	}
	if tr := buildTransport(q); tr != nil {
		ob["transport"] = tr
	}
	return ob, nil
}

func parseTrojan(link string) (singbox.Obj, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("parse trojan link: %w", err)
	}
	host, port, err := addrOf(u)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}
	if port == 0 {
		port = 443
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
		if p, ok := u.User.Password(); ok {
			password += ":" + p
		}
	}
	if password == "" {
		return nil, fmt.Errorf("trojan: missing password")
	}

	q := u.Query()
	ob := singbox.Obj{
		"type":        "trojan",
		"tag":         tagOf(u, host, port),
		"server":      host,
		"server_port": port,
		"password":    password,
	}
	if tls := buildTLS(q, host, "tls"); tls != nil {
		ob["tls"] = tls
	}
	if tr := buildTransport(q); tr != nil {
		ob["transport"] = tr
	}
	return ob, nil
}

func parseVmess(link string) (singbox.Obj, error) {
	raw := strings.TrimPrefix(link, "vmess://")
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	decoded, err := b64Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("vmess: bad base64 payload: %w", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(decoded), &v); err != nil {
		return nil, fmt.Errorf("vmess: bad JSON payload: %w", err)
	}

	host := strOf(v["add"])
	if host == "" {
		return nil, fmt.Errorf("vmess: missing address")
	}
	port := atoiAny(v["port"])
	if port == 0 {
		port = 443
	}
	uuid := strOf(v["id"])
	if uuid == "" {
		return nil, fmt.Errorf("vmess: missing uuid")
	}
	tag := strOf(v["ps"])
	if tag == "" {
		tag = host + ":" + strconv.Itoa(port)
	}

	ob := singbox.Obj{
		"type":        "vmess",
		"tag":         tag,
		"server":      host,
		"server_port": port,
		"uuid":        uuid,
		"security":    defaultStr(strOf(v["scy"]), "auto"),
	}
	if aid := atoiAny(v["aid"]); aid != 0 {
		ob["alter_id"] = aid
	}

	if t := strings.ToLower(strOf(v["tls"])); t == "tls" || t == "reality" {
		tls := singbox.Obj{"enabled": true}
		if sni := strOf(v["sni"]); sni != "" {
			tls["server_name"] = sni
		}
		if alpn := strOf(v["alpn"]); alpn != "" {
			tls["alpn"] = splitCSV(alpn)
		}
		if fp := strOf(v["fp"]); fp != "" {
			tls["utls"] = singbox.Obj{"enabled": true, "fingerprint": fp}
		}
		if t == "reality" {
			if pbk := strOf(v["pbk"]); pbk != "" {
				tls["reality"] = singbox.Obj{"enabled": true, "public_key": pbk, "short_id": strOf(v["sid"])}
			}
		}
		ob["tls"] = tls
	}

	switch strings.ToLower(strOf(v["net"])) {
	case "ws":
		tr := singbox.Obj{"type": "ws", "path": defaultPath(strOf(v["path"]))}
		if h := strOf(v["host"]); h != "" {
			tr["headers"] = singbox.Obj{"Host": h}
		}
		ob["transport"] = tr
	case "grpc":
		ob["transport"] = singbox.Obj{"type": "grpc", "service_name": strOf(v["path"])}
	case "http", "h2":
		tr := singbox.Obj{"type": "http", "path": defaultPath(strOf(v["path"]))}
		if h := strOf(v["host"]); h != "" {
			tr["host"] = splitCSV(h)
		}
		ob["transport"] = tr
	}
	return ob, nil
}

func strOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}

func atoiAny(v any) int {
	n, _ := strconv.Atoi(strings.TrimSpace(strOf(v)))
	return n
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
