//go:build windows && live

package tun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Be1zebub/sing-box-tray/internal/config"
)

// Live check of the split rules on a localhost mixed proxy. No TUN, no admin,
// no changes to the Windows routing table. route_exclude_address is TUN-only
// and is not covered here: an excluded IP still arrives at sing-box because
// the client chose the proxy, and the ip_cidr rule sends it to direct.
//
// scripts/test-split-tun.ps1 is the entry point.

const (
	liveProxyAddr = "127.0.0.1:18080"
	liveClashAddr = "127.0.0.1:19090"
	liveClashAuth = "split-tun-live"

	liveBypassIP  = "1.1.1.1"
	liveProcessIP = "1.0.0.1"
	liveBlackhole = "9.9.9.9"
	liveDomain    = "example.com"
)

func TestLiveSplitTUN(t *testing.T) {
	singBox := os.Getenv("SINGBOX_PATH")
	if singBox == "" {
		if os.Getenv("SPLIT_TUN_LIVE_REQUIRED") == "1" {
			t.Fatal("SINGBOX_PATH is required")
		}
		t.Skip("SINGBOX_PATH is required")
	}
	if err := listenFree(liveProxyAddr); err != nil {
		t.Fatal(err)
	}
	if err := listenFree(liveClashAddr); err != nil {
		t.Fatal(err)
	}

	art := os.Getenv("SPLIT_TUN_ARTIFACTS")
	if art == "" {
		art = filepath.Join("build", "split-tun-live", time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatalf("artifact dir: %v", err)
	}
	t.Logf("artifacts: %s", art)

	// sing-box compares process_path against QueryFullProcessImageName, which
	// uses the on-disk casing (C:\Windows\...) rather than %SystemRoot%
	// (C:\WINDOWS\...). A case-sensitive mismatch misses the rule.
	curl := `C:\Windows\System32\curl.exe`
	if _, err := os.Stat(curl); err != nil {
		t.Fatalf("curl.exe: %v", err)
	}

	work := filepath.Join(art, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	splitRaw, err := json.MarshalIndent(map[string]any{
		"ip_cidr":       []string{liveBypassIP},
		"domain_suffix": []string{liveDomain},
		"process_name":  []string{},
		"process_path":  []string{curl},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "split-tun.json"), splitRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(art, "split-tun.json"), splitRaw, 0o644)
	split, err := config.LoadSplitTUN(work)
	if err != nil {
		t.Fatalf("load split-tun.json: %v", err)
	}

	basePath := filepath.Join(art, "base-config.json")
	if err := os.WriteFile(basePath, []byte(liveBaseConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := config.LoadRawSingBoxConfig(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := injectSelfBypassRule(root, "sing-box.exe", split); err != nil {
		t.Fatalf("inject rules: %v", err)
	}
	injected, err := config.WriteRawSingBoxConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(injected) })
	if err := config.ApplyClashAPI(injected, config.ClashAPIConfig{
		Enabled: true,
		Listen:  liveClashAddr,
		Secret:  liveClashAuth,
	}); err != nil {
		t.Fatalf("clash api: %v", err)
	}
	injectedRaw, err := os.ReadFile(injected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "injected-config.json"), injectedRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	check := exec.Command(singBox, "check", "-c", injected)
	if out, err := check.CombinedOutput(); err != nil {
		_ = os.WriteFile(filepath.Join(art, "check-failed.log"), out, 0o644)
		t.Fatalf("sing-box check: %v\n%s", err, out)
	}

	logf, err := os.Create(filepath.Join(art, "sing-box.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()

	cmd := exec.Command(singBox, "run", "-c", injected)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sing-box: %v", err)
	}
	if job, jobErr := bindKillOnClose(uint32(cmd.Process.Pid)); jobErr != nil {
		t.Logf("job object: %s", jobErr)
	} else {
		defer windows.CloseHandle(job)
	}
	stopped := false
	stop := func() {
		if stopped || cmd.Process == nil {
			return
		}
		stopped = true
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(stop)

	if err := waitReady(15 * time.Second); err != nil {
		t.Fatalf("sing-box did not come up: %v", err)
	}

	// Dial results are recorded, but the pass/fail is the outbound sing-box
	// actually picked. A running TUN on the machine can refuse the socket
	// after the rule already matched, and closed flows disappear from the
	// Clash connection list.
	_, _ = proxyConnect(liveProxyAddr, net.JoinHostPort(liveBypassIP, "443"), 8*time.Second)
	_, _ = proxyConnect(liveProxyAddr, liveDomain+":443", 12*time.Second)
	_, _ = proxyConnect(liveProxyAddr, net.JoinHostPort(liveBlackhole, "443"), 4*time.Second)
	curlCmd := exec.Command(curl,
		"-x", "http://"+liveProxyAddr,
		"--connect-timeout", "5",
		"--max-time", "8",
		"-k", "-s", "-o", "NUL",
		"https://"+liveProcessIP+"/",
	)
	curlCmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	_, _ = curlCmd.CombinedOutput()

	time.Sleep(300 * time.Millisecond)
	logRaw, err := os.ReadFile(filepath.Join(art, "sing-box.log"))
	if err != nil {
		t.Fatal(err)
	}
	logText := stripANSI(string(logRaw))
	_ = os.WriteFile(filepath.Join(art, "sing-box.plain.log"), []byte(logText), 0o644)

	var probes []probeResult
	record := func(name, outbound, dest string) {
		ok := strings.Contains(logText, "outbound/"+outbound+": outbound connection to "+dest)
		probes = append(probes, probeResult{Name: name, OK: ok, Detail: outbound + " -> " + dest})
	}
	record("ip-rule-direct", "direct[direct]", liveBypassIP+":443")
	record("domain-rule-direct", "direct[direct]", liveDomain+":443")
	record("blackhole-not-bypassed", "socks[blackhole]", liveBlackhole+":443")
	record("process-path-direct", "direct[direct]", liveProcessIP+":443")

	rawProbes, _ := json.MarshalIndent(probes, "", "  ")
	_ = os.WriteFile(filepath.Join(art, "probes.json"), rawProbes, 0o644)
	for _, p := range probes {
		if !p.OK {
			t.Errorf("probe %s failed: %s", p.Name, p.Detail)
		} else {
			t.Logf("probe %s ok: %s", p.Name, p.Detail)
		}
	}
}

type probeResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func listenFree(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%s is already in use: %w", addr, err)
	}
	return ln.Close()
}

func proxyConnect(proxy, target string, timeout time.Duration) (int, error) {
	conn, err := net.DialTimeout("tcp", proxy, timeout)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if n == 0 {
		return 0, err
	}
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return 0, fmt.Errorf("proxy status %q", strings.TrimSpace(line))
	}
	code, convErr := strconv.Atoi(fields[1])
	if convErr != nil {
		return 0, convErr
	}
	if code != 200 {
		return code, fmt.Errorf("proxy status %d", code)
	}
	return code, nil
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

func clashGet(path string) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", liveClashAddr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "GET %s HTTP/1.0\r\nHost: %s\r\nAuthorization: Bearer %s\r\n\r\n", path, liveClashAddr, liveClashAuth)
	raw, err := io.ReadAll(conn)
	if err != nil {
		return nil, err
	}
	idx := bytes.Index(raw, []byte("\r\n\r\n"))
	if idx < 0 {
		return nil, fmt.Errorf("clash response has no body: %s", raw)
	}
	status, _, _ := strings.Cut(string(raw[:idx]), "\n")
	body := raw[idx+4:]
	if !strings.Contains(status, "200") {
		return body, fmt.Errorf("%s", strings.TrimSpace(status))
	}
	return body, nil
}

func waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if _, last = clashGet("/version"); last == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

func bindKillOnClose(pid uint32) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

const liveBaseConfig = `{
  "log": {"level": "info", "timestamp": true},
  "inbounds": [{
    "type": "mixed",
    "tag": "mixed-in",
    "listen": "127.0.0.1",
    "listen_port": 18080
  }],
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {"type": "socks", "tag": "blackhole", "server": "127.0.0.1", "server_port": 1}
  ],
  "route": {"final": "blackhole", "rules": []}
}
`
