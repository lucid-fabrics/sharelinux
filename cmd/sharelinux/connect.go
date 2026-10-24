package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// liveSession is a connected client and the goroutine running it.
type liveSession struct {
	req     *request
	client  *rfb.Client
	handler *handler
	echo    *latency
	// peer is the IP address the Screen Sharing connection was made to. The mouse helper's
	// datagrams go to this address and no other.
	peer   string
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	runErr error
}

// sessionPrompt asks the person which session to share, while Dial waits.
type sessionPrompt struct {
	request rfb.SessionRequest
	answer  chan rfb.SessionChoice
}

type dialResult struct {
	client *rfb.Client
	err    error
	peer   string
}

// decoderPixelRate is what the decoder of a session can keep up with. It decides the frame rate
// the Mac picks for a virtual display, and the one it offers the stream. A session with HDR is
// decoded by the GPU alone, so it gets the GPU's rate and never the CPU's.
func (a *app) decoderPixelRate(hdr bool) int64 {
	plan := planVideo(a.videoMode(), a.videoCapabilities(), true)
	if hdr {
		plan = plan.withHDR(a.videoMode(), a.videoCapabilities())
	}
	if a.chroma444Wanted() {
		plan = plan.with444(a.videoMode(), a.videoCapabilities())
	}
	return plan.decoderPixelRate(videoThreads())
}

func (a *app) rfbOptions(req *request, h *handler) rfb.Options {
	options := rfb.Options{
		Address:              req.target.Address(),
		Username:             req.target.User,
		Password:             req.dialPassword(),
		ClientName:           req.opts.ClientName,
		AllowUnverifiedLogin: req.opts.AllowUnverified,
		Quality:              req.opts.Quality,
		Mode:                 req.opts.Mode,
		Clipboard:            !req.opts.NoClipboard,
		DisplayID:            req.opts.Display,
		AutomaticUpdates:     true,
		Logger:               a.logger,
		SecurityTypes:        securityTypes(req),
		// What the decoder can keep up with decides the frame rate the Mac picks for a
		// virtual display, and the one it offers the stream.
	}
	// HDR means real HDR video: where this machine cannot show it, the virtual display stays
	// standard range too, so the Mac never sends a stream that would be shown wrong.
	hdr := a.hdrEffective(req.opts.HDR)
	options.DecoderMaxPixelRate = a.decoderPixelRate(hdr)
	if req.opts.HDR && !hdr {
		a.logger.Info("hdr", "state", "off", "reason", a.hdrOffReason())
	}
	if req.opts.Virtual != nil || hdr {
		virtual := req.opts.Virtual
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
	client, _, err := a.dialPeer(ctx, req, h)
	return client, err
}

// dialPeer is dial that also says which IP address the connection went to. The host name is
// looked up once here and the connection is made to that address, so the answer is the one
// the mouse helper can be sent to as well: a name looked up a second time could answer with
// another machine.
func (a *app) dialPeer(ctx context.Context, req *request, h *handler) (*rfb.Client, string, error) {
	options := a.rfbOptions(req, h)
	candidates := dialCandidates(ctx, req.target.Host)
	if len(candidates) == 0 { // not resolvable: the dialer reports why
		client, err := rfb.Dial(ctx, options, h)
		return client, "", err
	}
	var lastErr error
	for _, ip := range candidates {
		options.Address = net.JoinHostPort(ip, strconv.Itoa(req.target.Port))
		client, err := rfb.Dial(ctx, options, h)
		if err == nil {
			return client, ip, nil
		}
		lastErr = err
		// Only a connection that could not be made tries the next address; a refusal to log in
		// or an unsupported server would be the same on any of them.
		var opError *net.OpError
		if ctx.Err() != nil || !errors.As(err, &opError) || opError.Op != "dial" {
			break
		}
	}
	return nil, "", lastErr
}

// dialCandidates returns the addresses to try for a host: an IP address is its own, a name is
// looked up once (at most four answers).
func dialCandidates(ctx context.Context, host string) []string {
	host = strings.TrimSpace(host)
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []string{strings.Trim(host, "[]")}
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(lookup, "ip", host)
	if err != nil {
		return nil
	}
	var out []string
	for _, address := range addresses {
		out = append(out, address.String())
		if len(out) == 4 {
			break
		}
	}
	return out
}

// dialer is one connection attempt running in the background. The window shows
// its progress through dialEvents and can abort it.
type dialer struct {
	ctx     context.Context
	cancel  context.CancelFunc
	echo    *latency
	handler *handler
	prompts chan *sessionPrompt
	results chan dialResult
	req     *request
}

// dialEvents are what the window hears about an attempt. Each runs on the
// dialer's goroutine, so the window must hand them to its own thread.
type dialEvents struct {
	State  func(status string)
	Prompt func(*sessionPrompt)
	Done   func(*liveSession, error)
}

// beginDial starts dialing in the background.
func (a *app) beginDial(req *request) *dialer {
	ctx, cancel := context.WithCancel(context.Background())
	echo := &latency{}
	h := newHandler(a.logger, echo)
	d := &dialer{
		ctx: ctx, cancel: cancel, echo: echo, handler: h, req: req,
		prompts: make(chan *sessionPrompt),
		results: make(chan dialResult, 1),
	}
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
		case d.prompts <- prompt:
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
	go func() {
		client, peer, err := a.dialPeer(ctx, req, h)
		d.results <- dialResult{client, err, peer}
	}()
	return d
}

// abort cancels the attempt. Done still fires, with a cancellation error.
func (d *dialer) abort() {
	d.cancel()
}

// run reports the attempt until it ends. It blocks, so start it in a goroutine.
func (d *dialer) run(events dialEvents) {
	for {
		select {
		case result := <-d.results:
			if result.err != nil {
				d.cancel()
				events.Done(nil, result.err)
				return
			}
			if d.ctx.Err() != nil {
				_ = result.client.Close()
				events.Done(nil, E.Extend(context.Canceled, "cancelled"))
				return
			}
			live := &liveSession{req: d.req, client: result.client, handler: d.handler, echo: d.echo, peer: result.peer, ctx: d.ctx, cancel: d.cancel, done: make(chan struct{})}
			go func() {
				live.runErr = live.client.Run()
				close(live.done)
			}()
			events.Done(live, nil)
			return
		case state := <-d.handler.states:
			events.State(describeState(state))
		case prompt := <-d.prompts:
			events.Prompt(prompt)
		case <-d.ctx.Done():
			drainDial(d.results, (*rfb.Client).Close)
			events.Done(nil, E.Extend(context.Canceled, "cancelled"))
			return
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

func quoteName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "a user"
	}
	return name
}
