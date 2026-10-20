package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestDescribeError(t *testing.T) {
	refused := &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	cases := []struct {
		err  error
		kind problemKind
		text string
	}{
		{fmt.Errorf("wrap: %w", rfb.ErrAuthenticationFailed), problemPassword, "account name or password"},
		{rfb.ErrPermissionDenied, problemDenied, "declined"},
		{rfb.ErrUnverifiedServer, problemUnsupported, "-allow-unverified"},
		{rfb.ErrUnsupportedServer, problemUnsupported, "does not offer"},
		{refused, problemRefused, "Screen Sharing"},
		{&net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}, problemUnreachable, "nope.invalid"},
		{context.DeadlineExceeded, problemUnreachable, "No answer"},
		{context.Canceled, problemCancelled, "cancelled"},
		{io.EOF, problemEnded, "closed"},
		{fmt.Errorf("odd"), problemOther, "odd"},
	}
	for _, c := range cases {
		kind, text := describeError(c.err, "mac:5900", false)
		if kind != c.kind || !strings.Contains(text, c.text) {
			t.Errorf("%v: got %d %q", c.err, kind, text)
		}
	}
	if kind, _ := describeError(nil, "x", false); kind != problemOther {
		t.Fatal("nil is no problem")
	}
}

// A refused login is named for the kind that was tried, with no wording that
// covers both.
func TestDescribeErrorWrongPasswordMatchesLoginKind(t *testing.T) {
	failure := fmt.Errorf("wrap: %w", rfb.ErrAuthenticationFailed)
	kind, text := describeError(failure, "mac:5900", true)
	if kind != problemPassword || text != "That VNC password was not accepted." {
		t.Fatalf("%d %q", kind, text)
	}
	kind, text = describeError(failure, "mac:5900", false)
	if kind != problemPassword || text != "The account name or password was not accepted." {
		t.Fatalf("%d %q", kind, text)
	}
}

func TestDescribeErrorUnsupportedPointsAtVNC(t *testing.T) {
	_, text := describeError(rfb.ErrUnsupportedServer, "mac:5900", false)
	if !strings.Contains(text, "-vnc") {
		t.Fatalf("unsupported servers should point at -vnc: %q", text)
	}
}
