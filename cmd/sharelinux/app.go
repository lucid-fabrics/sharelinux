package main

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// now is the clock of the connection list, replaceable in tests.
var now = time.Now

// request is one connection to make: who, where, and the settings of this attempt.
type request struct {
	target   target
	password string
	// passwordVNC says which kind of login password belongs to: true for a VNC
	// password, false for a Mac account password. The two are never mixed.
	passwordVNC bool
	remember    bool
	opts        options
}

// vncLogin reports whether the request logs in with a VNC password instead of
// a Mac account.
func (r *request) vncLogin() bool {
	return r.opts.VNC || r.target.User == ""
}

// setPassword records a password together with the kind of login it was
// entered or saved for.
func (r *request) setPassword(password string, vnc bool) {
	r.password, r.passwordVNC = password, vnc
}

// dialPassword is the only password a dial may send: one that belongs to the
// kind of login the request makes. An account password must never go out as a
// VNC password, because that challenge can be cracked offline.
func (r *request) dialPassword() string {
	if r.passwordVNC != r.vncLogin() {
		return ""
	}
	return r.password
}

// switchLogin changes between an account login and a VNC password. The
// password belonged to the other kind, so it is dropped.
func (r *request) switchLogin(vnc bool) {
	r.opts.VNC = vnc
	r.setPassword("", vnc)
}

// vncLogin reports whether the saved connection logs in with a VNC password.
func (c savedConnection) vncLogin() bool {
	return c.VNC || c.User == ""
}

// loadSavedPassword puts the keyring password of a saved connection into the
// request, but only when the request makes the same kind of login. The keyring
// is not even read otherwise.
func (a *app) loadSavedPassword(req *request, saved savedConnection) {
	if saved.vncLogin() != req.vncLogin() {
		return
	}
	password, err := a.store.Password(saved)
	if err == nil {
		req.setPassword(password, saved.vncLogin())
	}
}

// app is the state every window shares.
type app struct {
	opts     *options
	logger   *slog.Logger
	store    *store
	prefs    *settingsStore
	settings settings

	access sync.Mutex

	helperOnce sync.Once
	helper     *helperKeys
	pairing    *helperPairing

	videoOnce sync.Once
	caps      videoCaps

	hdrOnce   sync.Once
	hdrAccess sync.Mutex
	hdrEnv    hdrEnv
	// gtkEnv is what prepareHDR added to the environment for GTK; it is taken out again once GTK has read it.
	gtkEnv []envChange
}

// videoMode is the video mode for new connections: the environment wins over the preferences.
func (a *app) videoMode() videoMode {
	if value := os.Getenv("SHARELINUX_VIDEO"); value != "" {
		return parseVideoMode(value)
	}
	if a.opts != nil && a.opts.Video != "" {
		return parseVideoMode(a.opts.Video)
	}
	return parseVideoMode(a.preferences().Video)
}

// soundWanted says whether a connection asks the Mac for its sound: the environment turns it off,
// then the flag, then the preference (which settings.apply has put into the options).
func (a *app) soundWanted(opts *options) bool {
	switch strings.ToLower(os.Getenv("SHARELINUX_SOUND")) {
	case "off", "0", "false", "no":
		return false
	}
	return opts == nil || !opts.NoSound
}

// chroma444Wanted says whether the video offer may name 4:4:4 8-bit: SHARELINUX_444=off keeps it to
// 4:2:0, to compare the two or to spare a GPU that decodes 4:4:4 slowly.
func (a *app) chroma444Wanted() bool {
	switch strings.ToLower(os.Getenv("SHARELINUX_444")) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

// videoCapabilities probes the decoders once. SHARELINUX_VAAPI_DEVICE names the
// VA-API render node to use when the machine has more than one GPU.
func (a *app) videoCapabilities() videoCaps {
	a.videoOnce.Do(func() {
		if videoProbe == nil {
			return
		}
		a.caps = videoProbe(os.Getenv("SHARELINUX_VAAPI_DEVICE"))
		if a.logger == nil {
			return
		}
		a.logger.Info("video decoders", "hevc-vaapi", a.caps.HEVCVAAPI, "hevc-software", a.caps.HEVCSoftware,
			"h264-vaapi", a.caps.H264VAAPI, "h264-software", a.caps.H264Software, "driver", a.caps.Driver, "vaapi-error", a.caps.Error)
	})
	return a.caps
}

// displayHDR asks the display server once what it offers for HDR, and hands out the last
// description of the screen the window is on. The first call has to come from the GTK thread
// after the display is open.
func (a *app) displayHDR() hdrEnv {
	a.hdrOnce.Do(func() {
		if hdrEnvironment == nil {
			return
		}
		env := hdrEnvironment()
		a.hdrAccess.Lock()
		a.hdrEnv = env
		a.hdrAccess.Unlock()
		if a.logger != nil {
			a.logger.Info("hdr display", "wayland", env.Wayland, "color-manager", env.ColorManager,
				"gtk-color-management", env.ColorManagement)
		}
		a.refreshHDROutput()
	})
	a.hdrAccess.Lock()
	defer a.hdrAccess.Unlock()
	return a.hdrEnv
}

// refreshHDROutput asks the compositor again what the screen the window is on shows, and says
// whether it is HDR now. The screen can change under a running app (the window moves, the person
// turns HDR on in the desktop's settings), so this is called when a session is about to start
// and when the window moves. It has to run on the GTK thread. Where nothing can be asked the
// description is left as it was.
func (a *app) refreshHDROutput() bool {
	if hdrOutputProbe == nil {
		a.hdrAccess.Lock()
		defer a.hdrAccess.Unlock()
		return a.hdrEnv.OutputHDR
	}
	color := hdrOutputProbe()
	a.hdrAccess.Lock()
	defer a.hdrAccess.Unlock()
	was := a.hdrEnv.OutputHDR
	a.hdrEnv = a.hdrEnv.withOutput(color)
	if a.logger != nil && (was != a.hdrEnv.OutputHDR || !color.Known) {
		a.logger.Info("hdr screen", "known", color.Known, "hdr", a.hdrEnv.OutputHDR, "transfer", color.Transfer,
			"max-nits", color.MaxLum, "target-max-nits", color.TargetMaxLum, "reference-nits", color.ReferenceLum)
	}
	return a.hdrEnv.OutputHDR
}

// hdrState says whether this machine can show HDR video.
func (a *app) hdrState() hdrState {
	return hdrAvailable(a.videoCapabilities(), a.videoMode(), a.displayHDR())
}

// hdrWanted is the person's wish for HDR: the flag or the preference.
func (a *app) hdrWanted() bool {
	return a.preferences().HDR || a.opts != nil && a.opts.HDR
}

// hdrOffReason says in a few words why a wish for HDR is not followed.
func (a *app) hdrOffReason() string {
	state := a.hdrState()
	if !state.Available {
		return state.Reason
	}
	return hdrNeedsRestart
}

// hdrEffective says whether a session asks the Mac for HDR video.
func (a *app) hdrEffective(wanted bool) bool {
	return hdrEffective(wanted, a.hdrState(), a.displayHDR())
}

// preferences returns the current preferences.
func (a *app) preferences() settings {
	a.access.Lock()
	defer a.access.Unlock()
	return a.settings
}

// updatePreferences changes the preferences and saves them.
func (a *app) updatePreferences(change func(*settings)) error {
	a.access.Lock()
	change(&a.settings)
	a.settings.sanitize()
	next := a.settings
	a.access.Unlock()
	if a.prefs == nil {
		return nil
	}
	return a.prefs.Save(next)
}

// newRequest builds the options of a connection: the command line over the
// saved connection over the preferences.
func (a *app) newRequest(target target, saved *savedConnection) (*request, error) {
	req := &request{opts: *a.opts, target: target}
	a.preferences().apply(&req.opts)
	if saved != nil {
		err := saved.apply(&req.opts)
		if err != nil {
			return nil, err
		}
		req.remember = true
	}
	return req, nil
}

// startRequest builds the request named on the command line, if any. The
// returned request may lack a password; the window then asks for it.
func (a *app) startRequest() (*request, error) {
	if a.opts.Target == "" {
		return nil, nil
	}
	var req *request
	if saved, found := a.store.Find(a.opts.Target); found {
		var err error
		req, err = a.newRequest(saved.target(), &saved)
		if err != nil {
			return nil, err
		}
		a.loadSavedPassword(req, saved)
	} else {
		parsed, err := parseTarget(a.opts.Target)
		if err != nil {
			return nil, err
		}
		req, err = a.newRequest(parsed, nil)
		if err != nil {
			return nil, err
		}
	}
	external, err := a.opts.readPassword(os.Getenv)
	if err != nil {
		return nil, err
	}
	if external != "" {
		req.setPassword(external, req.vncLogin())
	}
	req.remember = req.remember || a.opts.Save
	return req, nil
}

// remember stores the connection after it worked, when asked to, and notes the
// time of use of a saved one.
func (a *app) remember(req *request, name string) {
	if !req.remember {
		return
	}
	connection := savedConnection{
		Name:     name,
		User:     req.target.User,
		Host:     req.target.Host,
		Port:     req.target.Port,
		VNC:      req.opts.VNC,
		Mode:     req.opts.Mode.String(),
		Quality:  req.opts.Quality.String(),
		Keyboard: req.opts.Keyboard,
		LastUsed: now(),
	}
	if existing, found := a.store.Lookup(req.target); found && existing.Name != "" {
		connection.Name = existing.Name
	}
	err := a.store.Save(connection, req.dialPassword())
	if err != nil {
		a.logger.Warn("save connection", "error", err)
	}
}

// touch records that a saved connection was used without changing anything else.
func (a *app) touch(req *request) {
	existing, found := a.store.Lookup(req.target)
	if !found || req.remember {
		return
	}
	existing.LastUsed = now()
	err := a.store.Save(existing, "")
	if err != nil {
		a.logger.Warn("note last use", "error", err)
	}
}

func securityTypes(req *request) []rfb.SecurityType {
	if req.vncLogin() {
		return []rfb.SecurityType{rfb.SecurityVNCPassword, rfb.SecurityNone}
	}
	return nil
}

var errNoWindow = E.New("the graphical interface needs Linux with GTK 4 and libadwaita")

// helperKeys is the keyring-backed store of the Mac mouse helper keys.
func (a *app) helperKeys() *helperKeys {
	a.initHelper()
	return a.helper
}

// helperPairingFlow starts pairing attempts, one per explicit call.
func (a *app) helperPairingFlow() *helperPairing {
	a.initHelper()
	return a.pairing
}

func (a *app) initHelper() {
	a.helperOnce.Do(func() {
		a.helper = &helperKeys{secrets: keyringSecrets{}}
		a.pairing = newHelperPairing(a.helper)
	})
}
