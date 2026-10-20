package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestDrainDialClosesLateClient(t *testing.T) {
	results := make(chan dialResult, 1)
	client := &rfb.Client{}
	results <- dialResult{client: client}
	var closed *rfb.Client
	drainDial(results, func(c *rfb.Client) error {
		closed = c
		return nil
	})
	if closed != client {
		t.Fatal("a client that finished after cancel must be closed")
	}
}

func TestDrainDialFailedDialClosesNothing(t *testing.T) {
	results := make(chan dialResult, 1)
	results <- dialResult{err: context.Canceled}
	drainDial(results, func(*rfb.Client) error {
		t.Fatal("no client to close")
		return nil
	})
}

// A server with no Mac account login must fail once. The client must not
// connect again and offer the account password as a VNC password.
func TestDialNeverFallsBackToVNCPassword(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer conn.Close()
				_, _ = conn.Write([]byte("RFB 003.008\n"))
				buffer := make([]byte, 12)
				_, _ = io.ReadFull(conn, buffer)
				// Offer only VNC authentication (type 2).
				_, _ = conn.Write([]byte{1, 2})
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &app{opts: &options{}, logger: logger}
	req := &request{target: target{User: "me", Host: "127.0.0.1"}, password: "account-secret", opts: options{}}
	address := listener.Addr().(*net.TCPAddr)
	req.target.Host = "127.0.0.1"
	req.target.Port = address.Port
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := newHandler(logger, &latency{})
	_, err = a.dial(ctx, req, h)
	if err == nil {
		t.Fatal("expected a failure")
	}
	time.Sleep(200 * time.Millisecond)
	if got := accepted.Load(); got != 1 {
		t.Fatalf("connected %d times, want 1", got)
	}
	kind, _ := describeError(err, "x", false)
	if kind != problemUnsupported {
		t.Fatalf("kind %d: %v", kind, err)
	}
}
