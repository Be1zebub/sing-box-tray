// Package testrun validates and benchmarks imported outbounds through a local
// SOCKS inbound. It never creates a TUN device and never touches the system
// route: a throwaway sing-box process listens on loopback only.
package testrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/net/proxy"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
)

// Options controls a test run.
type Options struct {
	SingboxPath     string
	LatencyURL      string
	DownloadURL     string
	DownloadLimit   int64 // bytes per outbound; 0 = whole file
	LatencyTimeout  time.Duration
	DownloadTimeout time.Duration
	StartupTimeout  time.Duration
}

func (o Options) withDefaults() Options {
	if o.LatencyURL == "" {
		o.LatencyURL = "https://www.gstatic.com/generate_204"
	}
	if o.DownloadURL == "" {
		o.DownloadURL = "https://proof.ovh.net/files/10Mb.dat"
	}
	if o.LatencyTimeout == 0 {
		o.LatencyTimeout = 15 * time.Second
	}
	if o.DownloadTimeout == 0 {
		o.DownloadTimeout = 90 * time.Second
	}
	if o.StartupTimeout == 0 {
		o.StartupTimeout = 15 * time.Second
	}
	return o
}

// Result is the outcome for a single outbound.
type Result struct {
	Tag      string
	OK       bool
	Latency  time.Duration
	Bytes    int64
	Duration time.Duration
	Speed    float64 // bytes per second
	Err      string
}

// Run tests every concrete outbound, invoking onResult after each one.
func Run(ctx context.Context, outbounds []singbox.Obj, opts Options, onResult func(Result)) ([]Result, error) {
	opts = opts.withDefaults()
	if opts.SingboxPath == "" {
		return nil, errors.New("sing-box path is empty")
	}

	var concrete []singbox.Obj
	for _, ob := range outbounds {
		if singbox.IsConcrete(singbox.Type(ob)) && singbox.Str(ob, "server") != "" {
			concrete = append(concrete, ob)
		}
	}
	if len(concrete) == 0 {
		return nil, errors.New("no concrete outbounds to test")
	}

	ports := make([]int, len(concrete))
	inbounds := make([]any, len(concrete))
	rules := make([]any, len(concrete))
	for i, ob := range concrete {
		p, err := freePort()
		if err != nil {
			return nil, fmt.Errorf("allocate port: %w", err)
		}
		ports[i] = p
		inTag := fmt.Sprintf("test-%d", i)
		inbounds[i] = singbox.Obj{
			"type":        "mixed",
			"tag":         inTag,
			"listen":      "127.0.0.1",
			"listen_port": p,
		}
		rules[i] = singbox.Obj{"inbound": []any{inTag}, "outbound": singbox.Tag(ob)}
	}

	root := singbox.Obj{
		"log":      singbox.Obj{"level": "error"},
		"dns":      singbox.Obj{"servers": []any{singbox.Obj{"type": "local", "tag": "local"}}, "final": "local", "strategy": "prefer_ipv4"},
		"inbounds": inbounds,
		"route": singbox.Obj{
			"rules":                 rules,
			"final":                 "direct",
			"auto_detect_interface": true,
		},
	}
	objs := make([]any, 0, len(concrete)+1)
	for _, ob := range concrete {
		objs = append(objs, ob)
	}
	objs = append(objs, singbox.Obj{"type": "direct", "tag": "direct"})
	root["outbounds"] = objs

	cfg := singbox.FromRoot(root)
	data, err := cfg.Marshal()
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "config-importer-test-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "test.json")
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return nil, err
	}

	cmd := exec.Command(opts.SingboxPath, "run", "-c", cfgPath, "-D", dir)
	stderr := &tailBuffer{max: 8 << 10}
	cmd.Stderr = stderr
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sing-box: %w", err)
	}
	exited := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		exited <- cmd.Wait()
		close(done)
	}()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}()

	if err := waitReady(ctx, exited, ports[0], stderr, opts.StartupTimeout); err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(concrete))
	for i, ob := range concrete {
		res := testOne(ctx, opts, ports[i], singbox.Tag(ob))
		results = append(results, res)
		if onResult != nil {
			onResult(res)
		}
	}
	return results, nil
}

func testOne(ctx context.Context, opts Options, port int, tag string) Result {
	res := Result{Tag: tag}

	latencyClient, err := socksClient(port, opts.LatencyTimeout)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	latCtx, cancel := context.WithTimeout(ctx, opts.LatencyTimeout)
	defer cancel()
	latency, err := measureLatency(latCtx, latencyClient, opts.LatencyURL)
	if err != nil {
		res.Err = fmt.Sprintf("connect: %v", err)
		return res
	}
	res.Latency = latency
	res.OK = true

	if opts.DownloadURL == "" {
		return res
	}
	dlClient, err := socksClient(port, opts.DownloadTimeout)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	dlCtx, cancelDl := context.WithTimeout(ctx, opts.DownloadTimeout)
	defer cancelDl()
	n, dur, err := measureDownload(dlCtx, dlClient, opts.DownloadURL, opts.DownloadLimit)
	res.Bytes = n
	res.Duration = dur
	if dur > 0 {
		res.Speed = float64(n) / dur.Seconds()
	}
	if err != nil {
		res.OK = false
		res.Err = fmt.Sprintf("download: %v", err)
	}
	return res
}

func measureLatency(ctx context.Context, client *http.Client, url string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return time.Since(start), nil
}

func measureDownload(ctx context.Context, client *http.Client, url string, limit int64) (int64, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if limit > 0 {
		reader = io.LimitReader(resp.Body, limit)
	}
	n, err := io.Copy(io.Discard, reader)
	dur := time.Since(start)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, dur, err
	}
	return n, dur, nil
}

func socksClient(port int, timeout time.Duration) (*http.Client, error) {
	d, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil, proxy.Direct)
	if err != nil {
		return nil, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS dialer is not a ContextDialer")
	}
	tr := &http.Transport{DialContext: cd.DialContext, DisableKeepAlives: true}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

func waitReady(ctx context.Context, exited <-chan error, port int, stderr *tailBuffer, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return fmt.Errorf("sing-box exited early: %v\n%s", err, stderr.String())
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("sing-box did not open the SOCKS port in time\n%s", stderr.String())
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// Validate runs "sing-box check" against a config file.
func Validate(ctx context.Context, singboxPath, cfgPath string) error {
	cmd := exec.CommandContext(ctx, singboxPath, "check", "-c", cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sing-box check failed: %v\n%s", err, string(out))
	}
	return nil
}
