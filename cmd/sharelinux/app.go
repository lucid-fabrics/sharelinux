package main

import (
	"log/slog"
	"os"
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
	remember bool
	opts     options
}

// app is the state every window shares.
type app struct {
	opts     *options
	logger   *slog.Logger
	store    *store
	prefs    *settingsStore
	settings settings

	access sync.Mutex
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
		password, err := a.store.Password(saved)
		if err == nil {
			req.password = password
		}
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
		req.password = external
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
	err := a.store.Save(connection, req.password)
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
	if req.opts.VNC || req.target.User == "" {
		return []rfb.SecurityType{rfb.SecurityVNCPassword, rfb.SecurityNone}
	}
	return nil
}

var errNoWindow = E.New("the graphical interface needs Linux with GTK 4 and libadwaita")
