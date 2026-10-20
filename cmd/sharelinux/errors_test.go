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
		{fmt.Errorf("wrap: %w", rfb.ErrAuthenticationFailed), problemPassword, "password"},
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
		kind, text := describeError(c.err, "mac:5900")
		if kind != c.kind || !strings.Contains(text, c.text) {
			t.Errorf("%v: got %d %q", c.err, kind, text)
		}
	}
	if kind, _ := describeError(nil, "x"); kind != problemOther {
		t.Fatal("nil is no problem")
	}
}

func TestDescribeErrorWrongVNCPassword(t *testing.T) {
	kind, text := describeError(rfb.ErrAuthenticationFailed, "mac:5900")
	if kind != problemPassword || !strings.Contains(text, "VNC password") {
		t.Fatalf("%d %q", kind, text)
	}
	_, text = describeError(rfb.ErrUnsupportedServer, "mac:5900")
	if !strings.Contains(text, "-vnc") {
		t.Fatalf("unsupported servers should point at -vnc: %q", text)
	}
}
