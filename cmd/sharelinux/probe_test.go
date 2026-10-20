package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestProbeOnlineAndOffline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	address := listener.Addr().String()
	if got := probe(context.Background(), address, defaultProbeDial()); got != reachOnline {
		t.Fatalf("listening port: %v", got)
	}
	_ = listener.Close()
	if got := probe(context.Background(), address, defaultProbeDial()); got != reachOffline {
		t.Fatalf("closed port: %v", got)
	}
}

func TestProbeTimesOut(t *testing.T) {
	hang := func(ctx context.Context, network, address string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	started := time.Now()
	if got := probe(context.Background(), "10.255.255.1:5900", hang); got != reachOffline {
		t.Fatal("no answer is offline")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("the probe must give up after probeTimeout")
	}
}

func TestProbeAll(t *testing.T) {
	fake := func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "up:5900" {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		}
		return nil, context.DeadlineExceeded
	}
	result := probeAll(context.Background(), []string{"up:5900", "down:5900"}, fake)
	if result["up:5900"] != reachOnline || result["down:5900"] != reachOffline || len(result) != 2 {
		t.Fatalf("%v", result)
	}
}

func TestReachabilityText(t *testing.T) {
	if reachOnline.String() != "Online" || reachOffline.String() != "Offline" || reachUnknown.String() != "" {
		t.Fatal("status words")
	}
}
