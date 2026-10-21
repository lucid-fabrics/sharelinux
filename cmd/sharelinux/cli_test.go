package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != rfb.ModeControl || opts.Quality != rfb.QualityAdaptive || opts.Keyboard != "auto" || opts.Target != "" {
		t.Fatalf("%+v", opts)
	}
}

func TestParseArgsFlagsAfterTarget(t *testing.T) {
	opts, err := parseArgs([]string{"me@mac", "-mode", "observe", "-quality=low", "-virtual", "1920x1080@2", "-hud"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Target != "me@mac" || opts.Mode != rfb.ModeObserve || opts.Quality != rfb.QualityLow || !opts.HUD {
		t.Fatalf("%+v", opts)
	}
	if opts.Virtual == nil || *opts.Virtual != (virtualDisplay{1920, 1080, 2}) {
		t.Fatalf("%+v", opts.Virtual)
	}
	if !opts.Explicit["mode"] || opts.Explicit["keyboard"] {
		t.Fatalf("%v", opts.Explicit)
	}
}

func TestParseArgsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-mode", "root"}, {"-quality", "ultra"}, {"-keyboard", "dvorak"}, {"-session", "x"},
		{"-virtual", "100"}, {"-virtual", "100x100@3"}, {"-size", "10x10"}, {"a", "b"}, {"-nope"},
	} {
		if _, err := parseArgs(args, io.Discard); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestReadPassword(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(file, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := &options{PasswordFile: file}
	got, err := opts.readPassword(func(string) string { return "env" })
	if err != nil || got != "secret" {
		t.Fatal(got, err)
	}
	opts = &options{}
	got, _ = opts.readPassword(func(name string) string {
		if name == "SHARELINUX_PASSWORD" {
			return "env"
		}
		return ""
	})
	if got != "env" {
		t.Fatal(got)
	}
	opts = &options{PasswordFile: filepath.Join(t.TempDir(), "missing")}
	if _, err := opts.readPassword(func(string) string { return "" }); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestVideoFlag(t *testing.T) {
	opts, err := parseArgs([]string{"-video", "software", "mock@host"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Video != "software" || !opts.Explicit["video"] {
		t.Fatalf("%+v", opts)
	}
	a := &app{opts: opts, prefs: nil, settings: defaultSettings()}
	if a.videoMode() != videoSoftware {
		t.Fatal("the flag sets the mode")
	}
	t.Setenv("SHARELINUX_VIDEO", "off")
	if a.videoMode() != videoOff {
		t.Fatal("the environment wins over the flag")
	}
	t.Setenv("SHARELINUX_VIDEO", "")
	plain, err := parseArgs([]string{"mock@host"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	b := &app{opts: plain, settings: settings{Video: "off"}}
	if b.videoMode() != videoOff {
		t.Fatal("without a flag the preference decides")
	}
}
