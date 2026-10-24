package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/nekohasekai/open-screen-sharing/cmd/internal/ctl"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// Sizes the Displays page offers for a virtual display, in points. The first
// entry follows the window instead of a fixed size.
const virtualSizeWindow = "window"

var virtualSizes = []string{virtualSizeWindow, "1920x1080", "2560x1440", "3840x2160"}

// settings are the preferences. They hold no secrets, so they live as plain
// JSON next to connections.json.
type settings struct {
	Quality          string `json:"quality"`
	FitWindow        bool   `json:"fit_window"`
	ShowStats        bool   `json:"show_stats"`
	CommandKey       string `json:"command_key"`
	NaturalScrolling bool   `json:"natural_scrolling"`
	SharedClipboard  bool   `json:"shared_clipboard"`
	DownloadDir      string `json:"download_dir"`
	MainDisplay      bool   `json:"main_display"`
	VirtualDisplay   bool   `json:"virtual_display"`
	VirtualSize      string `json:"virtual_size"`
	HiDPI            bool   `json:"hidpi"`
	HDR              bool   `json:"hdr"`
	Video            string `json:"video"`
	// MouseHelper lets a paired Mac helper carry the locked pointer. It starts
	// on: pairing is the person's opt-in, and a file without the key keeps it on.
	MouseHelper bool `json:"mouse_helper"`
	// Sound plays the Mac's sound on this computer. It starts on: a file without the key keeps it on.
	Sound bool `json:"sound"`
}

func defaultSettings() settings {
	return settings{
		Quality:          rfb.QualityAdaptive.String(),
		FitWindow:        true,
		CommandKey:       commandKeySuper,
		NaturalScrolling: true,
		SharedClipboard:  true,
		VirtualSize:      virtualSizeWindow,
		Video:            string(videoAuto),
		MouseHelper:      true,
		Sound:            true,
	}
}

// sanitize replaces values a hand-edited file could break.
func (s *settings) sanitize() {
	defaults := defaultSettings()
	if _, err := rfb.ParseQuality(s.Quality); err != nil {
		s.Quality = defaults.Quality
	}
	if s.CommandKey != commandKeySuper && s.CommandKey != commandKeyAlt {
		s.CommandKey = defaults.CommandKey
	}
	s.Video = string(parseVideoMode(s.Video))
	known := false
	for _, size := range virtualSizes {
		known = known || size == s.VirtualSize
	}
	if !known {
		s.VirtualSize = defaults.VirtualSize
	}
}

// apply puts the preferences into the options of a connection. A flag given on
// the command line always wins.
func (s settings) apply(opts *options) {
	quality, err := rfb.ParseQuality(s.Quality)
	if err == nil && !opts.Explicit["quality"] {
		opts.Quality = quality
	}
	if !opts.Explicit["no-clipboard"] {
		opts.NoClipboard = !s.SharedClipboard
	}
	if !opts.Explicit["no-sound"] {
		opts.NoSound = !s.Sound
	}
	if !opts.Explicit["hud"] {
		opts.HUD = s.ShowStats
	}
	if !opts.Explicit["hdr"] {
		opts.HDR = s.HDR
	}
	if !opts.Explicit["virtual"] && s.VirtualDisplay && s.VirtualSize != virtualSizeWindow {
		width, height, err := ctl.ParseSize(s.VirtualSize)
		if err == nil {
			density := 1.0
			if s.HiDPI {
				density = 2
			}
			opts.Virtual = &virtualDisplay{Width: width, Height: height, Density: density}
		}
	}
}

// virtualFollowsWindow reports whether the virtual display should be asked for
// at the size of the window once the session shows.
func (s settings) virtualFollowsWindow(opts *options) bool {
	return s.VirtualDisplay && s.VirtualSize == virtualSizeWindow && opts.Virtual == nil && !opts.Explicit["virtual"]
}

// windowVirtualDisplay is the virtual display to ask for when the preferences
// say it follows the window: the size of the window in points, doubled in
// density when the HiDPI preference is on. It reports false while the window
// has no size yet.
func (s settings) windowVirtualDisplay(opts *options, width, height int) (virtualDisplay, bool) {
	if !s.virtualFollowsWindow(opts) || width <= 0 || height <= 0 {
		return virtualDisplay{}, false
	}
	density := 1.0
	if s.HiDPI {
		density = 2
	}
	return virtualDisplay{Width: width, Height: height, Density: density}, true
}

// downloadFolder is where files from the Mac land.
func (s settings) downloadFolder() string {
	if s.DownloadDir != "" {
		return s.DownloadDir
	}
	return downloadDirectory()
}

// settingsStore reads and writes settings.json.
type settingsStore struct {
	path   string
	access sync.Mutex
}

func defaultSettingsPath() (string, error) {
	directory, err := configDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "settings.json"), nil
}

// Load returns the saved preferences, or the defaults when there is no file or it cannot be read.
func (s *settingsStore) Load() (settings, error) {
	s.access.Lock()
	defer s.access.Unlock()
	loaded := defaultSettings()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return loaded, nil
	}
	if err != nil {
		return defaultSettings(), E.Cause(err, "read preferences")
	}
	err = json.Unmarshal(data, &loaded)
	if err != nil {
		return defaultSettings(), E.Cause(err, "parse ", s.path)
	}
	loaded.sanitize()
	return loaded, nil
}

func (s *settingsStore) Save(value settings) error {
	s.access.Lock()
	defer s.access.Unlock()
	value.sanitize()
	err := os.MkdirAll(filepath.Dir(s.path), 0o700)
	if err != nil {
		return E.Cause(err, "create the config directory")
	}
	data, err := json.MarshalIndent(value, "", "\t")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*")
	if err != nil {
		return E.Cause(err, "write preferences")
	}
	defer os.Remove(temporary.Name())
	_, err = temporary.Write(data)
	if err == nil {
		err = temporary.Chmod(0o600)
	}
	closeErr := temporary.Close()
	if err = E.Errors(err, closeErr); err != nil {
		return E.Cause(err, "write preferences")
	}
	return os.Rename(temporary.Name(), s.path)
}
