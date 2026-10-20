package main

import (
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestAllowedMode(t *testing.T) {
	for _, mode := range []rfb.Mode{rfb.ModeObserve, rfb.ModeControl, rfb.ModeExclusiveControl} {
		if got := allowedMode(mode, rfb.ServerFlagObserveOnly); got != rfb.ModeObserve {
			t.Errorf("view-only server allowed %v", got)
		}
		if got := allowedMode(mode, 0); got != mode {
			t.Errorf("%v changed to %v", mode, got)
		}
	}
}

func TestQualityLabels(t *testing.T) {
	for _, quality := range qualities {
		label := qualityLabel(quality)
		if label == "" || label[0] < 'A' || label[0] > 'Z' {
			t.Errorf("%v: %q", quality, label)
		}
		if qualityHint(quality) == "" {
			t.Errorf("%v has no hint", quality)
		}
	}
	if qualityLabel(rfb.QualityAdaptive) != "Adaptive" {
		t.Fatal("label")
	}
}

func sampleLayout() *rfb.DisplayLayout {
	return &rfb.DisplayLayout{
		Current: 7,
		Displays: []rfb.Display{
			{ID: 3, Flags: rfb.DisplayFlagMain},
			{ID: 7},
			{ID: 9, Flags: rfb.DisplayFlagVirtual},
		},
	}
}

func TestDisplayChoices(t *testing.T) {
	choices := displayChoices(sampleLayout())
	want := []displayChoice{{0, "All displays"}, {3, "Main display"}, {7, "Display 2"}, {9, "Virtual display"}}
	if len(choices) != len(want) {
		t.Fatalf("%+v", choices)
	}
	for index := range want {
		if choices[index] != want[index] {
			t.Errorf("%d: %+v, want %+v", index, choices[index], want[index])
		}
	}
	if len(displayChoices(nil)) != 1 {
		t.Fatal("no layout offers only all displays")
	}
}

func TestDisplayLabelAndMain(t *testing.T) {
	layout := sampleLayout()
	if displayLabel(layout) != "Display 2" || currentDisplay(layout) != 7 {
		t.Fatal(displayLabel(layout))
	}
	layout.Current = rfb.AllDisplays
	if displayLabel(layout) != "All displays" || currentDisplay(layout) != 0 {
		t.Fatal("all displays")
	}
	layout.Current = 55
	if displayLabel(layout) != "All displays" {
		t.Fatal("an unknown current display falls back")
	}
	if id, ok := mainDisplay(layout); !ok || id != 3 {
		t.Fatal("main display")
	}
	if _, ok := mainDisplay(&rfb.DisplayLayout{}); ok {
		t.Fatal("no displays, no main")
	}
	if displayLabel(nil) != "All displays" {
		t.Fatal("nil")
	}
}

func TestStatRows(t *testing.T) {
	rows := statRows(rfb.ServerInfo{Dialect: rfb.DialectApple}, 59.6, 14.2, 18.2e6, rfb.EncodingMVS, 2560, 1600)
	want := []statRow{{"Frame rate", "60 fps"}, {"Latency", "14 ms"}, {"Bandwidth", "18.2 Mbit/s"}, {"Codec", "Video"}, {"Resolution", "2560×1600"}}
	if len(rows) != len(want) {
		t.Fatalf("%+v", rows)
	}
	for index := range want {
		if rows[index] != want[index] {
			t.Errorf("%+v, want %+v", rows[index], want[index])
		}
	}
	plain := statRows(rfb.ServerInfo{Dialect: rfb.DialectStandard}, 30, 5, 0, rfb.EncodingZRLE, 640, 400)
	for _, row := range plain {
		if row.Label == "Bandwidth" {
			t.Fatal("a server without a byte count has no bandwidth row")
		}
	}
	if len(plain) != 4 {
		t.Fatalf("%+v", plain)
	}
}

func TestConnectedText(t *testing.T) {
	if connectedText(0) != "Connected" || connectedText(60) != "Connected · 60 fps" {
		t.Fatal("subtitle")
	}
}

func TestCodecLabelNames(t *testing.T) {
	if codecLabel(rfb.EncodingZRLE) != "ZRLE" || codecLabel(rfb.EncodingRaw) != "Raw" {
		t.Fatal("codec")
	}
}
