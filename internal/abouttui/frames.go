//go:build windows

package abouttui

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "embed"
)

//go:embed mascot.ascii
var mascotASCII string

type mascotFrame struct {
	art  string
	hold time.Duration
}

// track is one looping layer. rows are the only lines copied from its frames
// onto the base picture, so the eyes and the tail keep their own clocks.
type track struct {
	rows   []int
	frames []mascotFrame
}

type mascotStrip struct {
	cols   int
	frames []mascotFrame
	base   string
	eyes   track
	tail   track
}

func loadStrip() (mascotStrip, error) {
	sc := bufio.NewScanner(strings.NewReader(mascotASCII))
	var cols, rows, n int
	var frames []mascotFrame
	var cur strings.Builder
	inFrame := false
	hold := 1500 * time.Millisecond
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "---") {
			if inFrame {
				frames = append(frames, mascotFrame{
					art:  strings.TrimSuffix(cur.String(), "\n"),
					hold: hold,
				})
				cur.Reset()
			}
			inFrame = true
			hold = 1500 * time.Millisecond
			rest := strings.TrimSpace(strings.TrimPrefix(line, "---"))
			if rest != "" {
				ms, err := strconv.Atoi(rest)
				if err != nil || ms <= 0 {
					return mascotStrip{}, fmt.Errorf("mascot.ascii: hold %q", rest)
				}
				hold = time.Duration(ms) * time.Millisecond
			}
			continue
		}
		if !inFrame {
			key, val, ok := strings.Cut(line, " ")
			if !ok {
				continue
			}
			switch key {
			case "cols", "rows", "frames":
				num, err := strconv.Atoi(val)
				if err != nil {
					return mascotStrip{}, err
				}
				switch key {
				case "cols":
					cols = num
				case "rows":
					rows = num
				case "frames":
					n = num
				}
			}
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	if inFrame && cur.Len() > 0 {
		frames = append(frames, mascotFrame{
			art:  strings.TrimSuffix(cur.String(), "\n"),
			hold: hold,
		})
	}
	if len(frames) == 0 || (n > 0 && len(frames) != n) {
		return mascotStrip{}, fmt.Errorf("mascot.ascii: cols=%d rows=%d frames=%d header=%d", cols, rows, len(frames), n)
	}
	cols, frames = normalize(frames, cols, rows)
	if cols <= 0 {
		return mascotStrip{}, fmt.Errorf("mascot.ascii: empty frame")
	}
	base, eyes, tail := splitLayers(frames)
	return mascotStrip{cols: cols, frames: frames, base: base, eyes: eyes, tail: tail}, nil
}

// splitLayers separates the blink from the tail wag. A row that changes to
// the left of the body is the tail; any other changing row is the eyes.
// Tail poses that were drawn on a blink frame keep the slow wag hold instead
// of the blink's short one. Consecutive identical eye poses add their holds,
// so the eyes stay open while the tail keeps moving.
func splitLayers(frames []mascotFrame) (string, track, track) {
	base := frames[0].art
	baseLines := strings.Split(base, "\n")
	body := bodyCol(baseLines)
	eyeRow := make([]bool, len(baseLines))
	tailRow := make([]bool, len(baseLines))
	for _, frame := range frames {
		lines := strings.Split(frame.art, "\n")
		for r := 0; r < len(baseLines); r++ {
			other := ""
			if r < len(lines) {
				other = lines[r]
			}
			left, right := rowChanged(baseLines[r], other, body)
			if left {
				tailRow[r] = true
			} else if right {
				eyeRow[r] = true
			}
		}
	}
	var eyes, tail []int
	for r := range baseLines {
		if tailRow[r] {
			tail = append(tail, r)
			continue
		}
		if eyeRow[r] {
			eyes = append(eyes, r)
		}
	}

	pace := frames[0].hold
	if pace < 400*time.Millisecond {
		pace = 520 * time.Millisecond
	}
	eyeTrack := track{rows: eyes, frames: groupEyes(frames, eyes)}
	tailFrames := make([]mascotFrame, 0, len(frames))
	for _, frame := range frames {
		hold := frame.hold
		if hold < 400*time.Millisecond {
			hold = pace
		}
		if n := len(tailFrames); n > 0 && tailSig(tailFrames[n-1].art, tail) == tailSig(frame.art, tail) {
			tailFrames[n-1].hold += hold
			continue
		}
		tailFrames = append(tailFrames, mascotFrame{art: frame.art, hold: hold})
	}
	return base, eyeTrack, track{rows: tail, frames: tailFrames}
}

func groupEyes(frames []mascotFrame, rows []int) []mascotFrame {
	if len(rows) == 0 {
		return []mascotFrame{{art: frames[0].art, hold: 24 * time.Hour}}
	}
	var out []mascotFrame
	for _, frame := range frames {
		sig := tailSig(frame.art, rows)
		if n := len(out); n > 0 && tailSig(out[n-1].art, rows) == sig {
			out[n-1].hold += frame.hold
			continue
		}
		out = append(out, frame)
	}
	return out
}

func (s mascotStrip) picture(eye, tail int) string {
	lines := strings.Split(s.base, "\n")
	stamp := func(tr track, i int) {
		if len(tr.frames) == 0 {
			return
		}
		src := strings.Split(tr.frames[i%len(tr.frames)].art, "\n")
		for _, r := range tr.rows {
			if r < len(lines) && r < len(src) {
				lines[r] = src[r]
			}
		}
	}
	stamp(s.eyes, eye)
	stamp(s.tail, tail)
	return strings.Join(lines, "\n")
}

func tailSig(art string, rows []int) string {
	lines := strings.Split(art, "\n")
	var b strings.Builder
	for _, r := range rows {
		if r < len(lines) {
			b.WriteString(lines[r])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func bodyCol(lines []string) int {
	body := len(lines[0])
	for _, line := range lines {
		run := 0
		for i, r := range line {
			if r == '#' {
				run++
				if run >= 8 {
					start := i - run + 1
					if start < body {
						body = start
					}
				}
				continue
			}
			run = 0
		}
	}
	if body == len(lines[0]) {
		return 0
	}
	return body
}

func rowChanged(a, b string, body int) (left, right bool) {
	ar, br := []rune(a), []rune(b)
	n := len(ar)
	if len(br) > n {
		n = len(br)
	}
	for i := 0; i < n; i++ {
		ca, cb := rune(' '), rune(' ')
		if i < len(ar) {
			ca = ar[i]
		}
		if i < len(br) {
			cb = br[i]
		}
		if ca == cb {
			continue
		}
		if i < body {
			left = true
		} else {
			right = true
		}
	}
	return left, right
}

// normalize pads every frame to one rectangle. The header is a hint; a
// hand-edited line that is wider or a frame that is taller wins, so the
// console does not wrap mid-frame and the info column stays put.
func normalize(frames []mascotFrame, cols, rows int) (int, []mascotFrame) {
	split := make([][]string, len(frames))
	maxW, maxH := cols, rows
	for i, frame := range frames {
		lines := strings.Split(frame.art, "\n")
		split[i] = lines
		if len(lines) > maxH {
			maxH = len(lines)
		}
		for _, line := range lines {
			if w := utf8.RuneCountInString(line); w > maxW {
				maxW = w
			}
		}
	}
	if maxW < 1 {
		maxW = 1
	}
	if maxH < 1 {
		maxH = 1
	}
	for i := range frames {
		lines := split[i]
		for len(lines) < maxH {
			lines = append(lines, "")
		}
		for j, line := range lines {
			w := utf8.RuneCountInString(line)
			switch {
			case w < maxW:
				lines[j] = line + strings.Repeat(" ", maxW-w)
			case w > maxW:
				lines[j] = string([]rune(line)[:maxW])
			}
		}
		frames[i].art = strings.Join(lines, "\n")
	}
	return maxW, frames
}
