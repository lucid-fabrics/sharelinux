package main

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// handshake logs in to the mock over a pipe with the VNC password and returns
// the banner it greeted with and the name field of its ServerInit.
func handshake(t *testing.T) (banner string, name []byte) {
	t.Helper()
	baseWidth, baseHeight = 64, 64
	server, client := net.Pipe()
	defer client.Close()
	go serve(server, "secret")
	client.SetDeadline(time.Now().Add(5 * time.Second))
	read := func(size int) []byte {
		buffer := make([]byte, size)
		if _, err := io.ReadFull(client, buffer); err != nil {
			t.Fatal(err)
		}
		return buffer
	}
	greeting := read(12)
	client.Write(greeting)
	if types := read(2); types[0] != 1 || types[1] != 2 {
		t.Fatalf("security types %v", types)
	}
	client.Write([]byte{2})
	challenge := read(16)
	client.Write(vncResponse("secret", challenge))
	if result := read(4); binary.BigEndian.Uint32(result) != 0 {
		t.Fatal("login refused")
	}
	client.Write([]byte{1})
	header := read(24)
	return string(greeting), read(int(binary.BigEndian.Uint32(header[20:])))
}

func TestPlainServerAdvertisesNoFlags(t *testing.T) {
	observeOnly = false
	banner, name := handshake(t)
	if banner != "RFB 003.008\n" || string(name) != "mockrfb" {
		t.Fatalf("banner %q name %q", banner, name)
	}
}

func TestObserveOnlyServerAdvertisesTheFlag(t *testing.T) {
	observeOnly = true
	defer func() { observeOnly = false }()
	banner, name := handshake(t)
	if banner != "RFB 003.889\n" {
		t.Fatalf("banner %q", banner)
	}
	if len(name) != 22+len("mockrfb") || name[0] != 0 || name[1] != 0 {
		t.Fatalf("name field %v", name)
	}
	if flags := binary.BigEndian.Uint32(name[2:]); flags != 0x11 {
		t.Fatalf("flags %#x, want observe-only|extended 0x11", flags)
	}
	if string(name[22:]) != "mockrfb" {
		t.Fatalf("name %q", name[22:])
	}
}
