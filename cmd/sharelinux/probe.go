package main

import (
	"context"
	"net"
	"sync"
	"time"
)

// probeTimeout is how long a Mac gets to answer a probe before it counts as offline.
const probeTimeout = 1500 * time.Millisecond

// probeDial is the connect call of probe, replaceable in tests.
type probeDial func(ctx context.Context, network, address string) (net.Conn, error)

// probe answers Online when the address accepts a TCP connection and Offline
// when nothing answers in time. It cannot tell a sleeping Mac from one that is
// off, so the list has no "Asleep" state.
func probe(ctx context.Context, address string, dial probeDial) reachability {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return reachOffline
	}
	_ = conn.Close()
	return reachOnline
}

// probeAll checks every address at once and returns the answers by address.
func probeAll(ctx context.Context, addresses []string, dial probeDial) map[string]reachability {
	result := make(map[string]reachability, len(addresses))
	var access sync.Mutex
	var group sync.WaitGroup
	for _, address := range addresses {
		group.Add(1)
		go func() {
			defer group.Done()
			state := probe(ctx, address, dial)
			access.Lock()
			result[address] = state
			access.Unlock()
		}()
	}
	group.Wait()
	return result
}

func defaultProbeDial() probeDial {
	var dialer net.Dialer
	return dialer.DialContext
}
