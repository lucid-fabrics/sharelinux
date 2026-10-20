package main

import (
	"context"
	"strings"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// liveSession is a connected client and the goroutine running it.
type liveSession struct {
	req     *request
	client  *rfb.Client
	handler *handler
	echo    *latency
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	runErr  error
}

// sessionPrompt asks the person which session to share, while Dial waits.
type sessionPrompt struct {
	request rfb.SessionRequest
	answer  chan rfb.SessionChoice
}

type dialResult struct {
	client *rfb.Client
	err    error
}

func (a *app) rfbOptions(req *request, h *handler) rfb.Options {
	options := rfb.Options{
		Address:              req.target.Address(),
		Username:             req.target.User,
		Password:             req.password,
		ClientName:           req.opts.ClientName,
		AllowUnverifiedLogin: req.opts.AllowUnverified,
		Quality:              req.opts.Quality,
		Mode:                 req.opts.Mode,
		Clipboard:            !req.opts.NoClipboard,
		DisplayID:            req.opts.Display,
		AutomaticUpdates:     true,
		Logger:               a.logger,
		SecurityTypes:        securityTypes(req),
	}
	if req.opts.Virtual != nil || req.opts.HDR {
		virtual, hdr := req.opts.Virtual, req.opts.HDR
		options.Prepare = func(client *rfb.Client) {
			if virtual != nil {
				err := client.SetVirtualDisplay(virtual.Width, virtual.Height, virtual.Density, rfb.VirtualDisplayLargestSide, rfb.VirtualDisplayLargestSide)
				if err != nil {
					h.toast("Virtual display: " + err.Error())
				}
			}
			if hdr {
				err := client.SetHDR(true)
				if err != nil {
					h.toast("HDR: " + err.Error())
				}
			}
		}
	}
	return options
}

// dial makes one attempt with the login the request asks for. It never falls
// back to another login: an account password is not sent as a VNC password,
// because that challenge can be cracked offline. A VNC password is used only
// when the request says so (-vnc, or no user name).
func (a *app) dial(ctx context.Context, req *request, h *handler) (*rfb.Client, error) {
	return rfb.Dial(ctx, a.rfbOptions(req, h), h)
}

// connect dials the Mac while showing progress. Escape or closing the window cancels.
func (a *app) connect(req *request) (*liveSession, error) {
	ctx, cancel := context.WithCancel(context.Background())
	echo := &latency{}
	h := newHandler(a.logger, echo)
	prompts := make(chan *sessionPrompt)
	h.choose = func(request rfb.SessionRequest) (rfb.SessionChoice, error) {
		switch req.opts.Session {
		case "console":
			return rfb.SessionShareConsole, nil
		case "own":
			return rfb.SessionOwn, nil
		case "cancel":
			return rfb.SessionCancel, nil
		}
		prompt := &sessionPrompt{request: request, answer: make(chan rfb.SessionChoice, 1)}
		select {
		case prompts <- prompt:
		case <-ctx.Done():
			return rfb.SessionCancel, ctx.Err()
		}
		select {
		case choice := <-prompt.answer:
			return choice, nil
		case <-ctx.Done():
			return rfb.SessionCancel, ctx.Err()
		}
	}
	results := make(chan dialResult, 1)
	go func() {
		client, err := a.dial(ctx, req, h)
		results <- dialResult{client, err}
	}()
	status := "Connecting"
	var prompt *sessionPrompt
	for {
		a.drawConnecting(req, status, prompt)
		select {
		case result := <-results:
			if result.err != nil {
				cancel()
				return nil, result.err
			}
			live := &liveSession{req: req, client: result.client, handler: h, echo: echo, ctx: ctx, cancel: cancel, done: make(chan struct{})}
			go func() {
				live.runErr = live.client.Run()
				close(live.done)
			}()
			return live, nil
		case state := <-h.states:
			status = describeState(state)
		case prompt = <-prompts:
		default:
		}
		event := nextEvent(50)
		if event == nil {
			continue
		}
		if isQuit(event) {
			cancel()
			drainDial(results, (*rfb.Client).Close)
			return nil, errQuit
		}
		if event.Type != sdl.EVENT_KEY_DOWN {
			continue
		}
		key := event.KeyboardEvent().Key
		switch {
		case key == sdl.K_ESCAPE:
			cancel()
			drainDial(results, (*rfb.Client).Close)
			return nil, E.Extend(context.Canceled, "cancelled")
		case prompt != nil && key == sdl.K_C:
			prompt.answer <- rfb.SessionShareConsole
			prompt = nil
		case prompt != nil && key == sdl.K_O:
			prompt.answer <- rfb.SessionOwn
			prompt = nil
		}
	}
}

// drainDial waits for the dial that was cancelled and closes a client that
// finished anyway, so its socket and goroutines do not outlive the attempt.
func drainDial(results <-chan dialResult, closeClient func(*rfb.Client) error) {
	result := <-results
	if result.client != nil {
		_ = closeClient(result.client)
	}
}

func describeState(state rfb.State) string {
	switch state {
	case rfb.StateConnecting:
		return "Connecting"
	case rfb.StateAuthenticating:
		return "Logging in"
	case rfb.StateWaitingForApproval:
		return "Waiting for the person at the Mac to accept"
	case rfb.StateChoosingSession:
		return "Choosing a session"
	case rfb.StateConnected:
		return "Starting the screen"
	}
	return "Connecting"
}

func (a *app) drawConnecting(req *request, status string, prompt *sessionPrompt) {
	c := a.canvas
	width, _ := c.begin(uiScale)
	columns := glyphs(width) - 4
	c.text(16, 16, colorAccent, ellipsize(status+" to "+req.target.String(), columns))
	if prompt == nil {
		c.text(16, 48, colorMuted, "Esc cancels.")
	} else {
		user := prompt.request.ConsoleUser
		c.text(16, 48, colorText, ellipsize("The Mac has "+quoteName(user)+" logged in.", columns))
		c.text(16, 68, colorText, "C: share their screen   O: your own session   Esc: cancel")
	}
	_ = c.renderer.Present()
}

func quoteName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "a user"
	}
	return name
}
