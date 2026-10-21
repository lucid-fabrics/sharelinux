package main

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// quietHandler takes everything the client reports and ignores it.
type quietHandler struct{}

func (quietHandler) StateChanged(rfb.State)                          {}
func (quietHandler) ResizeFramebuffer(int, int) (*bgra.Image, error) { return nil, nil }
func (quietHandler) FramebufferUpdated(rfb.Update)                   {}
func (quietHandler) CursorChanged(*rfb.Cursor)                       {}
func (quietHandler) CursorMoved(int, int)                            {}
func (quietHandler) DisplaysChanged(*rfb.DisplayLayout)              {}
func (quietHandler) ClipboardReceived(*rfb.Clipboard)                {}
func (quietHandler) ClipboardFailed(rfb.ClipboardProblem, error)     {}
func (quietHandler) Bell()                                           {}
func (quietHandler) KeyboardSourceChanged(rfb.KeyboardSource)        {}
func (quietHandler) StatusReceived(rfb.Status)                       {}
func (quietHandler) DeviceInfoReceived(rfb.DeviceInfo)               {}
func (quietHandler) RemoteDragChanged(*rfb.RemoteDrag)               {}
func (quietHandler) DropDestinationReceived(uint32, string)          {}
func (quietHandler) SystemCommandsChanged([]rfb.SystemCommand)       {}
func (quietHandler) ChooseSession(rfb.SessionRequest) (rfb.SessionChoice, error) {
	return 0, nil
}

// TestServerAdvertisesObserveOnly is the regression for the view-only check: a
// real client that signs in sees the observe-only flag, which is what the
// viewer reads to disable Control and Exclusive.
func TestServerAdvertisesObserveOnly(t *testing.T) {
	server, err := rfb.NewServer(rfb.ServerOptions{
		Name:     "mockapple",
		Username: "mock",
		Password: "secret",
		Logger:   slog.New(slog.DiscardHandler),
		Handler:  &handler{screen: rfb.ServerScreen{Width: 64, Height: 64, Scale: 1}},
		Flags:    observeOnlyFlags,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := rfb.Dial(ctx, rfb.Options{
		Address:  listener.Addr().String(),
		Username: "mock",
		Password: "secret",
		Mode:     rfb.ModeControl,
		Logger:   slog.New(slog.DiscardHandler),
	}, quietHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info := client.Info()
	if info.Flags&rfb.ServerFlagObserveOnly == 0 {
		t.Fatalf("flags %#x lack the observe-only flag", info.Flags)
	}
	if info.Dialect != rfb.DialectApple {
		t.Fatalf("dialect %v, want Apple", info.Dialect)
	}
}
