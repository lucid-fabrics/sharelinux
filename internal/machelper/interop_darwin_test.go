package machelper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// These tests run this package against the Swift HelperCore through the
// line-driven corepeer executable (machelper/Sources/corepeer). Set
// MACHELPER_COREPEER to use a prebuilt binary; otherwise it is built with
// `swift build`, which only touches machelper/.build.

var (
	buildOnce sync.Once
	peerPath  string
	buildErr  error
)

func corePeerBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MACHELPER_COREPEER"); p != "" {
		return p
	}
	if _, err := exec.LookPath("swift"); err != nil {
		t.Skip("swift toolchain not found")
	}
	buildOnce.Do(func() {
		pkg, err := filepath.Abs("../../machelper")
		if err != nil {
			buildErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "swift", "build", "--package-path", pkg, "--product", "corepeer").CombinedOutput(); err != nil {
			buildErr = &buildError{string(out), err}
			return
		}
		out, err := exec.CommandContext(ctx, "swift", "build", "--package-path", pkg, "--show-bin-path").Output()
		if err != nil {
			buildErr = err
			return
		}
		peerPath = filepath.Join(strings.TrimSpace(string(out)), "corepeer")
	})
	if buildErr != nil {
		t.Fatalf("building corepeer: %v", buildErr)
	}
	return peerPath
}

type buildError struct {
	output string
	err    error
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.output }

type peer struct {
	t     *testing.T
	cmd   *exec.Cmd
	in    *bufio.Writer
	out   *bufio.Scanner
	stdin interface{ Close() error }
	pub   []byte
}

func startPeer(t *testing.T, paired ...[]byte) *peer {
	t.Helper()
	args := []string{}
	for _, k := range paired {
		args = append(args, "--paired", hex.EncodeToString(k))
	}
	cmd := exec.Command(corePeerBinary(t), args...)
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
	p := &peer{t: t, cmd: cmd, in: bufio.NewWriter(stdin), out: bufio.NewScanner(stdout), stdin: stdin}
	t.Cleanup(func() {
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
	p.out.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := p.readLine()
	if !strings.HasPrefix(line, "PUB ") {
		t.Fatalf("peer banner %q", line)
	}
	p.pub, err = hex.DecodeString(strings.TrimPrefix(line, "PUB "))
	if err != nil || len(p.pub) != 32 {
		t.Fatalf("peer key %q", line)
	}
	return p
}

func (p *peer) readLine() string {
	p.t.Helper()
	done := make(chan bool, 1)
	go func() { done <- p.out.Scan() }()
	select {
	case ok := <-done:
		if !ok {
			p.t.Fatalf("peer closed: %v", p.out.Err())
		}
	case <-time.After(20 * time.Second):
		_ = p.cmd.Process.Kill()
		p.t.Fatal("peer timed out")
	}
	return p.out.Text()
}

func (p *peer) command(c string) string {
	p.t.Helper()
	p.in.WriteString(c + "\n")
	p.in.Flush()
	return p.readLine()
}

// send delivers one datagram and returns the peer's events text and reply.
func (p *peer) send(datagram []byte) (event string, reply []byte) {
	p.t.Helper()
	event = p.command(hex.EncodeToString(datagram))
	if !strings.HasPrefix(event, "EVENT ") {
		p.t.Fatalf("expected EVENT line, got %q", event)
	}
	line := p.readLine()
	if line == "NONE" {
		return strings.TrimPrefix(event, "EVENT "), nil
	}
	if !strings.HasPrefix(line, "REPLY ") {
		p.t.Fatalf("expected REPLY/NONE, got %q", line)
	}
	reply, err := hex.DecodeString(strings.TrimPrefix(line, "REPLY "))
	if err != nil {
		p.t.Fatal(err)
	}
	return strings.TrimPrefix(event, "EVENT "), reply
}

// connect runs the IK handshake from Go against the Swift responder.
func (p *peer) connect(static noise.DHKey) *Session {
	p.t.Helper()
	ini, m1, err := NewIKInitiation(static, p.pub, time.Now())
	if err != nil {
		p.t.Fatal(err)
	}
	event, reply := p.send(m1)
	if event != "answered" || reply == nil {
		p.t.Fatalf("handshake not answered: %q", event)
	}
	if _, err := ini.Read(reply); err != nil {
		p.t.Fatalf("reading Swift response: %v", err)
	}
	s, err := ini.Session(time.Now())
	if err != nil {
		p.t.Fatal(err)
	}
	return s
}

func (p *peer) sendPacket(s *Session, pk InputPacket) (string, []byte) {
	p.t.Helper()
	dg, err := s.Seal(pk.Encode(), time.Now())
	if err != nil {
		p.t.Fatal(err)
	}
	return p.send(dg)
}

func (p *peer) status(s *Session, reply []byte) StatusPacket {
	p.t.Helper()
	if reply == nil {
		p.t.Fatal("expected a status reply")
	}
	pt, err := s.Open(reply, StatusPacketSize, time.Now())
	if err != nil {
		p.t.Fatalf("opening Swift status: %v", err)
	}
	st, err := DecodeStatusPacket(pt)
	if err != nil {
		p.t.Fatal(err)
	}
	return st
}

func TestInteropSessionInputAndStatus(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	s := p.connect(static)

	want := InputPacket{Type: PacketInput, Buttons: ButtonLeft | ButtonBack, DX: -300, DY: 1234, SX: 7, SY: -9, Seq: 42, Flags: FlagPointerLocked}
	event, reply := p.sendPacket(s, want)
	wantEvent := "started " + hex.EncodeToString(static.Public) + ";input " + hex.EncodeToString(want.Encode())
	if event != wantEvent || reply != nil {
		t.Fatalf("event %q reply %v\nwant %q", event, reply, wantEvent)
	}

	p.command("!advance 0.3")
	event, reply = p.sendPacket(s, InputPacket{Type: PacketKeepalive, Seq: 43})
	if event != "keepalive" {
		t.Fatalf("event %q", event)
	}
	if st := p.status(s, reply); st.State != StateActive {
		t.Fatalf("status %v", st)
	}
}

func TestInteropClampAndStrictDecode(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	s := p.connect(static)
	event, _ := p.sendPacket(s, InputPacket{Type: PacketInput, DX: -32768, DY: 32767, SX: 9999, SY: -9999})
	clamped := InputPacket{Type: PacketInput, DX: -32767, DY: 32767, SX: 2000, SY: -2000}
	if !strings.HasSuffix(event, "input "+hex.EncodeToString(clamped.Encode())) {
		t.Fatalf("clamp: %q", event)
	}
	// Authenticated but malformed plaintext is dropped, not delivered.
	bad := InputPacket{Type: PacketInput}.Encode()
	bad[19] = 1
	dg, _ := s.Seal(bad, time.Now())
	if event, _ := p.send(dg); event != "dropped malformed" {
		t.Fatalf("malformed plaintext: %q", event)
	}
}

func TestInteropReplayAndTamper(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	_, m1, _ := NewIKInitiation(static, p.pub, time.Now())
	if event, _ := p.send(m1); event != "answered" {
		t.Fatalf("event %q", event)
	}
	p.command("!advance 1")
	if event, reply := p.send(m1); event != "dropped staleTimestamp" || reply != nil {
		t.Fatalf("replayed initiation: %q %v", event, reply)
	}

	s := p.connect2(t, static)
	dg, _ := s.Seal(InputPacket{Type: PacketInput, DX: 1}.Encode(), time.Now())
	for i := range dg {
		bad := bytes.Clone(dg)
		bad[i] ^= 0x80
		if event, reply := p.send(bad); strings.Contains(event, "input") || reply != nil {
			t.Fatalf("tampered byte %d: %q", i, event)
		}
	}
	if event, _ := p.send(dg); !strings.HasSuffix(event, "input "+hex.EncodeToString(InputPacket{Type: PacketInput, DX: 1}.Encode())) {
		t.Fatalf("original after tamper attempts: %q", event)
	}
	if event, _ := p.send(dg); event != "dropped replay" {
		t.Fatalf("replay: %q", event)
	}
}

// connect2 is connect with a clock step first, so the handshake limiter and
// the timestamp ordering both accept it.
func (p *peer) connect2(t *testing.T, static noise.DHKey) *Session {
	t.Helper()
	p.command("!advance 1")
	time.Sleep(2 * time.Millisecond)
	return p.connect(static)
}

func TestInteropUnpairedAndWrongKey(t *testing.T) {
	paired := mustKey(t)
	p := startPeer(t, paired.Public)

	stranger := mustKey(t)
	_, m1, _ := NewIKInitiation(stranger, p.pub, time.Now())
	if event, reply := p.send(m1); event != "dropped unpaired" || reply != nil {
		t.Fatalf("unpaired: %q %v", event, reply)
	}

	p.command("!advance 1")
	wrongMac := mustKey(t)
	_, m1, _ = NewIKInitiation(paired, wrongMac.Public, time.Now())
	if event, reply := p.send(m1); event != "dropped handshakeFailed" || reply != nil {
		t.Fatalf("wrong pinned key: %q %v", event, reply)
	}
}

func TestInteropGateStates(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	s := p.connect(static)
	p.sendPacket(s, InputPacket{Type: PacketKeepalive})

	cases := []struct {
		cmds  []string
		state HelperState
		code  string
	}{
		{[]string{"!toggle off"}, StateDisabled, "1"},
		{[]string{"!toggle on", "!locked on"}, StateLocked, "2"},
		{[]string{"!locked off", "!console off"}, StateNotConsole, "4"},
		{[]string{"!console on", "!sharing off"}, StateNoSession, "3"},
		{[]string{"!sharing on", "!grant off"}, StateNeedsGrant, "6"},
	}
	for _, c := range cases {
		for _, cmd := range c.cmds {
			if r := p.command(cmd); r != "OK" {
				t.Fatalf("%s -> %s", cmd, r)
			}
		}
		p.command("!advance 0.3")
		event, reply := p.sendPacket(s, InputPacket{Type: PacketInput, DX: 5})
		if event != "denied "+c.code {
			t.Fatalf("state %v: event %q", c.state, event)
		}
		if st := p.status(s, reply); st.State != c.state {
			t.Fatalf("status %v want %v", st.State, c.state)
		}
	}
	p.command("!grant on")
	p.command("!advance 0.3")
	if event, _ := p.sendPacket(s, InputPacket{Type: PacketInput, DX: 5}); !strings.HasPrefix(event, "input ") {
		t.Fatalf("recovered state: %q", event)
	}
}

func TestInteropSilenceAndBye(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	s := p.connect(static)
	p.sendPacket(s, InputPacket{Type: PacketKeepalive})
	p.command("!advance 1.5")
	if got := p.command("!tick"); got != "ended silence" {
		t.Fatalf("tick: %q", got)
	}
	if event, _ := p.sendPacket(s, InputPacket{Type: PacketKeepalive}); event != "dropped noSession" {
		t.Fatalf("after silence: %q", event)
	}

	s = p.connect2(t, static)
	p.sendPacket(s, InputPacket{Type: PacketKeepalive})
	if event, _ := p.sendPacket(s, InputPacket{Type: PacketBye}); event != "ended bye" {
		t.Fatalf("bye: %q", event)
	}
}

func TestInteropPairingSAS(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t) // nothing paired yet: XX works without any
	ini, err := NewHandshake(HandshakeConfig{Pattern: PatternXX, Initiator: true, Static: static})
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := ini.Write(nil)
	if event, reply := p.send(m1); event != "pair-ok" || reply == nil {
		t.Fatalf("m1: %q", event)
	}
	_, reply := p.send(m1) // restart: a second attempt replaces the first
	if _, err := ini.Read(reply); err != nil {
		t.Fatalf("reading Swift message 2: %v", err)
	}
	m3, err := ini.Write([]byte("Linux box"))
	if err != nil {
		t.Fatal(err)
	}
	event, reply := p.send(m3)
	want := "pair-sas " + SAS(ini.Hash()) + " Linux_box " + hex.EncodeToString(static.Public)
	if event != want || reply != nil {
		t.Fatalf("pairing result\n got %q\nwant %q", event, want)
	}
	if !bytes.Equal(ini.PeerStatic(), p.pub) {
		t.Fatal("Go did not learn the Mac's static key")
	}
}

func TestInteropPairingRejectsBadDeviceName(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t)
	ini, _ := NewHandshake(HandshakeConfig{Pattern: PatternXX, Initiator: true, Static: static})
	m1, _ := ini.Write(nil)
	_, reply := p.send(m1)
	if _, err := ini.Read(reply); err != nil {
		t.Fatal(err)
	}
	m3, _ := ini.Write([]byte("evil\nname"))
	if event, _ := p.send(m3); !strings.HasPrefix(event, "pair-error") {
		t.Fatalf("control characters in the name were accepted: %q", event)
	}
}
