// Command mockapple is a view-only Screen Sharing server for checking viewers
// without a Mac. It speaks Apple's dialect through the core server, signs in
// one account, advertises the observe-only server flag and logs every input
// event it receives, so a viewer that wrongly sends input shows up in the log.
// It has no picture of its own: the video request is refused, unless -video is
// given, which plays a canned HEVC clip (the built-in one, or the Annex B file
// named by -clip) as the Mac's media stream, so a viewer's video path can be
// tried without a Mac.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"

	"github.com/nekohasekai/open-screen-sharing/cmd/sharelinux/internal/mockmedia"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	E "github.com/sagernet/sing/common/exceptions"
)

// observeOnlyFlags is what a Mac that only allows viewing advertises.
const observeOnlyFlags = rfb.ServerFlagObserveOnly | rfb.ServerFlagExtended

var errNoPicture = E.New("mockapple has no picture")

type handler struct {
	screen rfb.ServerScreen
	// clip is what the media stream plays; nil refuses the stream.
	clip      *mockmedia.Clip
	frameRate int
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

func (h *handler) OpenVideo(ctx context.Context, request media.ServerVideoRequest) (media.ServerVideoSource, error) {
	if h.clip == nil {
		return nil, errNoPicture
	}
	log.Println("video requested", request.Mode, request.PixelFormats)
	return h.clip.OpenVideo(h.frameRate)(ctx, request)
}

func (h *handler) OpenAudio() (media.ServerAudioSource, error) { return nil, errNoPicture }

// listenIP is the address of the TCP listener, which the media sockets share.
func listenIP(listen string) net.IP {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return nil
	}
	return net.ParseIP(host)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:5991", "address to listen on")
	user := flag.String("user", "mock", "account name")
	password := flag.String("password", os.Getenv("MOCKAPPLE_PASSWORD"), "account password (or MOCKAPPLE_PASSWORD)")
	size := flag.String("size", "", "screen size WxH (default 800x500, or the clip size with -video)")
	video := flag.Bool("video", false, "play a canned HEVC clip as the media stream")
	clipPath := flag.String("clip", "", "Annex B HEVC file for -video (default: the built-in clip)")
	frameRate := flag.Int("fps", 30, "frame rate of the clip")
	flag.Parse()
	var clip *mockmedia.Clip
	if *video {
		var err error
		if *clipPath == "" {
			clip, err = mockmedia.Builtin()
		} else {
			var data []byte
			data, err = os.ReadFile(*clipPath)
			if err == nil {
				clip, err = mockmedia.ParseAnnexB(data)
			}
		}
		if err != nil {
			log.Fatal(err)
		}
	}
	if *size == "" {
		*size = "800x500"
		if clip != nil {
			*size = fmt.Sprintf("%dx%d", clip.Width, clip.Height)
		}
	}
	var width, height int
	if _, err := fmt.Sscanf(*size, "%dx%d", &width, &height); err != nil || width < 16 || height < 16 {
		log.Fatal("bad -size ", *size)
	}
	server, err := rfb.NewServer(rfb.ServerOptions{
		Name:     "mockapple",
		Username: *user,
		Password: *password,
		Logger:   slog.Default(),
		Handler:  &handler{screen: rfb.ServerScreen{Width: width, Height: height, Scale: 1}, clip: clip, frameRate: *frameRate},
		Flags:    observeOnlyFlags,
		// A viewer on the same machine is a fair test of the video path.
		Media: media.ServerStreamConfig{
			AllowLoopbackTarget: true,
			OpenAudio:           noSound,
			ListenUDP: func(port uint16) (*net.UDPConn, error) {
				return net.ListenUDP("udp", &net.UDPAddr{IP: listenIP(*listen), Port: int(port)})
			},
		},
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

// noSound is a Mac that sends no sound: the server then answers without an audio stream.
func noSound() (media.ServerAudioSource, error) { return nil, media.ErrAudioUnsupported }
