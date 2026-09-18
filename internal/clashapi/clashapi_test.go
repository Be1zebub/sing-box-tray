//go:build windows

package clashapi

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseResponse(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "plain body",
			raw:  "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"a\":1}",
			want: `{"a":1}`,
		},
		{
			name: "chunked body",
			raw:  "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n7\r\n{\"a\":1}\r\n0\r\n\r\n",
			want: `{"a":1}`,
		},
		{
			name: "204 no content",
			raw:  "HTTP/1.1 204 No Content\r\n\r\n",
			want: "",
		},
		{
			name:    "error status",
			raw:     "HTTP/1.1 400 Bad Request\r\n\r\n",
			wantErr: true,
		},
		{
			name:    "no header terminator",
			raw:     "HTTP/1.1 200 OK\r\n",
			wantErr: true,
		},
		{
			name:    "garbage status line",
			raw:     "garbage\r\n\r\n",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResponse([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDechunk(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"single", "7\r\n{\"a\":1}\r\n0\r\n\r\n", `{"a":1}`, false},
		{"multi", "3\r\nabc\r\n3\r\ndef\r\n0\r\n\r\n", "abcdef", false},
		{"extension", "3;foo=bar\r\nabc\r\n0\r\n\r\n", "abc", false},
		{"truncated size", "zz\r\nabc\r\n0\r\n\r\n", "", true},
		{"truncated data", "5\r\nabc\r\n0\r\n\r\n", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dechunk([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

// stubServer answers every request with the same canned response, recording
// the request line and body so the tests can assert on what was sent.
type stubServer struct {
	ln       net.Listener
	requests chan string
	bodies   chan string
}

func newStubServer(t *testing.T, response string) *stubServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &stubServer{
		ln:       ln,
		requests: make(chan string, 8),
		bodies:   make(chan string, 8),
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				line, body := splitRequest(readRequest(c))
				s.requests <- line
				s.bodies <- body
				_, _ = io.WriteString(c, response)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *stubServer) addr() string { return s.ln.Addr().String() }

// wait returns channels since io.ReadAll only finishes once the client closes
// its side, which it does right after writing the request.
func waitOn(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("stub server saw no request")
		return ""
	}
}

func TestGroups(t *testing.T) {
	body := `{"proxies":{
      "auto":{"type":"URLTest","now":"B","all":["A","B"]},
      "pick":{"type":"Selector","now":"A","all":["A","B"]},
      "direct":{"type":"Direct","now":"","all":null},
      "A":{"type":"Vless","now":"","all":null}
    }}`
	s := newStubServer(t, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"+body)

	groups, err := New(s.addr(), "").Groups()
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if line := waitOn(t, s.requests); !strings.HasPrefix(line, "GET /proxies ") {
		t.Errorf("request line = %q", line)
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 selectable groups, got %d: %+v", len(groups), groups)
	}
	// sorted by name: "auto", "pick"
	if groups[0].Name != "auto" || groups[0].Type != "URLTest" || groups[0].Now != "B" {
		t.Errorf("groups[0] = %+v", groups[0])
	}
	if len(groups[0].All) != 2 || groups[0].All[0] != "A" {
		t.Errorf("groups[0].All = %v", groups[0].All)
	}
	if groups[1].Name != "pick" || groups[1].Type != "Selector" {
		t.Errorf("groups[1] = %+v", groups[1])
	}
}

func TestSelect(t *testing.T) {
	// A group name with a space and a non-ASCII character exercises escaping.
	group := "→ Remnawave"
	s := newStubServer(t, "HTTP/1.1 204 No Content\r\n\r\n")

	if err := New(s.addr(), "topsecret").Select(group, "Vless Reality | Best Bypass"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	line := waitOn(t, s.requests)
	if !strings.HasPrefix(line, "PUT /proxies/") || !strings.HasSuffix(line, " HTTP/1.1") {
		t.Errorf("request line = %q", line)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(line, "PUT "), " HTTP/1.1")
	if strings.ContainsAny(path, " →") {
		t.Errorf("group name must be percent-encoded, got %q", path)
	}

	body := waitOn(t, s.bodies)
	if body != `{"name":"Vless Reality | Best Bypass"}` {
		t.Errorf("body = %q", body)
	}
}

func TestSelectSurfacesHTTPError(t *testing.T) {
	s := newStubServer(t, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
	if err := New(s.addr(), "").Select("pick", "A"); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
}

func TestDialFailureIsReported(t *testing.T) {
	// Nothing listens here: the client must return an error, not hang.
	if _, err := New("127.0.0.1:1", "").Groups(); err == nil {
		t.Fatal("expected a dial error")
	}
}

// readRequest reads exactly one HTTP request: everything up to the header
// terminator, plus Content-Length body bytes. It must not wait for EOF — the
// client keeps the connection open until it has read the response, so reading
// to EOF would deadlock.
func readRequest(c net.Conn) string {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var buf bytes.Buffer
	tmp := make([]byte, 512)
	headerEnd := -1
	for {
		n, err := c.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			raw := buf.Bytes()
			if headerEnd < 0 {
				if idx := bytes.Index(raw, []byte("\r\n\r\n")); idx >= 0 {
					headerEnd = idx + 4
				}
			}
			if headerEnd >= 0 && buf.Len() >= headerEnd+contentLength(raw[:headerEnd]) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

func contentLength(head []byte) int {
	length := 0
	for _, l := range strings.Split(string(head), "\r\n") {
		name, value, ok := strings.Cut(l, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	return length
}

// splitRequest returns the request line and the body of a raw HTTP request.
func splitRequest(raw string) (string, string) {
	head, body, found := strings.Cut(raw, "\r\n\r\n")
	if !found {
		return strings.TrimSpace(raw), ""
	}
	if n := contentLength([]byte(head + "\r\n\r\n")); n > 0 && n <= len(body) {
		body = body[:n]
	}
	line, _, _ := strings.Cut(head, "\r\n")
	return strings.TrimSpace(line), body
}

func TestSplitRequest(t *testing.T) {
	raw := "PUT /proxies/x HTTP/1.1\r\nContent-Length: 12\r\n\r\n{\"name\":\"a\"}tail"
	line, body := splitRequest(raw)
	if line != "PUT /proxies/x HTTP/1.1" {
		t.Errorf("line = %q", line)
	}
	if body != `{"name":"a"}` {
		t.Errorf("body = %q", body)
	}
	if !bytes.Equal([]byte(body), []byte(`{"name":"a"}`)) {
		t.Error("body mismatch")
	}
}
