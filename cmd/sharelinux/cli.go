package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/cmd/internal/ctl"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

const usage = `usage: sharelinux [flags] [USER@HOST[:PORT] | SAVED-NAME]

Without an address the window lists your saved Macs and the Macs found on the
network. The password comes from the keyring, SHARELINUX_PASSWORD,
-password-file, or is typed into the sign-in dialog.

In a session Ctrl+Alt+Esc leaves exclusive mode. Hold Ctrl+Alt+Shift and press:
  M cycle view/control/exclusive   H stats   F fullscreen   U quality
  D cycle displays   V virtual display at window size   X toggle HDR
  R refresh   T type the local clipboard   G fetch files of the Mac's drag
  S save a screenshot   Q quit

flags:
`

// options is everything the command line can set.
type options struct {
	Target       string
	PasswordFile string
	VNC          bool
	Mode         rfb.Mode
	Quality      rfb.Quality
	Keyboard     string
	Display      uint32
	Virtual      *virtualDisplay
	HDR          bool
	NoClipboard  bool
	NoSound      bool
	NoGrab       bool
	Fullscreen   bool
	HUD          bool
	// Video is the video mode given on the command line, empty when it was not.
	Video           string
	Save            bool
	List            bool
	Forget          string
	Session         string
	AllowUnverified bool
	ClientName      string
	Verbose         bool
	WindowWidth     int
	WindowHeight    int
	ScreenshotDir   string

	// Explicit names the flags that were given, so a saved connection does not override them.
	Explicit map[string]bool
}

type virtualDisplay struct {
	Width   int
	Height  int
	Density float64
}

func (v virtualDisplay) String() string {
	return fmt.Sprintf("%dx%d@%g", v.Width, v.Height, v.Density)
}

func parseVirtual(value string) (*virtualDisplay, error) {
	size, densityText, _ := strings.Cut(value, "@")
	width, height, err := ctl.ParseSize(size)
	if err != nil {
		return nil, err
	}
	if width <= 0 || height <= 0 {
		return nil, E.New("virtual display size ", strconv.Quote(size), " must be positive")
	}
	density := 1.0
	if densityText != "" {
		density, err = strconv.ParseFloat(densityText, 64)
		if err != nil || (density != 1 && density != 2) {
			return nil, E.New("virtual display density ", strconv.Quote(densityText), " must be 1 or 2")
		}
	}
	return &virtualDisplay{Width: width, Height: height, Density: density}, nil
}

func parseMode(value string) (rfb.Mode, error) {
	for mode := rfb.ModeObserve; mode <= rfb.ModeExclusiveControl; mode++ {
		if mode.String() == value {
			return mode, nil
		}
	}
	return 0, E.New("unknown mode ", strconv.Quote(value), ", use observe, control or exclusive")
}

func parseKeyboard(value string) (string, error) {
	switch value {
	case "auto", "ansi", "iso", "jis":
		return value, nil
	}
	return "", E.New("unknown keyboard ", strconv.Quote(value), ", use auto, ansi, iso or jis")
}

func parseSessionAnswer(value string) (string, error) {
	switch value {
	case "ask", "console", "own", "cancel":
		return value, nil
	}
	return "", E.New("unknown session answer ", strconv.Quote(value), ", use ask, console, own or cancel")
}

// parseArgs parses the command line. Flags may come before or after the address.
func parseArgs(args []string, output io.Writer) (*options, error) {
	opts := &options{}
	set := flag.NewFlagSet("sharelinux", flag.ContinueOnError)
	set.SetOutput(output)
	var mode, quality, keyboard, virtual, session, size string
	var display uint
	set.StringVar(&opts.PasswordFile, "password-file", "", "read the password from this file")
	set.BoolVar(&opts.VNC, "vnc", false, "log in with the VNC password instead of a Mac account")
	set.StringVar(&mode, "mode", "control", "observe, control or exclusive")
	set.StringVar(&quality, "quality", "adaptive", "adaptive, full, high, medium or low")
	set.StringVar(&keyboard, "keyboard", "auto", "keyboard type: auto, ansi, iso or jis")
	set.UintVar(&display, "display", 0, "display id to share, 0 for all")
	set.StringVar(&virtual, "virtual", "", "ask for a virtual display WxH[@DENSITY] in points, density 1 or 2")
	set.BoolVar(&opts.HDR, "hdr", false, "ask for HDR video (Wayland with color management); the virtual display follows")
	set.BoolVar(&opts.NoClipboard, "no-clipboard", false, "do not share the clipboard")
	set.BoolVar(&opts.NoSound, "no-sound", false, "do not play the Mac's sound (it comes with the video stream); SHARELINUX_SOUND=off does the same")
	set.BoolVar(&opts.NoGrab, "no-grab", false, "do not grab the keyboard in control mode")
	set.BoolVar(&opts.Fullscreen, "fullscreen", false, "start fullscreen")
	set.BoolVar(&opts.HUD, "hud", false, "start with the stats overlay on")
	set.StringVar(&opts.Video, "video", "", "the Mac's video stream: auto, software or off (picture tiles); SHARELINUX_VIDEO does the same")
	set.BoolVar(&opts.Save, "save", false, "remember this connection (password in the keyring) after it connects")
	set.BoolVar(&opts.List, "list", false, "list saved connections and exit")
	set.StringVar(&opts.Forget, "forget", "", "delete the saved connection with this name or address, and its password")
	set.StringVar(&session, "session", "ask", "when the Mac offers sessions: ask, console, own or cancel")
	set.BoolVar(&opts.AllowUnverified, "allow-unverified", false, "allow logins that send the password to an unverified server")
	set.StringVar(&opts.ClientName, "name", "sharelinux", "name shown to the Mac's user")
	set.BoolVar(&opts.Verbose, "v", false, "debug logging")
	set.StringVar(&size, "size", "1280x800", "initial window size WxH")
	set.StringVar(&opts.ScreenshotDir, "screenshot-dir", "", "where screenshots are saved (default: the downloads folder)")
	set.Usage = func() {
		fmt.Fprint(output, usage)
		set.PrintDefaults()
	}
	rest := args
	for {
		err := set.Parse(rest)
		if err != nil {
			return nil, err
		}
		if set.NArg() == 0 {
			break
		}
		if opts.Target != "" {
			return nil, E.New("more than one address: ", strconv.Quote(opts.Target), " and ", strconv.Quote(set.Arg(0)))
		}
		opts.Target = set.Arg(0)
		rest = set.Args()[1:]
	}
	opts.Explicit = make(map[string]bool)
	set.Visit(func(f *flag.Flag) {
		opts.Explicit[f.Name] = true
	})
	var err error
	opts.Mode, err = parseMode(mode)
	if err != nil {
		return nil, err
	}
	opts.Quality, err = rfb.ParseQuality(quality)
	if err != nil {
		return nil, err
	}
	opts.Keyboard, err = parseKeyboard(keyboard)
	if err != nil {
		return nil, err
	}
	opts.Session, err = parseSessionAnswer(session)
	if err != nil {
		return nil, err
	}
	if virtual != "" {
		opts.Virtual, err = parseVirtual(virtual)
		if err != nil {
			return nil, err
		}
	}
	if display > uint(^uint32(0)) {
		return nil, E.New("display id ", display, " is out of range")
	}
	opts.Display = uint32(display)
	opts.WindowWidth, opts.WindowHeight, err = ctl.ParseSize(size)
	if err != nil || opts.WindowWidth < 320 || opts.WindowHeight < 200 {
		return nil, E.New("window size ", strconv.Quote(size), " must be WxH, at least 320x200")
	}
	return opts, nil
}

// readPassword returns the password given outside the login form, in order of
// preference: the password file, then SHARELINUX_PASSWORD. It is never taken from a flag.
func (o *options) readPassword(getenv func(string) string) (string, error) {
	if o.PasswordFile != "" {
		data, err := os.ReadFile(o.PasswordFile)
		if err != nil {
			return "", E.Cause(err, "read password file")
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	return getenv("SHARELINUX_PASSWORD"), nil
}
