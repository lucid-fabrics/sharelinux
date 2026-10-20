package main

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"syscall"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// problemKind says what the login form should do about a failure.
type problemKind int

const (
	problemOther problemKind = iota
	problemPassword
	problemUnreachable
	problemRefused
	problemDenied
	problemUnsupported
	problemCancelled
	problemEnded
)

// describeError turns a connection error into a message for the person at the
// keyboard and a kind the caller can act on. The raw error stays in the log.
func describeError(err error, address string) (problemKind, string) {
	var dns *net.DNSError
	var netError net.Error
	switch {
	case err == nil:
		return problemOther, ""
	case E.IsMulti(err, context.Canceled, rfb.ErrSessionCancelled):
		return problemCancelled, "Connection cancelled."
	case errors.Is(err, rfb.ErrAuthenticationFailed):
		return problemPassword, "The user name or password was not accepted. Check the Mac account's name and password, or the VNC password when no user is given."
	case errors.Is(err, rfb.ErrPermissionDenied):
		return problemDenied, "The person at the Mac declined the request to share the screen."
	case errors.Is(err, rfb.ErrUnverifiedServer):
		return problemUnsupported, "This server would learn the password in clear text. Start with -allow-unverified to log in anyway."
	case errors.Is(err, rfb.ErrUnsupportedServer):
		return problemUnsupported, "This server does not offer a login this client supports: " + err.Error() + ". If it only has a VNC password, leave the user empty or start with -vnc."
	case errors.Is(err, syscall.ECONNREFUSED):
		return problemRefused, address + " refused the connection. Turn on Screen Sharing in System Settings > General > Sharing on the Mac."
	case errors.As(err, &dns) && dns.IsNotFound:
		return problemUnreachable, "Could not find " + dns.Name + ". Check the spelling of the host name."
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netError) && netError.Timeout():
		return problemUnreachable, "No answer from " + address + ". Check that the Mac is awake and on the same network."
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return problemUnreachable, "No route to " + address + ". Check the network and the address."
	case E.IsMulti(err, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, syscall.ECONNRESET, syscall.EPIPE):
		return problemEnded, "The connection to " + address + " was closed."
	}
	return problemOther, "Could not connect to " + address + ": " + err.Error()
}

func pluralize(count int, word string) string {
	if count == 1 {
		return "1 " + word
	}
	return strconv.Itoa(count) + " " + word + "s"
}
