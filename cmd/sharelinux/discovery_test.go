package main

import (
	"net"
	"testing"

	"github.com/libp2p/zeroconf/v2"
)

func entry(instance, host string, port int, addrs ...string) *zeroconf.ServiceEntry {
	e := &zeroconf.ServiceEntry{HostName: host, Port: port}
	e.Instance = instance
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip.To4() != nil {
			e.AddrIPv4 = append(e.AddrIPv4, ip)
		} else {
			e.AddrIPv6 = append(e.AddrIPv6, ip)
		}
	}
	return e
}

func TestNewDiscoveredMac(t *testing.T) {
	mac, ok := newDiscoveredMac(entry("Mac mini", "mac-mini.local.", 5900, "192.0.2.30", "fe80::1", "2001:db8::5"))
	if !ok || mac.Name != "Mac mini" || mac.Host != "mac-mini.local" || mac.Port != 5900 {
		t.Fatalf("%+v %v", mac, ok)
	}
	if len(mac.Addresses) != 2 || mac.Addresses[0] != "192.0.2.30" || mac.Addresses[1] != "2001:db8::5" {
		t.Fatalf("link-local IPv6 is dropped: %v", mac.Addresses)
	}
	if mac.dialHost() != "192.0.2.30" {
		t.Fatal("a numeric address is dialled first")
	}
	if mac.subtitle() != "mac-mini.local · Screen Sharing is on" {
		t.Fatal(mac.subtitle())
	}
	if got := mac.target(); got.Host != "192.0.2.30" || got.Port != 5900 || got.User != "" {
		t.Fatalf("%+v", got)
	}
}

func TestNewDiscoveredMacRejectsJunk(t *testing.T) {
	for name, e := range map[string]*zeroconf.ServiceEntry{
		"nil":          nil,
		"no port":      entry("A", "a.local.", 0, "10.0.0.1"),
		"port too big": entry("A", "a.local.", 70000, "10.0.0.1"),
		"nowhere":      entry("A", "", 5900),
	} {
		if _, ok := newDiscoveredMac(e); ok {
			t.Errorf("%s accepted", name)
		}
	}
	mac, ok := newDiscoveredMac(entry("", "studio.local.", 5900, "10.0.0.2"))
	if !ok || mac.Name != "studio" {
		t.Fatalf("the host names a Mac without an instance name: %+v", mac)
	}
	hostOnly, ok := newDiscoveredMac(entry("Lab", "lab.local.", 5900))
	if !ok || hostOnly.dialHost() != "lab.local" {
		t.Fatal("no address falls back to the .local name")
	}
}

func TestMergeScanDeduplicatesInterfaces(t *testing.T) {
	first, _ := newDiscoveredMac(entry("Mac mini", "mac-mini.local.", 5900, "2001:db8::5"))
	second, _ := newDiscoveredMac(entry("Mac mini", "mac-mini.local.", 5900, "192.0.2.30"))
	other, _ := newDiscoveredMac(entry("Air", "air.local.", 5900, "192.0.2.31"))
	list := mergeScan(nil, first)
	list = mergeScan(list, second)
	list = mergeScan(list, second)
	list = mergeScan(list, other)
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
	if list[0].dialHost() != "192.0.2.30" || len(list[0].Addresses) != 2 {
		t.Fatalf("addresses merge with IPv4 first: %v", list[0].Addresses)
	}
}

func TestUnsavedHidesKnownMacsAndSorts(t *testing.T) {
	mini, _ := newDiscoveredMac(entry("Mac mini", "mac-mini.local.", 5900, "192.0.2.30"))
	air, _ := newDiscoveredMac(entry("air", "air.local.", 5900, "192.0.2.31"))
	studio, _ := newDiscoveredMac(entry("Studio", "studio.local.", 5900, "192.0.2.32"))
	saved := []savedConnection{
		{Host: "Mac-Mini.local", Port: 5900},
		{Host: "192.0.2.32", Port: 5900},
		{Host: "air.local", Port: 5901},
	}
	got := unsaved([]discoveredMac{studio, mini, air}, saved)
	if len(got) != 1 || got[0].Name != "air" {
		t.Fatalf("a saved Mac matches by host name or address on the same port: %+v", got)
	}
	got = unsaved([]discoveredMac{studio, mini, air}, nil)
	if len(got) != 3 || got[0].Name != "air" || got[1].Name != "Mac mini" || got[2].Name != "Studio" {
		t.Fatalf("sorted by name: %+v", got)
	}
}

func TestUnescapeLabel(t *testing.T) {
	cases := map[string]string{
		"Mac mini":               "Mac mini",
		`Mac\ mini`:              "Mac mini",
		`cicd\226\128\153s iMac`: "cicd\u2019s iMac",
		`a\.b\\c`:                `a.b\c`,
		`trailing\`:              `trailing\`,
		`bad\999x`:               "bad999x",
		`two\04`:                 "two04",
	}
	for in, want := range cases {
		if got := unescapeLabel(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	mac, ok := newDiscoveredMac(entry(`MacBook\ Pro`, "mb.local.", 5900, "10.0.0.4"))
	if !ok || mac.Name != "MacBook Pro" {
		t.Fatalf("%+v", mac)
	}
}

func TestModelKeys(t *testing.T) {
	info := modelKeys("MacBook-Pro-m3-Max", "MacBook-Pro-m3-Max.local.")
	rfb := modelKeys("MacBook Pro m3 Max", "MacBook-Pro-m3-Max.local")
	if len(info) != 2 || len(rfb) != 2 || info[0] != rfb[0] {
		t.Fatalf("the host key joins the two answers: %v %v", info, rfb)
	}
	if info[1] == rfb[1] {
		t.Fatal("different instance names stay different")
	}
	if got := modelKeys("", ""); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
