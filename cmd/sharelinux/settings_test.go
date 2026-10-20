package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestSettingsDefaultsWithoutFile(t *testing.T) {
	store := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json")}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != defaultSettings() {
		t.Fatalf("%+v", got)
	}
	if !got.FitWindow || !got.SharedClipboard || !got.NaturalScrolling || got.ShowStats {
		t.Fatalf("defaults drifted: %+v", got)
	}
}

func TestSettingsRoundTripKeepsPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	store := &settingsStore{path: path}
	want := defaultSettings()
	want.Quality = "high"
	want.CommandKey = commandKeyAlt
	want.DownloadDir = "/data/in"
	want.VirtualDisplay, want.VirtualSize, want.HiDPI = true, "2560x1440", true
	err := store.Save(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode %v %v", info.Mode(), err)
	}
}

func TestSettingsPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"show_stats": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (&settingsStore{path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.ShowStats || !got.SharedClipboard || got.Quality != "adaptive" {
		t.Fatalf("a missing key must keep its default: %+v", got)
	}
}

func TestSettingsRejectBrokenValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"quality":"ultra","command_key":"hyper","virtual_size":"9x9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (&settingsStore{path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Quality != "adaptive" || got.CommandKey != commandKeySuper || got.VirtualSize != virtualSizeWindow {
		t.Fatalf("%+v", got)
	}
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = (&settingsStore{path: path}).Load()
	if err == nil || got != defaultSettings() {
		t.Fatal("a corrupt file falls back to the defaults and reports it")
	}
}

func TestSettingsApplyRespectsExplicitFlags(t *testing.T) {
	s := defaultSettings()
	s.Quality, s.SharedClipboard, s.ShowStats, s.HDR = "low", false, true, true
	opts := &options{Quality: rfb.QualityFull, Explicit: map[string]bool{"quality": true}}
	s.apply(opts)
	if opts.Quality != rfb.QualityFull {
		t.Fatal("an explicit -quality wins")
	}
	if !opts.NoClipboard || !opts.HUD || !opts.HDR {
		t.Fatalf("%+v", opts)
	}
	opts = &options{Explicit: map[string]bool{"no-clipboard": true, "hud": true}}
	s.apply(opts)
	if opts.NoClipboard || opts.HUD {
		t.Fatal("explicit flags keep their own value")
	}
	if opts.Quality != rfb.QualityLow {
		t.Fatal("the preference sets the quality otherwise")
	}
}

func TestSettingsVirtualDisplay(t *testing.T) {
	s := defaultSettings()
	opts := &options{Explicit: map[string]bool{}}
	s.apply(opts)
	if opts.Virtual != nil || s.virtualFollowsWindow(opts) {
		t.Fatal("off by default")
	}
	s.VirtualDisplay, s.VirtualSize, s.HiDPI = true, "1920x1080", true
	s.apply(opts)
	if opts.Virtual == nil || opts.Virtual.Width != 1920 || opts.Virtual.Height != 1080 || opts.Virtual.Density != 2 {
		t.Fatalf("%+v", opts.Virtual)
	}
	s.VirtualSize = virtualSizeWindow
	opts = &options{Explicit: map[string]bool{}}
	s.apply(opts)
	if opts.Virtual != nil || !s.virtualFollowsWindow(opts) {
		t.Fatal("window size is decided once the window shows")
	}
	opts.Explicit["virtual"] = true
	if s.virtualFollowsWindow(opts) {
		t.Fatal("-virtual on the command line wins")
	}
}

func TestDownloadFolder(t *testing.T) {
	s := defaultSettings()
	s.DownloadDir = "/somewhere"
	if s.downloadFolder() != "/somewhere" {
		t.Fatal("explicit folder")
	}
	s.DownloadDir = ""
	if s.downloadFolder() == "" {
		t.Fatal("falls back to the downloads folder")
	}
}
