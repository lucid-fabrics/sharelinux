package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// color444 is what the SPS of the Mac's standard range 4:4:4 stream says: BT.709, full range.
func color444() media.Color {
	return media.Color{BitDepth: 8, ChromaFormat: 3, Primaries: 1, Transfer: 1, Matrix: 1, FullRange: true}
}

func plan444Caps() videoCaps {
	return videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x8VAAPI: true, HEVC444x8Software: true}
}

func TestSDRPlanOffersOnly420(t *testing.T) {
	plan := planVideo(videoAuto, plan444Caps(), true)
	if plan.Chroma444 || plan.Offer.PixelFormats&media.PixelFormat444FullRange != 0 {
		t.Fatalf("the plain plan offers 4:4:4: %+v", plan)
	}
	if plan.Offer.Audio || plan.Sound {
		t.Fatal("the plain plan asks for sound")
	}
}

func TestWith444OffersFullRange444WhenTheGPUDecodesIt(t *testing.T) {
	plan := planVideo(videoAuto, plan444Caps(), true).with444(videoAuto, plan444Caps())
	if !plan.Chroma444 || plan.Offer.PixelFormats&media.PixelFormat444FullRange == 0 {
		t.Fatalf("no 4:4:4 offer: %+v", plan)
	}
	if plan.Offer.PixelFormats&media.PixelFormatsSDR420 != media.PixelFormatsSDR420 {
		t.Fatal("4:2:0 must stay in the offer for a Mac that cannot send 4:4:4")
	}
	if got := plan.kindsFor(color444()); len(got) != 1 || got[0] != decoderVAAPI {
		t.Fatalf("4:4:4 8-bit goes to %v, want the GPU alone", got)
	}
	if got := plan.kindsFor(media.Color{BitDepth: 8, ChromaFormat: 1}); len(got) != 2 {
		t.Fatalf("4:2:0 keeps both decoders, got %v", got)
	}
}

func TestWith444FallsBackTo420(t *testing.T) {
	cases := []struct {
		name string
		mode videoMode
		caps videoCaps
	}{
		{"GPU cannot decode the profile", videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x8Software: true}},
		{"software only", videoAuto, videoCaps{HEVCSoftware: true, HEVC444x8Software: true}},
		{"software mode", videoSoftware, plan444Caps()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := planVideo(c.mode, c.caps, true).with444(c.mode, c.caps)
			if plan.Chroma444 || plan.Offer.PixelFormats&media.PixelFormat444FullRange != 0 {
				t.Fatalf("offered 4:4:4: %+v", plan)
			}
		})
	}
	h264 := videoCaps{H264VAAPI: true, HEVC444x8VAAPI: true}
	if plan := planVideo(videoAuto, h264, true).with444(videoAuto, h264); plan.Chroma444 {
		t.Fatal("H.264 plans have no 4:4:4")
	}
	if plan := planVideo(videoOff, plan444Caps(), true).with444(videoOff, plan444Caps()); plan.Chroma444 || plan.enabled() {
		t.Fatal("a plan that is off stays off")
	}
}

func TestWithout444TakesItBackOut(t *testing.T) {
	plan := planVideo(videoAuto, plan444Caps(), true).with444(videoAuto, plan444Caps()).without444()
	if plan.Chroma444 || plan.Offer.PixelFormats&media.PixelFormat444FullRange != 0 || len(plan.Kinds444) != 0 {
		t.Fatalf("%+v", plan)
	}
	if plan.Offer.PixelFormats&media.PixelFormatsSDR420 != media.PixelFormatsSDR420 {
		t.Fatal("4:2:0 must stay")
	}
}

func TestDroppingTheGPUDecoderLeavesNoDecoderFor444(t *testing.T) {
	plan := planVideo(videoAuto, plan444Caps(), true).with444(videoAuto, plan444Caps()).withoutKind(decoderVAAPI)
	if got := plan.kindsFor(color444()); len(got) != 0 {
		t.Fatalf("software must not take a 4:4:4 stream: %v", got)
	}
	if got := plan.kindsFor(media.Color{BitDepth: 8, ChromaFormat: 1}); len(got) != 1 || got[0] != decoderSoftware {
		t.Fatalf("4:2:0 still decodes in software: %v", got)
	}
}

func TestDecoderPixelRateCountsA444PictureTwice(t *testing.T) {
	plain := planVideo(videoAuto, plan444Caps(), true)
	with := plain.with444(videoAuto, plan444Caps())
	if got, want := with.decoderPixelRate(4), plain.decoderPixelRate(4)/2; got != want {
		t.Fatalf("rate %d, want %d", got, want)
	}
	soft := videoCaps{HEVCSoftware: true}
	if got := planVideo(videoAuto, soft, true).with444(videoAuto, soft).decoderPixelRate(4); got != rateSoftwareThread*4 {
		t.Fatalf("software rate changed: %d", got)
	}
}

func TestStreamSupportedAndLabelFor444(t *testing.T) {
	c := color444()
	if streamSupported(c, false, false) || streamSupported(c, true, false) {
		t.Fatal("a 4:4:4 8-bit stream that was not offered must be refused")
	}
	if !streamSupported(c, false, true) {
		t.Fatal("an offered 4:4:4 8-bit stream must be taken")
	}
	if !streamSupported(media.Color{BitDepth: 8, ChromaFormat: 1}, false, false) {
		t.Fatal("4:2:0 is always taken")
	}
	if got := streamLabel(media.CodecHEVC, c, decoderVAAPI); got != "HEVC 4:4:4 (VA-API)" {
		t.Fatalf("label %q", got)
	}
	if got := streamLabel(media.CodecHEVC, media.Color{BitDepth: 8, ChromaFormat: 1}, decoderVAAPI); got != "HEVC (VA-API)" {
		t.Fatalf("label %q", got)
	}
}

func TestWithSoundOpensTheOffer(t *testing.T) {
	plan := planVideo(videoAuto, plan444Caps(), true)
	if got := plan.withSound(true, true); !got.Sound || !got.Offer.Audio {
		t.Fatalf("sound not asked for: %+v", got)
	}
	for _, c := range []struct{ wanted, decoder bool }{{false, true}, {true, false}, {false, false}} {
		if got := plan.withSound(c.wanted, c.decoder); got.Sound || got.Offer.Audio {
			t.Fatalf("wanted %v decoder %v: asked for sound", c.wanted, c.decoder)
		}
	}
	if got := planVideo(videoOff, plan444Caps(), true).withSound(true, true); got.Sound || got.Offer.Audio {
		t.Fatal("a plan that is off asks for nothing")
	}
}

func TestSoundIsOnByDefaultAndPersists(t *testing.T) {
	if !defaultSettings().Sound {
		t.Fatal("sound must start on")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	store := &settingsStore{path: path}
	// A file from before the setting existed keeps the default.
	if err := os.WriteFile(path, []byte(`{"show_stats": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || !got.Sound {
		t.Fatalf("a missing key must keep sound on: %+v %v", got, err)
	}
	got.Sound = false
	if err := store.Save(got); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load()
	if err != nil || again.Sound {
		t.Fatalf("sound off did not persist: %+v %v", again, err)
	}
}

func TestSoundPreferenceReachesTheOptions(t *testing.T) {
	s := defaultSettings()
	opts, err := parseArgs(nil, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	s.apply(opts)
	if opts.NoSound {
		t.Fatal("sound on in the preferences must not set -no-sound")
	}
	s.Sound = false
	opts, _ = parseArgs(nil, os.Stderr)
	s.apply(opts)
	if !opts.NoSound {
		t.Fatal("sound off in the preferences must set NoSound")
	}
	// The flag on the command line wins over a preference that has sound on.
	s.Sound = true
	opts, _ = parseArgs([]string{"-no-sound"}, os.Stderr)
	s.apply(opts)
	if !opts.NoSound {
		t.Fatal("-no-sound must win")
	}
}

func TestSoundWantedHonoursEnvironmentAndOptions(t *testing.T) {
	a := &app{}
	t.Setenv("SHARELINUX_SOUND", "")
	if !a.soundWanted(&options{}) || !a.soundWanted(nil) {
		t.Fatal("sound should be wanted by default")
	}
	if a.soundWanted(&options{NoSound: true}) {
		t.Fatal("NoSound ignored")
	}
	t.Setenv("SHARELINUX_SOUND", "off")
	if a.soundWanted(&options{}) {
		t.Fatal("SHARELINUX_SOUND=off ignored")
	}
}

func TestStatsRowForTheSound(t *testing.T) {
	rows := applyVideoRows([]statRow{{"Codec", "x"}}, videoStats{
		Active: true, SoundOn: true,
		Sound: soundStats{Counters: audioCounters{Depth: 4, Underruns: 1, Dropped: 2}, Device: 40_000_000},
	})
	var got string
	for _, row := range rows {
		if row.Label == "Sound" {
			got = row.Value
		}
	}
	if got != "80 ms, 1 gaps, 2 dropped" {
		t.Fatalf("sound row %q", got)
	}
	rows = applyVideoRows([]statRow{{"Codec", "x"}}, videoStats{Active: true, SoundOn: true, Sound: soundStats{Muted: true}})
	for _, row := range rows {
		if row.Label == "Sound" && row.Value != "Muted" {
			t.Fatalf("muted row %q", row.Value)
		}
	}
	for _, row := range applyVideoRows([]statRow{{"Codec", "x"}}, videoStats{Active: true}) {
		if row.Label == "Sound" {
			t.Fatal("a stream without sound has no Sound row")
		}
	}
}
