package links

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

func parseShadowsocks(link string) (singbox.Obj, error) {
	body := strings.TrimPrefix(link, "ss://")

	name := ""
	if i := strings.IndexByte(body, '#'); i >= 0 {
		name, _ = url.QueryUnescape(body[i+1:])
		body = body[:i]
	}
	query := ""
	if i := strings.IndexByte(body, '?'); i >= 0 {
		query = body[i+1:]
		body = body[:i]
	}

	var userinfo, hostport string
	if at := strings.LastIndexByte(body, '@'); at >= 0 {
		userinfo, hostport = body[:at], body[at+1:]
	} else {
		decoded, err := b64Decode(body)
		if err != nil {
			return nil, fmt.Errorf("ss: bad base64 payload: %w", err)
		}
		at := strings.LastIndexByte(decoded, '@')
		if at < 0 {
			return nil, fmt.Errorf("ss: malformed payload")
		}
		userinfo, hostport = decoded[:at], decoded[at+1:]
	}

	if strings.Contains(userinfo, "%") {
		if unescaped, err := url.QueryUnescape(userinfo); err == nil {
			userinfo = unescaped
		}
	}
	creds := userinfo
	if !strings.Contains(creds, ":") {
		if d, err := b64Decode(userinfo); err == nil {
			creds = d
		}
	}
	method, password := splitUserPass(creds)
	if method == "" {
		return nil, fmt.Errorf("ss: missing method")
	}

	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("ss: invalid port %q", portStr)
	}

	tag := name
	if tag == "" {
		tag = host + ":" + strconv.Itoa(port)
	}
	ob := singbox.Obj{
		"type":        "shadowsocks",
		"tag":         tag,
		"server":      host,
		"server_port": port,
		"method":      method,
		"password":    password,
	}
	if query != "" {
		q, _ := url.ParseQuery(query)
		if plugin := q.Get("plugin"); plugin != "" {
			parts := strings.SplitN(plugin, ";", 2)
			ob["plugin"] = parts[0]
			if len(parts) > 1 {
				ob["plugin_opts"] = parts[1]
			}
		}
	}
	return ob, nil
}

func parseHysteria2(link string) (singbox.Obj, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("parse hysteria2 link: %w", err)
	}
	host, port, err := addrOf(u)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}
	if port == 0 {
		port = 443
	}
	auth := ""
	if u.User != nil {
		auth = u.User.Username()
		if p, ok := u.User.Password(); ok {
			auth += ":" + p
		}
	}
	if auth == "" {
		return nil, fmt.Errorf("hysteria2: missing auth")
	}

	q := u.Query()
	ob := singbox.Obj{
		"type":        "hysteria2",
		"tag":         tagOf(u, host, port),
		"server":      host,
		"server_port": port,
		"password":    auth,
	}
	tls := singbox.Obj{"enabled": true}
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	if sni != "" {
		tls["server_name"] = sni
	}
	if alpn := q.Get("alpn"); alpn != "" {
		tls["alpn"] = splitCSV(alpn)
	}
	if truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")) {
		tls["insecure"] = true
	}
	ob["tls"] = tls

	if obfs := q.Get("obfs-password"); obfs != "" {
		ob["obfs"] = singbox.Obj{"type": "salamander", "password": obfs}
	}
	return ob, nil
}

func parseSocks(link string) (singbox.Obj, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("parse socks link: %w", err)
	}
	host, port, err := addrOf(u)
	if err != nil {
		return nil, fmt.Errorf("socks: %w", err)
	}
	if port == 0 {
		port = 1080
	}
	ob := singbox.Obj{
		"type":        "socks",
		"tag":         tagOf(u, host, port),
		"server":      host,
		"server_port": port,
		"version":     "5",
	}
	if u.User != nil {
		if username := u.User.Username(); username != "" {
			ob["username"] = username
		}
		if pass, ok := u.User.Password(); ok {
			ob["password"] = pass
		}
	}
	return ob, nil
}

func parseHTTP(link string) (singbox.Obj, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("parse http link: %w", err)
	}
	host, port, err := addrOf(u)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	secure := strings.EqualFold(u.Scheme, "https")
	if port == 0 {
		if secure {
			port = 443
		} else {
			port = 80
		}
	}
	ob := singbox.Obj{
		"type":        "http",
		"tag":         tagOf(u, host, port),
		"server":      host,
		"server_port": port,
	}
	if u.User != nil {
		if username := u.User.Username(); username != "" {
			ob["username"] = username
		}
		if pass, ok := u.User.Password(); ok {
			ob["password"] = pass
		}
	}
	if secure {
		tls := singbox.Obj{"enabled": true}
		if sni := u.Query().Get("sni"); sni != "" {
			tls["server_name"] = sni
		}
		ob["tls"] = tls
	}
	return ob, nil
}
