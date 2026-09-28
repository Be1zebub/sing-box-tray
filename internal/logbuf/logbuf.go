package logbuf

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	// maxLogBytes is the on-disk cap for sing-box-tray.log.
	maxLogBytes = 1 << 20
	// keepLogBytes is what a trim leaves behind, so the next lines do not
	// immediately trim again.
	keepLogBytes = 768 << 10
)

// Buffer is a thread-safe circular buffer of log lines that optionally mirrors
// all entries to a file writer with timestamps. The file mirror is what the
// terminal log viewer tails.
type Buffer struct {
	mu      sync.RWMutex
	data    []string
	cap     int
	fileOut io.Writer
}

func New(capacity int) *Buffer {
	return &Buffer{
		data: make([]string, 0, capacity),
		cap:  capacity,
	}
}

// SetFileOutput enables mirroring all future Append calls to w with a
// timestamp prefix. Safe to call once before the app starts processing.
func (b *Buffer) SetFileOutput(w io.Writer) {
	b.mu.Lock()
	b.fileOut = w
	b.mu.Unlock()
}

func (b *Buffer) Append(line string) {
	b.mu.Lock()
	if len(b.data) >= b.cap {
		b.data = b.data[1:]
	}
	b.data = append(b.data, line)
	out := b.fileOut
	b.mu.Unlock()

	if out != nil {
		ts := time.Now().Format("2006-01-02 15:04:05")
		fmt.Fprintf(out, "[%s] %s\n", ts, line)
	}
}

// OpenRolling opens path for append and keeps it at or below maxLogBytes by
// dropping the oldest bytes. An already oversized file is trimmed on open.
func OpenRolling(path string) (*RollingFile, error) {
	return openRolling(path, maxLogBytes, keepLogBytes)
}

// RollingFile is an append-only log that discards the oldest bytes once it
// grows past max.
type RollingFile struct {
	mu   sync.Mutex
	f    *os.File
	size int64
	max  int64
	keep int64
}

func openRolling(path string, max, keep int64) (*RollingFile, error) {
	if keep <= 0 || keep >= max {
		return nil, fmt.Errorf("log keep %d must be inside max %d", keep, max)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	r := &RollingFile{f: f, size: info.Size(), max: max, keep: keep}
	if r.size > r.max {
		if err := r.trimLocked(); err != nil {
			f.Close()
			return nil, err
		}
	}
	return r, nil
}

func (r *RollingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.max {
		if err := r.trimLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.WriteAt(p, r.size)
	r.size += int64(n)
	return n, err
}

func (r *RollingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// trimLocked rewrites the file as its trailing keep bytes, starting at the
// next newline so a line is not cut in half.
func (r *RollingFile) trimLocked() error {
	if r.size <= r.keep {
		return nil
	}
	buf := make([]byte, r.keep)
	n, err := r.f.ReadAt(buf, r.size-r.keep)
	if err != nil && err != io.EOF {
		return err
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 && i+1 < len(buf) {
		buf = buf[i+1:]
	}
	if err := r.f.Truncate(0); err != nil {
		return err
	}
	if _, err := r.f.WriteAt(buf, 0); err != nil {
		return err
	}
	r.size = int64(len(buf))
	return nil
}
