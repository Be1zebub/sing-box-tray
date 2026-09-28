//go:build windows

// Package abouttui is the console splash opened from the tray About item.
// The tray binary is a GUI subsystem app, so this runs in a second process
// that allocates its own console.
package abouttui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Flag is the argv switch that selects the splash instead of the tray.
// FlagWT is the same splash started inside Windows Terminal: the process
// attaches to the parent console instead of allocating conhost.
const (
	Flag   = "--about"
	FlagWT = "--about-wt"
)

// WantsRun reports whether this process was started as the About splash.
func WantsRun(args []string) bool {
	return len(args) > 1 && (args[1] == Flag || args[1] == FlagWT)
}

const (
	repoURL = "https://github.com/Be1zebub/sing-box-tray"
	appName = "sing-box-tray"
)

const (
	enableVirtualTerminal = 0x0004
	enableProcessedOutput = 0x0001
)

// Run draws a short animation, then waits for a key.
func Run() {
	if err := prepareConsole(); err != nil {
		return
	}
	defer func() {
		fmt.Print("\x1b[?25h\x1b[?1049l")
		if consoleOwned {
			freeConsole.Call()
		}
	}()

	strip, err := loadStrip()
	if err != nil {
		return
	}
	info := loadPanelFast()
	panelCh := make(chan panel, 1)
	go func() {
		panelCh <- loadPanel()
	}()
	done := make(chan struct{})
	go func() {
		waitKey()
		close(done)
	}()

	out := os.Stdout
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l\x1b[2J")
	eye, tail := 0, 0
	eyeLeft, tailLeft := trackHold(strip.eyes, 0), trackHold(strip.tail, 0)
	for {
		select {
		case info = <-panelCh:
			panelCh = nil
		default:
		}
		art := strip.picture(eye, tail)
		fmt.Fprint(out, place(compose(art, strip.cols, info.lines)))
		wait := eyeLeft
		if tailLeft < wait {
			wait = tailLeft
		}
		timer := time.NewTimer(wait)
		select {
		case <-done:
			timer.Stop()
			return
		case p := <-panelCh:
			info = p
			panelCh = nil
			timer.Stop()
		case <-timer.C:
			eyeLeft -= wait
			tailLeft -= wait
			if eyeLeft <= 0 {
				eye = (eye + 1) % trackLen(strip.eyes)
				eyeLeft = trackHold(strip.eyes, eye)
			}
			if tailLeft <= 0 {
				tail = (tail + 1) % trackLen(strip.tail)
				tailLeft = trackHold(strip.tail, tail)
			}
		}
	}
}

func trackLen(tr track) int {
	if len(tr.frames) == 0 {
		return 1
	}
	return len(tr.frames)
}

func trackHold(tr track, i int) time.Duration {
	if len(tr.frames) == 0 {
		return time.Hour
	}
	h := tr.frames[i%len(tr.frames)].hold
	if h <= 0 {
		return time.Millisecond
	}
	return h
}

func compose(art string, artCols int, info []string) string {
	left := strings.Split(art, "\n")
	pad := infoPad(left, len(info))
	n := len(left)
	if pad+len(info) > n {
		n = pad + len(info)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		row := ""
		if i < len(left) {
			row = left[i]
		} else {
			row = strings.Repeat(" ", artCols)
		}
		b.WriteString(row)
		b.WriteString("  ")
		if j := i - pad; j >= 0 && j < len(info) {
			b.WriteString(info[j])
		}
		b.WriteString("\x1b[K")
	}
	return b.String()
}

// infoPad is the art row where the info block starts so the block's middle
// lines up with the mascot, ignoring blank rows above and below the drawing.
func infoPad(art []string, infoLines int) int {
	top, bot := 0, len(art)-1
	for top <= bot && strings.TrimSpace(art[top]) == "" {
		top++
	}
	for bot >= top && strings.TrimSpace(art[bot]) == "" {
		bot--
	}
	if top > bot {
		return 0
	}
	span := bot - top + 1
	if infoLines >= span {
		return top
	}
	return top + (span-infoLines)/2
}

func place(body string) string {
	lines := strings.Split(body, "\n")
	w, h := 0, len(lines)
	for _, ln := range lines {
		if n := visibleWidth(ln); n > w {
			w = n
		}
	}
	winW, winH := fitConsole(w+4, h+2)
	padX := (winW - w) / 2
	padY := (winH - h) / 2
	if padX < 0 {
		padX = 0
	}
	if padY < 0 {
		padY = 0
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i := 0; i < padY; i++ {
		b.WriteString("\x1b[2K\n")
	}
	for i, ln := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(" ", padX))
		b.WriteString(ln)
		b.WriteString("\x1b[K")
	}
	b.WriteString("\x1b[J")
	return b.String()
}

func visibleWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && (s[i+1] == '[' || s[i+1] == ']') {
			if s[i+1] == ']' {
				i += 2
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				continue
			}
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size < 1 {
			size = 1
		}
		n++
		i += size
	}
	return n
}

func fitConsole(cols, rows int) (int, int) {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return cols, rows
	}
	if r, _, _ := procLargestWindow.Call(uintptr(h)); r != 0 {
		lw, lh := int(int16(r)), int(int16(r>>16))
		if lw > 0 && lw < cols {
			cols = lw
		}
		if lh > 0 && lh < rows {
			rows = lh
		}
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(h, &info); err != nil {
		return cols, rows
	}
	bufW, bufH := int(info.Size.X), int(info.Size.Y)
	if bufW < cols {
		bufW = cols
	}
	if bufH < rows {
		bufH = rows
	}
	packed := uintptr(uint32(uint16(bufW)) | uint32(uint16(bufH))<<16)
	procSetBuffer.Call(uintptr(h), packed)
	rect := windows.SmallRect{Right: int16(cols - 1), Bottom: int16(rows - 1)}
	procSetWindow.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&rect)))
	if err := windows.GetConsoleScreenBufferInfo(h, &info); err != nil {
		return cols, rows
	}
	return int(info.Window.Right-info.Window.Left) + 1, int(info.Window.Bottom-info.Window.Top) + 1
}

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procAlloc         = kernel32.NewProc("AllocConsole")
	procAttach        = kernel32.NewProc("AttachConsole")
	freeConsole       = kernel32.NewProc("FreeConsole")
	setTitle          = kernel32.NewProc("SetConsoleTitleW")
	procLargestWindow = kernel32.NewProc("GetLargestConsoleWindowSize")
	procSetBuffer     = kernel32.NewProc("SetConsoleScreenBufferSize")
	procSetWindow     = kernel32.NewProc("SetConsoleWindowInfo")
)

// consoleOwned is true when this process allocated the console and must free it.
var consoleOwned bool

const attachParentProcess = uintptr(0xFFFFFFFF) // ATTACH_PARENT_PROCESS

func prepareConsole() error {
	if len(os.Args) > 1 && os.Args[1] == FlagWT {
		if r, _, _ := procAttach.Call(attachParentProcess); r != 0 {
			return openConsoleStreams(false)
		}
	}
	if r, _, err := procAlloc.Call(); r == 0 {
		return err
	}
	return openConsoleStreams(true)
}

func openConsoleStreams(owned bool) error {
	consoleOwned = owned
	title, _ := windows.UTF16PtrFromString(appName)
	setTitle.Call(uintptr(unsafe.Pointer(title)))

	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	os.Stdout = out
	os.Stderr = out
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	os.Stdin = in

	handle := windows.Handle(out.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err == nil {
		_ = windows.SetConsoleMode(handle, mode|enableVirtualTerminal|enableProcessedOutput)
	}
	if owned {
		disableQuickEdit()
	}
	_ = windows.SetConsoleOutputCP(65001)
	return nil
}

// disableQuickEdit stops conhost from entering mark mode on click. That mode
// retitles the window to "Select …" and freezes the process until Escape.
func disableQuickEdit() {
	const (
		enableQuickEdit     = 0x0040
		enableExtendedFlags = 0x0080
	)
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return
	}
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return
	}
	mode |= enableExtendedFlags
	mode &^= enableQuickEdit
	_ = windows.SetConsoleMode(h, mode)
}

// isModifierVK reports Ctrl, Alt, Shift and the Windows keys. Ctrl+Click on an
// OSC 8 link delivers the modifier as a key event; closing on it kills the
// splash while the terminal is opening the target.
func isModifierVK(vk uint16) bool {
	switch vk {
	case 0x10, 0x11, 0x12, 0x5B, 0x5C, 0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5:
		return true
	default:
		return false
	}
}

func waitKey() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	read := kernel32.NewProc("ReadConsoleInputW")
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return
	}
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT))
	}
	type inputRecord struct {
		EventType uint16
		_         uint16
		Event     [16]byte
	}
	const keyEvent = 0x0001
	// Drop the focus/resize noise Windows Terminal queues as the tab opens,
	// and ignore keys for a moment so that same noise cannot dismiss the splash.
	flush := kernel32.NewProc("FlushConsoleInputBuffer")
	flush.Call(uintptr(h))
	start := time.Now()
	fails := 0
	for {
		var rec inputRecord
		var n uint32
		r, _, _ := read.Call(uintptr(h), uintptr(unsafe.Pointer(&rec)), 1, uintptr(unsafe.Pointer(&n)))
		if r == 0 || n == 0 {
			// Opening an OSC 8 link makes the read fail for a moment. That is
			// not a keypress, and returning here closes the splash mid-click.
			fails++
			if fails > 50 {
				return
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		fails = 0
		if time.Since(start) < 400*time.Millisecond {
			continue
		}
		// Ctrl+Click activates an OSC 8 link. The Ctrl key itself must not close
		// the splash: exiting here tears down the console while Terminal is
		// opening the link.
		if rec.EventType == keyEvent && rec.Event[0] != 0 && !isModifierVK(uint16(rec.Event[6])|uint16(rec.Event[7])<<8) {
			return
		}
	}
}
