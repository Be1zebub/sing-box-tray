package links

import "testing"

func TestParseVlessReality(t *testing.T) {
	link := "vless://11111111-1111-1111-1111-111111111111@203.0.113.10:8443?encryption=none&flow=xtls-rprx-vision&type=tcp&security=reality&sni=www.example.com&fp=firefox&pbk=PUB&sid=SID#VlessRelay"
	obs, err := Parse(link)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ob := obs[0]
	if ob["type"] != "vless" || ob["tag"] != "VlessRelay" {
		t.Fatalf("unexpected: %#v", ob)
	}
	if ob["server"] != "203.0.113.10" || ob["server_port"] != 8443 {
		t.Errorf("server = %v:%v", ob["server"], ob["server_port"])
	}
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v", ob["flow"])
	}
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil || tls["server_name"] != "www.example.com" {
		t.Fatalf("tls = %#v", ob["tls"])
	}
	reality, _ := tls["reality"].(map[string]any)
	if reality == nil || reality["public_key"] != "PUB" || reality["short_id"] != "SID" {
		t.Errorf("reality = %#v", tls["reality"])
	}
}

func TestParseHysteria2HasNoUTLS(t *testing.T) {
	obs, err := Parse("hy2://secret@hy2.example.com:5443?sni=hy2.example.com&alpn=h3#Hy2")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ob := obs[0]
	if ob["type"] != "hysteria2" || ob["password"] != "secret" {
		t.Fatalf("unexpected: %#v", ob)
	}
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil || tls["server_name"] != "hy2.example.com" {
		t.Fatalf("tls = %#v", ob["tls"])
	}
	if _, ok := tls["utls"]; ok {
		t.Error("hysteria2 must not carry uTLS")
	}
}

func TestParseShadowsocks(t *testing.T) {
	// base64("chacha20-ietf-poly1305:password")
	obs, err := Parse("ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzd29yZA==@203.0.113.10:1234#SS")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ob := obs[0]
	if ob["type"] != "shadowsocks" || ob["method"] != "chacha20-ietf-poly1305" || ob["password"] != "password" {
		t.Fatalf("unexpected: %#v", ob)
	}
	if ob["server"] != "203.0.113.10" || ob["server_port"] != 1234 {
		t.Errorf("server = %v:%v", ob["server"], ob["server_port"])
	}
}

func TestParseTrojan(t *testing.T) {
	obs, err := Parse("trojan://pw@trojan.example.com:9443?type=tcp&security=tls&sni=trojan.example.com&fp=chrome#Troj")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ob := obs[0]
	if ob["type"] != "trojan" || ob["password"] != "pw" || ob["server_port"] != 9443 {
		t.Fatalf("unexpected: %#v", ob)
	}
}

func TestParseRejectsUnknownScheme(t *testing.T) {
	if _, err := Parse("garbage://x"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestParseMultiple(t *testing.T) {
	obs, err := Parse("vless://id@a:443?security=tls&sni=a#one\nss://YWVzLTI1Ni1nY206cA==@b:8388#two")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d outbounds, want 2", len(obs))
	}
}
