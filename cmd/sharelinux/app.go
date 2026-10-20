package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// request is one connection to make: who, where, and the settings of this attempt.
type request struct {
	target   target
	password string
	remember bool
	opts     options
}

type app struct {
	opts   *options
	logger *slog.Logger
	store  *store
	canvas *canvas
}

const windowTitle = "Open Screen Sharing"

func (a *app) open() error {
	err := loadSDL()
	if err != nil {
		return err
	}
	flags := sdl.WINDOW_RESIZABLE | sdl.WINDOW_HIGH_PIXEL_DENSITY
	if a.opts.Fullscreen {
		flags |= sdl.WINDOW_FULLSCREEN
	}
	window, renderer, err := sdl.CreateWindowAndRenderer(windowTitle, a.opts.WindowWidth, a.opts.WindowHeight, flags)
	if err != nil {
		return E.Cause(err, "create the window")
	}
	_ = renderer.SetVSync(1)
	_ = window.SetMinimumSize(320, 200)
	a.canvas = &canvas{window: window, renderer: renderer}
	return nil
}

func (a *app) close() {
	if a.canvas != nil {
		a.canvas.renderer.Destroy()
		a.canvas.window.Destroy()
	}
	sdl.Quit()
}

// run is the whole life of the window: pick or enter a Mac, connect, view, repeat.
func (a *app) run() error {
	req, err := a.startRequest()
	if err != nil {
		return err
	}
	for {
		if req == nil {
			req, err = a.chooseRequest()
			if errors.Is(err, errQuit) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		live, err := a.connect(req)
		if errors.Is(err, errQuit) {
			return nil
		}
		if err != nil {
			req, err = a.afterFailure(req, err)
			if errors.Is(err, errQuit) {
				return nil
			}
			if err != nil {
				return err
			}
			continue
		}
		a.remember(req)
		err = a.view(live)
		_ = a.canvas.window.SetTitle(windowTitle)
		if errors.Is(err, errQuit) {
			return nil
		}
		message := "The connection to " + req.target.Address() + " ended."
		if err != nil {
			_, message = describeError(err, req.target.Address())
		}
		key, msgErr := a.showMessage("Disconnected", message+"\n\nEnter: reconnect   P: pick another Mac   Esc: quit", sdl.K_P)
		switch {
		case msgErr != nil, key == sdl.K_ESCAPE:
			return nil
		case key == sdl.K_P:
			req = nil
		}
	}
}

// startRequest builds the request named on the command line, if any.
func (a *app) startRequest() (*request, error) {
	if a.opts.Target == "" {
		return nil, nil
	}
	req := &request{opts: *a.opts}
	if saved, found := a.store.Find(a.opts.Target); found {
		req.target = saved.target()
		err := saved.apply(&req.opts)
		if err != nil {
			return nil, err
		}
		password, err := a.store.Password(saved)
		if err == nil {
			req.password = password
		}
	} else {
		var err error
		req.target, err = parseTarget(a.opts.Target)
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
	req.remember = a.opts.Save
	if req.password == "" {
		return a.askLogin(req, "")
	}
	return req, nil
}

// askLogin runs the login form seeded from req and returns the filled request.
func (a *app) askLogin(req *request, problem string) (*request, error) {
	form := newLoginForm(req.target, req.remember)
	form.Error = problem
	form.Fields[fieldPassword].Value = nil
	result, err := a.runForm(form)
	if err != nil {
		return nil, err
	}
	next := *req
	next.target, next.password, next.remember = result.Target, result.Password, result.Remember
	return &next, nil
}

// chooseRequest shows the picker of saved connections, or the form when there are none.
func (a *app) chooseRequest() (*request, error) {
	items, err := a.store.List()
	notice := ""
	if err != nil {
		notice = err.Error()
	}
	base := &request{opts: *a.opts}
	if len(items) == 0 {
		return a.askLogin(base, "")
	}
	for {
		chosen, err := a.runPicker(items, notice)
		if err != nil {
			return nil, err
		}
		if !chosen.Saved {
			return a.askLogin(base, "")
		}
		req := &request{opts: *a.opts, target: chosen.Connection.target(), remember: true}
		err = chosen.Connection.apply(&req.opts)
		if err != nil {
			notice = err.Error()
			continue
		}
		password, err := a.store.Password(chosen.Connection)
		if err != nil {
			problem := ""
			if !errors.Is(err, errNoSecret) {
				problem = "The keyring could not be read: " + err.Error()
			}
			return a.askLogin(req, problem)
		}
		req.password = password
		return req, nil
	}
}

// afterFailure shows why a connection failed and returns the request to try next.
func (a *app) afterFailure(req *request, failure error) (*request, error) {
	address := req.target.Address()
	kind, message := describeError(failure, address)
	a.logger.Warn("connect failed", "address", address, "kind", int(kind), "error", failure)
	if kind == problemCancelled {
		return nil, nil
	}
	if kind == problemPassword {
		return a.askLogin(req, message)
	}
	key, err := a.showMessage("Could not connect", message+"\n\nEnter: try again   P: pick another Mac   Esc: quit", sdl.K_P)
	if err != nil {
		return nil, err
	}
	switch key {
	case sdl.K_ESCAPE:
		return nil, errQuit
	case sdl.K_P:
		return nil, nil
	}
	return req, nil
}

// remember stores the connection after it worked, when asked to.
func (a *app) remember(req *request) {
	if !req.remember {
		return
	}
	connection := savedConnection{
		User:     req.target.User,
		Host:     req.target.Host,
		Port:     req.target.Port,
		VNC:      req.opts.VNC,
		Mode:     req.opts.Mode.String(),
		Quality:  req.opts.Quality.String(),
		Keyboard: req.opts.Keyboard,
	}
	err := a.store.Save(connection, req.password)
	if err != nil {
		a.logger.Warn("save connection", "error", err)
	}
}

func securityTypes(req *request) []rfb.SecurityType {
	if req.opts.VNC || req.target.User == "" {
		return []rfb.SecurityType{rfb.SecurityVNCPassword, rfb.SecurityNone}
	}
	return nil
}
