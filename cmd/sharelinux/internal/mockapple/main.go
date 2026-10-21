// Command mockapple is a view-only Screen Sharing server for checking viewers
// without a Mac. It speaks Apple's dialect through the core server, signs in
// one account, advertises the observe-only server flag and logs every input
// event it receives, so a viewer that wrongly sends input shows up in the log.
// It has no picture: the video request is refused.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	E "github.com/sagernet/sing/common/exceptions"
)

// observeOnlyFlags is what a Mac that only allows viewing advertises.
const observeOnlyFlags = rfb.ServerFlagObserveOnly | rfb.ServerFlagExtended

var errNoPicture = E.New("mockapple has no picture")

type handler struct {
	screen rfb.ServerScreen
}

func (h *handler) Screen() rfb.ServerScreen { return h.screen }

func (h *handler) SessionStarted(*rfb.ServerSession) { log.Println("viewer connected") }

func (h *handler) SessionEnded(*rfb.ServerSession, error) { log.Println("viewer left") }

func (h *handler) HandleEvent(_ *rfb.ServerSession, event rfb.ViewerEvent) {
	// Every line starts with "input" so a check can count what a viewer sent.
	switch event := event.(type) {
	case rfb.ViewerMode:
		log.Println("mode", event.Mode)
	case rfb.ViewerClipboardSharing:
		log.Println("clipboard sharing", event.Enabled)
	default:
		log.Printf("input %T %+v", event, event)
	}
}

func (h *handler) Clipboard(context.Context) (*rfb.Clipboard, error) { return nil, errNoPicture }

func (h *handler) OpenVideo(context.Context, media.ServerVideoRequest) (media.ServerVideoSource, error) {
	return nil, errNoPicture
}

func (h *handler) OpenAudio() (media.ServerAudioSource, error) { return nil, errNoPicture }

func main() {
	listen := flag.String("listen", "127.0.0.1:5991", "address to listen on")
	user := flag.String("user", "mock", "account name")
	password := flag.String("password", os.Getenv("MOCKAPPLE_PASSWORD"), "account password (or MOCKAPPLE_PASSWORD)")
	size := flag.String("size", "800x500", "screen size WxH")
	flag.Parse()
	var width, height int
	if _, err := fmt.Sscanf(*size, "%dx%d", &width, &height); err != nil || width < 16 || height < 16 {
		log.Fatal("bad -size ", *size)
	}
	server, err := rfb.NewServer(rfb.ServerOptions{
		Name:     "mockapple",
		Username: *user,
		Password: *password,
		Logger:   slog.Default(),
		Handler:  &handler{screen: rfb.ServerScreen{Width: width, Height: height, Scale: 1}},
		Flags:    observeOnlyFlags,
	})
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("listening on", listener.Addr())
	log.Fatal(server.Serve(listener))
}
