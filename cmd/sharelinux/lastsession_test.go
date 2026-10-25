package main

import (
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLastSessionChips(t *testing.T) {
	last := lastSession{Width: 5120, Height: 1440, Codec: "HEVC 4:4:4", HDR: true, Sound: true}
	got := last.chips()
	want := []chip{{Text: "5120 × 1440"}, {Text: "HEVC 4:4:4"}, {Text: "HDR", HDR: true}, {Text: "Sound on"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("chip %d = %+v, want %+v", index, got[index], want[index])
		}
	}
	if chips := (lastSession{}).chips(); len(chips) != 0 {
		t.Fatalf("an empty session has no chips: %+v", chips)
	}
	plain := lastSession{Width: 1920, Height: 1080, Codec: "ZRLE"}.chips()
	if len(plain) != 2 {
		t.Fatalf("no HDR and no sound leaves two chips: %+v", plain)
	}
}

func TestCodecChipText(t *testing.T) {
	if codecChipText("HEVC", true) != "HEVC 4:4:4" || codecChipText("HEVC", false) != "HEVC" || codecChipText("", true) != "" {
		t.Fatal("codec chip")
	}
}

func TestLastFrameText(t *testing.T) {
	current := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := map[time.Duration]string{
		2 * time.Minute:  "LAST FRAME, 2 MIN AGO",
		time.Minute:      "LAST FRAME, 1 MIN AGO",
		5 * time.Hour:    "LAST FRAME, 5 HOURS AGO",
		30 * time.Second: "LAST FRAME, JUST NOW",
	}
	for age, want := range cases {
		if got := lastFrameText(current.Add(-age), current); got != want {
			t.Errorf("%s: %q, want %q", age, got, want)
		}
	}
	if lastFrameText(time.Time{}, current) != "LAST FRAME" {
		t.Fatal("no time")
	}
}

func TestThumbnailPath(t *testing.T) {
	a := thumbnailPath("/cache", "user@mac.local:5900")
	b := thumbnailPath("/cache", "other@mac.local:5900")
	if a == b {
		t.Fatal("two Macs share a thumbnail")
	}
	if filepath.Dir(a) != filepath.Join("/cache", "thumbnails") || !strings.HasSuffix(a, ".png") {
		t.Fatalf("path %s", a)
	}
	if strings.Contains(a, "mac.local") {
		t.Fatal("the host name leaks into the file name")
	}
	if a != thumbnailPath("/cache", "user@mac.local:5900") {
		t.Fatal("not stable")
	}
}

func TestLastUsedAndRecentMacs(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	saved := []savedConnection{
		{Name: "never", Host: "a"},
		{Name: "old", Host: "b", LastUsed: base.Add(-48 * time.Hour)},
		{Name: "new", Host: "c", LastUsed: base.Add(-time.Hour)},
		{Name: "mid", Host: "d", LastUsed: base.Add(-24 * time.Hour)},
	}
	best, ok := lastUsedMac(saved)
	if !ok || best.Name != "new" {
		t.Fatalf("%+v %v", best, ok)
	}
	if _, ok := lastUsedMac([]savedConnection{{Name: "x"}}); ok {
		t.Fatal("a Mac never used is not the last used")
	}
	recent := recentMacs(saved)
	names := ""
	for _, c := range recent {
		names += c.Name + " "
	}
	if names != "new mid old " {
		t.Fatalf("order %q", names)
	}
}

func TestSummarizeSession(t *testing.T) {
	hdr := videoStats{Active: true, Codec: media.CodecHEVC, Color: media.Color{BitDepth: 10, ChromaFormat: 3, Transfer: media.TransferPQ}, SoundOn: true}
	got := summarizeSession(5120, 1440, hdr, rfb.EncodingMediaStream, false)
	if got != (lastSession{Width: 5120, Height: 1440, Codec: "HEVC 4:4:4", HDR: true, Sound: true}) {
		t.Fatalf("%+v", got)
	}
	if got := summarizeSession(5120, 1440, hdr, rfb.EncodingMediaStream, true); got.Sound {
		t.Fatal("a muted session had no sound to remember")
	}
	plain := videoStats{Active: true, Codec: media.CodecH264, Color: media.Color{BitDepth: 8, ChromaFormat: 1}}
	if got := summarizeSession(2560, 1600, plain, rfb.EncodingMediaStream, false); got.Codec != "H.264" || got.HDR || got.Sound {
		t.Fatalf("%+v", got)
	}
	if got := summarizeSession(1280, 800, videoStats{}, rfb.EncodingZRLE, false); got.Codec != "ZRLE" || got.Width != 1280 {
		t.Fatalf("%+v", got)
	}
}
