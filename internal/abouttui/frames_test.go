//go:build windows

package abouttui

import (
	"strings"
	"testing"
	"time"
)

func TestModifierKeysDoNotClose(t *testing.T) {
	if !isModifierVK(0x11) || !isModifierVK(0xA2) {
		t.Fatal("ctrl should be ignored")
	}
	if isModifierVK(0x0D) || isModifierVK(0x1B) {
		t.Fatal("enter and escape should close")
	}
}

func TestSnapshotArg(t *testing.T) {
	raw, ok := snapshotRaw([]string{"exe", FlagWT, EncodeSnapshot(`{"mode":"TUN"}`)})
	if !ok || raw != `{"mode":"TUN"}` {
		t.Fatalf("got %q ok=%v", raw, ok)
	}
	if _, ok := snapshotRaw([]string{"exe", FlagWT, "!!!"}); ok {
		t.Fatal("bad payload accepted")
	}
}

func TestBakedMascotFrames(t *testing.T) {
	strip, err := loadStrip()
	if err != nil {
		t.Fatal(err)
	}
	if len(strip.frames) < 2 {
		t.Fatalf("frames = %d", len(strip.frames))
	}
	if strip.frames[1].art == strip.frames[0].art {
		t.Fatal("blink frame matches the base")
	}
	for i, frame := range strip.frames {
		if frame.hold <= 0 {
			t.Fatalf("frame %d hold = %s", i, frame.hold)
		}
		lines := strings.Split(frame.art, "\n")
		if i == 0 {
			if len(lines) < 1 {
				t.Fatal("empty frame")
			}
		} else if len(lines) != len(strings.Split(strip.frames[0].art, "\n")) {
			t.Fatalf("frame %d rows = %d, want %d", i, len(lines), len(strings.Split(strip.frames[0].art, "\n")))
		}
		for _, line := range lines {
			if len(line) != strip.cols {
				t.Fatalf("frame %d line width %d, want %d", i, len(line), strip.cols)
			}
		}
	}
}

func TestVisibleWidthIgnoresOSC8(t *testing.T) {
	linked := osc8(repoURL, repoURL)
	if visibleWidth(linked) != len(repoURL) {
		t.Fatalf("width = %d, want %d", visibleWidth(linked), len(repoURL))
	}
	if got := fileURL(`C:\configs\my config.json`); got != "file:///C:/configs/my%20config.json" {
		t.Fatalf("file url = %s", got)
	}
}

func TestComposeCentersInfoOnMascot(t *testing.T) {
	art := strings.Join([]string{"", "", "cat", "cat", "cat", "cat", ""}, "\n")
	got := strings.Split(compose(art, 3, []string{"a", "b"}), "\n")
	if !strings.HasSuffix(strings.TrimSuffix(got[3], "\x1b[K"), "  a") || !strings.HasSuffix(strings.TrimSuffix(got[4], "\x1b[K"), "  b") {
		t.Fatalf("info not centered on the mascot: %#v", got)
	}
	if strings.HasSuffix(strings.TrimSuffix(got[2], "\x1b[K"), "  a") {
		t.Fatal("info stuck to the top of the mascot")
	}
}

func TestLayersRunOnSeparateClocks(t *testing.T) {
	strip, err := loadStrip()
	if err != nil {
		t.Fatal(err)
	}
	if len(strip.eyes.rows) == 0 || len(strip.tail.rows) == 0 {
		t.Fatalf("eyes rows %d, tail rows %d", len(strip.eyes.rows), len(strip.tail.rows))
	}
	var blink = -1
	for i, frame := range strip.eyes.frames {
		if frame.hold <= 200*time.Millisecond {
			blink = i
			break
		}
	}
	if blink < 0 {
		t.Fatal("blink hold was merged into the tail clock")
	}
	for i, frame := range strip.tail.frames {
		if frame.hold < 400*time.Millisecond {
			t.Fatalf("tail frame %d hold = %s", i, frame.hold)
		}
	}
	pic := strip.picture(blink, 0)
	lines := strings.Split(pic, "\n")
	eyeSrc := strings.Split(strip.eyes.frames[blink].art, "\n")
	tailSrc := strings.Split(strip.tail.frames[0].art, "\n")
	for _, r := range strip.eyes.rows {
		if lines[r] != eyeSrc[r] {
			t.Fatalf("eye row %d was not taken from the blink", r)
		}
	}
	for _, r := range strip.tail.rows {
		if lines[r] != tailSrc[r] {
			t.Fatalf("tail row %d changed during the blink", r)
		}
	}
}
