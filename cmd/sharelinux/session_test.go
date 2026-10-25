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
	want := []statRow{{"Codec", "Video"}, {"Picture", "2560 × 1600 · 60 fps"}, {"Delay", "14 ms"}, {"Bandwidth", "18.2 Mbit/s"}}
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
	if len(plain) != 3 {
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

func TestVideoDisplayReady(t *testing.T) {
	two := []rfb.Display{{ID: 1, Flags: rfb.DisplayFlagMain}, {ID: 2}}
	cases := []struct {
		name   string
		layout *rfb.DisplayLayout
		want   bool
	}{
		{"no layout yet", nil, false},
		{"several displays together", &rfb.DisplayLayout{Current: rfb.AllDisplays, Displays: two}, false},
		{"one display of several", &rfb.DisplayLayout{Current: 2, Displays: two}, true},
		{"a Mac with one display, all selected", &rfb.DisplayLayout{Current: rfb.AllDisplays, Displays: two[:1]}, true},
		{"a layout without displays", &rfb.DisplayLayout{Current: rfb.AllDisplays}, true},
	}
	for _, c := range cases {
		if got := videoDisplayReady(c.layout); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAppendSample(t *testing.T) {
	var history []float64
	for sample := 1.0; sample <= 5; sample++ {
		history = appendSample(history, sample, 3)
	}
	if len(history) != 3 || history[0] != 3 || history[2] != 5 {
		t.Fatalf("%v", history)
	}
}

func TestSparkPoints(t *testing.T) {
	if sparkPoints([]float64{12}, 208, 36, 4) != nil {
		t.Fatal("one sample draws no line")
	}
	if sparkPoints([]float64{12, 13}, 6, 36, 4) != nil {
		t.Fatal("a box smaller than its padding draws no line")
	}
	steady := sparkPoints([]float64{12, 12, 12, 12}, 208, 36, 4)
	if len(steady) != 4 || steady[0][0] != 4 || steady[3][0] != 204 {
		t.Fatalf("%v", steady)
	}
	for _, point := range steady {
		if point[1] != 18 {
			t.Fatalf("a steady delay draws a centred line: %v", steady)
		}
	}
	rising := sparkPoints([]float64{10, 20, 30}, 100, 40, 4)
	if !(rising[0][1] > rising[1][1] && rising[1][1] > rising[2][1]) {
		t.Fatalf("a higher delay is drawn higher: %v", rising)
	}
	for _, point := range rising {
		if point[1] < 4 || point[1] > 36 {
			t.Fatalf("inside the padding: %v", point)
		}
	}
}
