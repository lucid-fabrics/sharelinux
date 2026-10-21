package machelper

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
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
	return startPeerWith(t, nil, paired...)
}

// startPeerWith adds extra corepeer arguments (--key, --timestamps).
func startPeerWith(t *testing.T, extra []string, paired ...[]byte) *peer {
	t.Helper()
	args := append([]string{}, extra...)
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

// stop ends the peer process (a helper restart).
func (p *peer) stop() {
	_ = p.stdin.Close()
	_ = p.cmd.Wait()
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
	clamped := InputPacket{Type: PacketInput, DX: -32752, DY: 32752, SX: 2000, SY: -2000}
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
	if event, reply := p.send(m1); event != "dropped badMac" || reply != nil {
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

// pairStep sends one pairing datagram and returns the event text and reply.
func (p *peer) pairStep(dg []byte) (string, []byte) { return p.send(dg) }

// pairUpTo drives a Go initiator against the Swift responder as far as the
// reveal; the returned function sends the reveal (or any replacement).
func pairUpTo(t *testing.T, p *peer, static noise.DHKey, name string) (*PairingInitiator, func(reveal []byte) string) {
	t.Helper()
	ini, err := NewPairingInitiator(static, name)
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := ini.Start()
	event, m2 := p.send(m1)
	if event != "pair-ok" || m2 == nil {
		t.Fatalf("m1: %q", event)
	}
	out, err := ini.Receive(m2)
	if err != nil || len(out) != 2 {
		t.Fatalf("reading Swift message 2: %v", err)
	}
	if event, reply := p.send(out[0]); event != "pair-ok" || reply != nil {
		t.Fatalf("m3: %q", event)
	}
	event, rm := p.send(out[1])
	if event != "pair-ok" || rm == nil {
		t.Fatalf("commit: %q", event)
	}
	out, err = ini.Receive(rm)
	if err != nil || len(out) != 1 {
		t.Fatalf("reading Swift nonce: %v", err)
	}
	return ini, func(reveal []byte) string {
		if reveal == nil {
			reveal = out[0]
		}
		event, _ := p.send(reveal)
		return event
	}
}

func TestInteropPairingCodeNameAndKeysAgree(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t)
	p.command("!pair open")
	ini, reveal := pairUpTo(t, p, static, "Linux box")
	event := reveal(nil)
	want := "pair-sas " + ini.Code() + " Linux_box " + hex.EncodeToString(static.Public)
	if event != want {
		t.Fatalf("pairing result\n got %q\nwant %q", event, want)
	}
	if !bytes.Equal(ini.PeerStatic(), p.pub) {
		t.Fatal("Go did not learn the Mac's static key")
	}
	if got := p.command("!pair confirm"); got != "confirmed Linux_box "+hex.EncodeToString(static.Public) {
		t.Fatalf("confirm: %q", got)
	}
	// One pairing per click: the window is shut now.
	m1, _ := mustPairing(t, static, "x").Start()
	if event, _ := p.send(m1); event != "pair-error closed" {
		t.Fatalf("after confirm: %q", event)
	}
}

func TestInteropPairingNeedsTheLocalClick(t *testing.T) {
	p := startPeer(t)
	m1, _ := mustPairing(t, mustKey(t), "Linux box").Start()
	if event, reply := p.send(m1); event != "pair-error closed" || reply != nil {
		t.Fatalf("window never opened: %q", event)
	}
}

func TestInteropRevealThatDoesNotMatchTheCommitIsRefused(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t)
	p.command("!pair open")
	ini, reveal := pairUpTo(t, p, static, "Linux box")
	lie, err := ini.session.Seal(append([]byte{PairingTagReveal}, bytes.Repeat([]byte{9}, 32)...), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event := reveal(lie); event != "pair-error commitMismatch" {
		t.Fatalf("lying reveal: %q", event)
	}
}

func TestInteropPairingWindowClosesAfterThreeFailures(t *testing.T) {
	p := startPeer(t)
	p.command("!pair open")
	for i := 0; i < 3; i++ {
		p.command("!advance 1") // attempts may not start faster than the spacing
		static := mustKey(t)
		ini, reveal := pairUpTo(t, p, static, "Linux box")
		lie, _ := ini.session.Seal(append([]byte{PairingTagReveal}, bytes.Repeat([]byte{byte(i)}, 32)...), time.Now())
		if event := reveal(lie); event != "pair-error commitMismatch" {
			t.Fatalf("attempt %d: %q", i, event)
		}
	}
	m1, _ := mustPairing(t, mustKey(t), "Linux box").Start()
	if event, _ := p.send(m1); event != "pair-error closed" {
		t.Fatalf("window should be shut after 3 failures: %q", event)
	}
}

func TestInteropPairingRejectsInvisibleDeviceNames(t *testing.T) {
	for _, name := range []string{"evil\nname", "zero\u200bwidth", "bidi\u202eflip", "fill\u3164er"} {
		static := mustKey(t)
		p := startPeer(t)
		p.command("!pair open")
		ini, _ := NewHandshake(HandshakeConfig{Pattern: PatternXX, Initiator: true, Static: static})
		m1, _ := ini.Write(nil)
		_, m2 := p.send(m1)
		if _, err := ini.Read(m2); err != nil {
			t.Fatal(err)
		}
		m3, _ := ini.Write([]byte(name)) // bypasses the Go-side check on purpose
		if event, _ := p.send(m3); event != "pair-error badDeviceName" {
			t.Fatalf("%q accepted: %q", name, event)
		}
	}
}

func TestInteropPrologueMismatchIsRefused(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	ini, _ := NewHandshake(HandshakeConfig{
		Pattern: PatternIK, Initiator: true, Static: static, RemoteStatic: p.pub,
		Prologue: []byte("OSS-mousehelper v1 session Noise_IK_25519_ChaChaPoly_SHA256 initiator=linux responder=mac"),
	})
	ts := TAI64N(time.Now())
	m1, err := ini.Write(ts[:])
	if err != nil {
		t.Fatal(err)
	}
	if event, reply := p.send(m1); event != "dropped handshakeFailed" || reply != nil {
		t.Fatalf("old-version prologue: %q", event)
	}
	p.command("!advance 1")
	p.connect(static) // the standard prologue works
}

func TestInteropJunkWithoutMAC1NeverReachesTheHandshake(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	_, m1, _ := NewIKInitiation(static, p.pub, time.Now())
	for i := 0; i < 50; i++ {
		junk := bytes.Clone(m1)
		junk[len(junk)-1-i%MAC1Size] ^= 0xff
		if event, reply := p.send(junk); event != "dropped badMac" || reply != nil {
			t.Fatalf("junk %d: %q", i, event)
		}
	}
	if event, _ := p.send(m1); event != "answered" {
		t.Fatalf("real initiation after junk: %q", event)
	}
}

func TestInteropOneSourceCannotHammerHandshakes(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	var events []string
	for i := 0; i < 4; i++ {
		_, m1, _ := NewIKInitiation(static, p.pub, time.Now().Add(time.Duration(i)*time.Second))
		event, _ := p.send(m1)
		events = append(events, event)
	}
	if strings.Join(events, ",") != "answered,answered,dropped handshakeRateLimit,dropped handshakeRateLimit" {
		t.Fatalf("per-source limit: %v", events)
	}
}

func TestInteropMotionBudget(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	s := p.connect(static)
	big := InputPacket{Type: PacketInput, DX: 32767, DY: 32767}
	delivered, denied := 0, 0
	for i := 0; i < 8; i++ {
		event, _ := p.sendPacket(s, big)
		switch {
		case strings.Contains(event, "input "):
			delivered++
		case strings.HasSuffix(event, "denied 5"):
			denied++
		default:
			t.Fatalf("packet %d: %q", i, event)
		}
	}
	// 20 000 pt/s * 16 = 320 000 units; each packet costs 2 * 32752.
	if want := MotionUnitsPerSecond / (2 * int(MaxMotion)); delivered != want || delivered+denied != 8 {
		t.Fatalf("delivered %d denied %d, want %d delivered", delivered, denied, want)
	}
}

func TestInteropStaleEnvironmentDeniesInput(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t, static.Public)
	s := p.connect(static)
	p.sendPacket(s, InputPacket{Type: PacketKeepalive})
	p.command("!stale on")
	p.command("!advance 0.3")
	event, reply := p.sendPacket(s, InputPacket{Type: PacketInput, DX: 1})
	if event != "denied 2" {
		t.Fatalf("stale snapshot: %q", event)
	}
	if st := p.status(s, reply); st.State != StateLocked {
		t.Fatalf("status %v", st)
	}
}

func TestInteropReplayedInitiationIsRefusedAfterARestart(t *testing.T) {
	static := mustKey(t)
	priv := make([]byte, 32)
	_, _ = rand.Read(priv)
	file := filepath.Join(t.TempDir(), "timestamps.json")
	args := []string{"--key", hex.EncodeToString(priv), "--timestamps", file}

	first := startPeerWith(t, args, static.Public)
	_, m1, _ := NewIKInitiation(static, first.pub, time.Now())
	if event, _ := first.send(m1); event != "answered" {
		t.Fatalf("first run: %q", event)
	}
	first.stop()

	second := startPeerWith(t, args, static.Public)
	if !bytes.Equal(first.pub, second.pub) {
		t.Fatal("restart changed the static key")
	}
	if event, reply := second.send(m1); event != "dropped staleTimestamp" || reply != nil {
		t.Fatalf("captured initiation replayed after restart: %q", event)
	}
	second.connect2(t, static) // a new initiation is welcome
}

// pairToNonce drives a Go initiator against the Swift window up to the point
// where the Mac has sent r_M. It returns the initiator (its Code() is already
// known, the reveal has not been sent) or the event that stopped it.
func pairToNonce(t *testing.T, p *peer, static noise.DHKey) (*PairingInitiator, string) {
	t.Helper()
	ini, err := NewPairingInitiator(static, "Linux box")
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := ini.Start()
	event, m2 := p.send(m1)
	if event != "pair-ok" || m2 == nil {
		return nil, event
	}
	out, err := ini.Receive(m2)
	if err != nil || len(out) != 2 {
		t.Fatalf("reading Swift message 2: %v", err)
	}
	if event, _ := p.send(out[0]); event != "pair-ok" {
		t.Fatalf("m3: %q", event)
	}
	event, rm := p.send(out[1])
	if event != "pair-ok" || rm == nil {
		t.Fatalf("commit: %q", event)
	}
	if _, err := ini.Receive(rm); err != nil {
		t.Fatalf("reading Swift nonce: %v", err)
	}
	return ini, ""
}

// The abort-grinding man in the middle: finish the Linux leg, run XX and the
// commit against the Mac, read r_M, compute the Mac's code, and start over
// when it is not the one he needs. The window must stop him after three.
func TestInteropAbortGrindingMITMSeesAtMostThreeCodes(t *testing.T) {
	p := startPeer(t)
	p.command("!pair open")
	codes := 0
	var stopped string
	for i := 0; i < 100; i++ {
		p.command("!advance 1")
		ini, event := pairToNonce(t, p, mustKey(t))
		if ini == nil {
			stopped = event
			break
		}
		if len(ini.Code()) != 6 {
			t.Fatalf("no code after r_M: %q", ini.Code())
		}
		codes++ // he compares it with the Linux code, they differ, he aborts
	}
	if codes != 3 || stopped != "pair-error closed" {
		t.Fatalf("attacker saw %d codes, stopped with %q; want 3 and closed", codes, stopped)
	}
}

// Datagrams from another source address cannot touch or count against an
// attempt in flight (finding 2), and the honest attempt still finishes.
func TestInteropForgedSourcesCannotChargeOrKillAnAttempt(t *testing.T) {
	static := mustKey(t)
	p := startPeer(t)
	p.command("!pair open")
	p.command("!src 1")
	ini, event := pairToNonce(t, p, static)
	if ini == nil {
		t.Fatalf("honest attempt: %q", event)
	}
	p.command("!src 9")
	junk := append([]byte{byte(KindPairingTransport)}, bytes.Repeat([]byte{0x5a}, 9+33+16)...)
	for i := 0; i < 6; i++ {
		if event, _ := p.send(junk); event != "pair-error otherSource" {
			t.Fatalf("forged packet %d: %q", i, event)
		}
	}
	p.command("!advance 1")
	m1, _ := mustPairing(t, mustKey(t), "x").Start()
	if event, _ := p.send(m1); event != "pair-error otherSource" {
		t.Fatalf("forged message 1: %q", event)
	}
	p.command("!src 1")
	// Garbage under the attempt's own source is dropped, not counted.
	for i := 0; i < 6; i++ {
		if event, _ := p.send(junk); event != "pair-error unauthenticated" {
			t.Fatalf("garbled packet %d: %q", i, event)
		}
	}
	reveal, err := ini.session.Seal(append([]byte{PairingTagReveal}, ini.nonce...), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := "pair-sas " + ini.Code() + " Linux_box " + hex.EncodeToString(static.Public)
	if event, _ := p.send(reveal); event != want {
		t.Fatalf("honest reveal after forgeries\n got %q\nwant %q", event, want)
	}
}
