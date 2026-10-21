package machelper

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// Cacophony vectors for Noise_XX and Noise_IK (25519, ChaChaPoly, SHA256),
// copied unchanged from
// https://github.com/centromere/cacophony/blob/master/vectors/cacophony.txt
// and shared byte for byte with the Swift tests.

var zeroTime = time.Unix(1_800_000_000, 0)

type vectorMessage struct {
	Payload    string `json:"payload"`
	Ciphertext string `json:"ciphertext"`
}

type vector struct {
	ProtocolName     string          `json:"protocol_name"`
	InitPrologue     string          `json:"init_prologue"`
	InitStatic       string          `json:"init_static"`
	InitEphemeral    string          `json:"init_ephemeral"`
	InitRemoteStatic string          `json:"init_remote_static"`
	RespPrologue     string          `json:"resp_prologue"`
	RespStatic       string          `json:"resp_static"`
	RespEphemeral    string          `json:"resp_ephemeral"`
	HandshakeHash    string          `json:"handshake_hash"`
	Messages         []vectorMessage `json:"messages"`
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func dhKey(t *testing.T, private string) noise.DHKey {
	t.Helper()
	priv := unhex(t, private)
	pub, err := noise.DH25519.DH(priv, basepoint())
	if err != nil {
		t.Fatal(err)
	}
	return noise.DHKey{Private: priv, Public: pub}
}

func basepoint() []byte {
	b := make([]byte, 32)
	b[0] = 9
	return b
}

func TestNoiseVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/noise-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	patterns := map[string]Pattern{
		"Noise_XX_25519_ChaChaPoly_SHA256": PatternXX,
		"Noise_IK_25519_ChaChaPoly_SHA256": PatternIK,
	}
	if len(file.Vectors) != len(patterns) {
		t.Fatalf("expected %d vectors, have %d", len(patterns), len(file.Vectors))
	}
	for _, v := range file.Vectors {
		pattern, ok := patterns[v.ProtocolName]
		if !ok {
			t.Fatalf("unexpected protocol %s", v.ProtocolName)
		}
		t.Run(v.ProtocolName, func(t *testing.T) { runVector(t, v, pattern) })
	}
}

func runVector(t *testing.T, v vector, pattern Pattern) {
	initStatic, respStatic := dhKey(t, v.InitStatic), dhKey(t, v.RespStatic)
	cfgI := HandshakeConfig{
		Pattern: pattern, Initiator: true, Static: initStatic,
		Prologue: unhex(t, v.InitPrologue), random: bytes.NewReader(unhex(t, v.InitEphemeral)), rawNoise: true,
	}
	cfgR := HandshakeConfig{
		Pattern: pattern, Static: respStatic,
		Prologue: unhex(t, v.RespPrologue), random: bytes.NewReader(unhex(t, v.RespEphemeral)), rawNoise: true,
	}
	if pattern == PatternIK {
		cfgI.RemoteStatic = respStatic.Public
		if v.InitRemoteStatic != hex.EncodeToString(respStatic.Public) {
			t.Fatal("vector remote static does not match responder static")
		}
	}
	ini, err := NewHandshake(cfgI)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewHandshake(cfgR)
	if err != nil {
		t.Fatal(err)
	}

	i := 0
	for ; !(ini.Complete() && res.Complete()); i++ {
		m := v.Messages[i]
		writer, reader := ini, res
		if i%2 == 1 {
			writer, reader = res, ini
		}
		dg, err := writer.Write(unhex(t, m.Payload))
		if err != nil {
			t.Fatalf("message %d write: %v", i, err)
		}
		if got := hex.EncodeToString(dg[1:]); got != m.Ciphertext {
			t.Fatalf("message %d ciphertext\n got %s\nwant %s", i, got, m.Ciphertext)
		}
		pt, err := reader.Read(dg)
		if err != nil {
			t.Fatalf("message %d read: %v", i, err)
		}
		if !bytes.Equal(pt, unhex(t, m.Payload)) {
			t.Fatalf("message %d payload", i)
		}
	}
	want := unhex(t, v.HandshakeHash)
	if !bytes.Equal(ini.Hash(), want) || !bytes.Equal(res.Hash(), want) {
		t.Fatal("handshake hash mismatch")
	}
	if !bytes.Equal(ini.PeerStatic(), respStatic.Public) || !bytes.Equal(res.PeerStatic(), initStatic.Public) {
		t.Fatal("peer static mismatch")
	}

	// Post-handshake messages: the cipher states from Split, with explicit counters.
	counters := map[bool]uint64{}
	sessI, err := ini.Session(zeroTime)
	if err != nil {
		t.Fatal(err)
	}
	sessR, err := res.Session(zeroTime)
	if err != nil {
		t.Fatal(err)
	}
	for ; i < len(v.Messages); i++ {
		m := v.Messages[i]
		fromInitiator := i%2 == 0
		sender, receiver := sessI, sessR
		if !fromInitiator {
			sender, receiver = sessR, sessI
		}
		sender.counter = counters[fromInitiator]
		dg, err := sender.Seal(unhex(t, m.Payload), zeroTime)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(dg[9:]); got != m.Ciphertext {
			t.Fatalf("transport message %d\n got %s\nwant %s", i, got, m.Ciphertext)
		}
		pt, err := receiver.Open(dg, len(m.Payload)/2, zeroTime)
		if err != nil || !bytes.Equal(pt, unhex(t, m.Payload)) {
			t.Fatalf("transport message %d open: %v", i, err)
		}
		counters[fromInitiator]++
	}
}
