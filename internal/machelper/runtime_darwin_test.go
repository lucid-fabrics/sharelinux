package machelper

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// These tests drive the Mac app's runtime (machelper/Sources/HelperRuntime)
// over a real UDP socket on loopback, through the runtimeharness executable.
// The harness runs the same runtime, channel, gate and mouse controller as the
// app but posts into a fake poster that prints "POST ..." lines, and takes its
// environment (lock, console, Screen Sharing, grant) from stdin commands. It
// posts nothing to the window server and asks for no permission, so these
// tests never touch the Mac's real input. MACHELPER_RUNTIMEHARNESS can point at
// a prebuilt binary.

var (
	harnessOnce  sync.Once
	harnessPath  string
	harnessError error
)

func harnessBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MACHELPER_RUNTIMEHARNESS"); p != "" {
		return p
	}
	if _, err := exec.LookPath("swift"); err != nil {
		t.Skip("swift toolchain not found")
	}
	harnessOnce.Do(func() {
		pkg, err := filepath.Abs("../../machelper")
		if err != nil {
			harnessError = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "swift", "build", "--package-path", pkg, "--product", "runtimeharness").CombinedOutput(); err != nil {
			harnessError = &buildError{string(out), err}
			return
		}
		out, err := exec.CommandContext(ctx, "swift", "build", "--package-path", pkg, "--show-bin-path").Output()
		if err != nil {
			harnessError = err
			return
		}
		harnessPath = filepath.Join(strings.TrimSpace(string(out)), "runtimeharness")
	})
	if harnessError != nil {
		t.Fatalf("building runtimeharness: %v", harnessError)
	}
	return harnessPath
}

type harness struct {
	t     *testing.T
	cmd   *exec.Cmd
	in    *bufio.Writer
	close func() error
	pub   []byte
	conn  *net.UDPConn

	mu      sync.Mutex
	posts   []string
	states  []string
	okCh    chan struct{}
	replies chan []byte
}

func startHarness(t *testing.T, paired ...[]byte) *harness {
	t.Helper()
	args := []string{}
	for _, k := range paired {
		args = append(args, "--paired", hex.EncodeToString(k))
	}
	cmd := exec.Command(harnessBinary(t), args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, cmd: cmd, in: bufio.NewWriter(stdin), close: stdin.Close, okCh: make(chan struct{}, 64), replies: make(chan []byte, 64)}
	t.Cleanup(func() {
		if h.conn != nil {
			_ = h.conn.Close()
		}
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	portCh := make(chan int, 1)
	pubCh := make(chan []byte, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "POST "):
				h.mu.Lock()
				h.posts = append(h.posts, strings.TrimPrefix(line, "POST "))
				h.mu.Unlock()
			case strings.HasPrefix(line, "STATE "):
				h.mu.Lock()
				h.states = append(h.states, strings.TrimPrefix(line, "STATE "))
				h.mu.Unlock()
			case line == "OK":
				h.okCh <- struct{}{}
			case strings.HasPrefix(line, "PUB "):
				b, _ := hex.DecodeString(strings.TrimPrefix(line, "PUB "))
				pubCh <- b
			case strings.HasPrefix(line, "LISTEN "):
				n, _ := strconv.Atoi(strings.TrimPrefix(line, "LISTEN "))
				portCh <- n
			}
		}
	}()
	select {
	case h.pub = <-pubCh:
	case <-time.After(20 * time.Second):
		t.Fatal("harness did not start")
	}
	port := <-portCh
	h.conn, err = net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := h.conn.Read(buf)
			if err != nil {
				return
			}
			h.replies <- append([]byte(nil), buf[:n]...)
		}
	}()
	return h
}

// do runs a harness command on its runtime queue and waits for the OK.
func (h *harness) do(format string, a ...any) {
	h.t.Helper()
	h.in.WriteString(fmt.Sprintf(format, a...) + "\n")
	h.in.Flush()
	select {
	case <-h.okCh:
	case <-time.After(5 * time.Second):
		h.t.Fatalf("harness did not answer %q", fmt.Sprintf(format, a...))
	}
}

// connect runs the IK handshake over UDP and sends the first keepalive, which
// makes the session live.
func (h *harness) connect(static noise.DHKey) *Session {
	h.t.Helper()
	ini, m1, err := NewIKInitiation(static, h.pub, time.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.conn.Write(m1); err != nil {
		h.t.Fatal(err)
	}
	var reply []byte
	select {
	case reply = <-h.replies:
	case <-time.After(3 * time.Second):
		h.t.Fatal("handshake not answered")
	}
	if _, err := ini.Read(reply); err != nil {
		h.t.Fatalf("reading the response: %v", err)
	}
	s, err := ini.Session(time.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	h.send(s, InputPacket{Type: PacketKeepalive})
	return s
}

func (h *harness) send(s *Session, p InputPacket) {
	h.t.Helper()
	dg, err := s.Seal(p.Encode(), time.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.conn.Write(dg); err != nil {
		h.t.Fatal(err)
	}
}

// settle waits until everything sent so far has been handled by the runtime.
func (h *harness) settle() {
	h.t.Helper()
	time.Sleep(150 * time.Millisecond)
	h.do("!sync")
}

func (h *harness) takePosts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.posts
	h.posts = nil
	return p
}

// waitPost waits for a post line and returns everything seen up to it.
func (h *harness) waitPost(want string, within time.Duration) []string {
	h.t.Helper()
	deadline := time.Now().Add(within)
	var seen []string
	for time.Now().Before(deadline) {
		seen = append(seen, h.takePosts()...)
		for _, p := range seen {
			if p == want {
				return seen
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("no %q within %v; saw %q", want, within, seen)
	return nil
}

func (h *harness) status(s *Session, within time.Duration) (StatusPacket, bool) {
	h.t.Helper()
	select {
	case r := <-h.replies:
		pt, err := s.Open(r, StatusPacketSize, time.Now())
		if err != nil {
			h.t.Fatalf("opening status: %v", err)
		}
		st, err := DecodeStatusPacket(pt)
		if err != nil {
			h.t.Fatal(err)
		}
		return st, true
	case <-time.After(within):
		return StatusPacket{}, false
	}
}

func (h *harness) drainReplies() {
	for {
		select {
		case <-h.replies:
		default:
			return
		}
	}
}

// setup: harness with the Go key paired and the switch on, and a live session.
func liveSession(t *testing.T) (*harness, *Session, noise.DHKey) {
	t.Helper()
	static := mustKey(t)
	h := startHarness(t, static.Public)
	h.do("!toggle on")
	h.settle()
	s := h.connect(static)
	h.settle()
	h.drainReplies()
	h.takePosts()
	return h, s, static
}

func TestRuntimeSocketMotionButtonsAndScrollReachThePoster(t *testing.T) {
	h, s, _ := liveSession(t)
	// 2.5 points: two now, the half carries over to the next packet.
	h.send(s, InputPacket{Type: PacketInput, DX: 40, DY: -40, Buttons: ButtonLeft})
	h.send(s, InputPacket{Type: PacketInput, DX: 8, DY: -8, Buttons: ButtonLeft, SY: 3})
	h.send(s, InputPacket{Type: PacketInput})
	h.settle()
	want := []string{"move 2 -2 held=0", "down 0 click=1", "move 1 -1 held=1", "scroll 0 3", "up 0"}
	got := h.takePosts()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("posted %q\nwant   %q", got, want)
	}
}

// Every way the gate can say no, and every way the owner can pull the plug,
// stops the posting and puts a held button up, over the real socket.
func TestRuntimeSocketStopsOnEveryDeny(t *testing.T) {
	cases := []struct {
		name   string
		stop   func(h *harness)
		status HelperState // expected status for the next packet, 0xff: session gone
	}{
		{"screen locked", func(h *harness) { h.do("!locked on") }, StateLocked},
		{"lock notification", func(h *harness) { h.do("!lock") }, StateLocked},
		{"left the console", func(h *harness) { h.do("!console off") }, StateNotConsole},
		{"grant revoked", func(h *harness) { h.do("!grant off") }, StateNeedsGrant},
		{"screen sharing ended", func(h *harness) { h.do("!sharing off") }, 0xff},
		{"switch turned off", func(h *harness) { h.do("!toggle off") }, 0xff},
		{"kill switch", func(h *harness) { h.do("!kill") }, 0xff},
		{"sleep", func(h *harness) { h.do("!sleep") }, 0xff},
		{"key unpaired", func(h *harness) { h.do("!unpair") }, 0xff},
		{"environment poller stalled", func(h *harness) {
			h.do("!freeze on")
		}, StateLocked},
		{"client says bye", nil, 0xff},
		{"client goes silent", func(h *harness) { time.Sleep(1300 * time.Millisecond) }, 0xff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s, _ := liveSession(t)
			// A drag is under way.
			h.send(s, InputPacket{Type: PacketInput, Buttons: ButtonLeft, DX: 32})
			h.waitPost("down 0 click=1", 2*time.Second)
			h.takePosts()

			if tc.stop != nil {
				tc.stop(h)
			} else {
				h.send(s, InputPacket{Type: PacketBye})
			}
			h.waitPost("up 0", 2*time.Second)

			// Nothing more is posted, however much the client keeps sending.
			h.takePosts()
			h.drainReplies()
			for i := 0; i < 4; i++ { // keepalives like a real client; the Mac answers with a status at most every 250 ms
				h.send(s, InputPacket{Type: PacketKeepalive})
				time.Sleep(90 * time.Millisecond)
			}
			for i := 0; i < 5; i++ {
				h.send(s, InputPacket{Type: PacketInput, Buttons: ButtonLeft, DX: 160, DY: 160, SX: 4, SY: 4})
				time.Sleep(20 * time.Millisecond)
			}
			h.settle()
			if got := h.takePosts(); len(got) != 0 {
				t.Fatalf("posted after the stop: %q", got)
			}
			if tc.status != 0xff {
				st, ok := h.status(s, time.Second)
				if !ok {
					t.Fatalf("expected a %v status", tc.status)
				}
				if st.State != tc.status {
					t.Fatalf("status %v, want %v", st.State, tc.status)
				}
			}
		})
	}
}

// Screen Sharing ending ends the helper session within 500 ms, with nobody
// sending anything.
func TestRuntimeSocketSessionEndsWithScreenSharing(t *testing.T) {
	h, s, _ := liveSession(t)
	h.send(s, InputPacket{Type: PacketInput, Buttons: ButtonRight, DX: 16})
	h.waitPost("down 1 click=1", 2*time.Second)
	start := time.Now()
	h.do("!sharing off")
	h.waitPost("up 1", 500*time.Millisecond)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("took %v", d)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		last := ""
		if len(h.states) > 0 {
			last = h.states[len(h.states)-1]
		}
		h.mu.Unlock()
		if strings.Contains(last, "device=-") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the session did not end")
}

// The switch is off on every launch: a paired device connects, gets told
// "disabled", and nothing is posted until the owner turns it on.
func TestRuntimeSocketSwitchIsOffByDefault(t *testing.T) {
	static := mustKey(t)
	h := startHarness(t, static.Public)
	s := h.connect(static)
	h.send(s, InputPacket{Type: PacketInput, Buttons: ButtonLeft, DX: 160})
	st, ok := h.status(s, time.Second)
	if !ok || st.State != StateDisabled {
		t.Fatalf("status %v ok=%v, want disabled", st, ok)
	}
	h.settle()
	if got := h.takePosts(); len(got) != 0 {
		t.Fatalf("posted while the switch was off: %q", got)
	}
	h.do("!toggle on")
	time.Sleep(300 * time.Millisecond)
	h.send(s, InputPacket{Type: PacketInput, DX: 16})
	h.waitPost("move 1 0 held=0", 2*time.Second)
}

// Unpaired devices and unauthenticated junk get no answer and post nothing.
func TestRuntimeSocketIgnoresStrangersAndJunk(t *testing.T) {
	static := mustKey(t)
	h := startHarness(t, static.Public)
	h.do("!toggle on")
	stranger := mustKey(t)
	_, m1, err := NewIKInitiation(stranger, h.pub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, dg := range [][]byte{m1, {0x03, 1, 2, 3}, make([]byte, 600), {0xff}, {}} {
		if _, err := h.conn.Write(dg); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case r := <-h.replies:
		t.Fatalf("unexpected reply %x", r)
	case <-time.After(400 * time.Millisecond):
	}
	h.settle()
	if got := h.takePosts(); len(got) != 0 {
		t.Fatalf("posted: %q", got)
	}
}
