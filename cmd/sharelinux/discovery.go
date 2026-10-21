package main

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/zeroconf/v2"
	E "github.com/sagernet/sing/common/exceptions"
)

// rfbService is what a Mac with Screen Sharing turned on announces on the network.
const rfbService = "_rfb._tcp"

// scanWindow is how long one browse listens for answers.
const scanWindow = 4 * time.Second

// discoveredMac is a Mac found on the local network.
type discoveredMac struct {
	Name string
	// Host is the .local name, Addresses the IPv4 then IPv6 addresses it announced.
	Host      string
	Addresses []string
	Port      int
}

// unescapeLabel undoes the DNS escaping of a service instance name: a
// backslash before a character stands for the character, and a backslash with
// three digits for the byte of that decimal value. Names like "Wassim\226\128\153s Mac"
// come back as written by the person who named the Mac.
func unescapeLabel(label string) string {
	if !strings.Contains(label, "\\") {
		return label
	}
	var result []byte
	for index := 0; index < len(label); index++ {
		if label[index] != '\\' || index+1 >= len(label) {
			result = append(result, label[index])
			continue
		}
		if value, ok := decimalByte(label[index+1:]); ok {
			result = append(result, value)
			index += 3
			continue
		}
		result = append(result, label[index+1])
		index++
	}
	return string(result)
}

// decimalByte reads three decimal digits at the start of text as a byte.
func decimalByte(text string) (byte, bool) {
	if len(text) < 3 {
		return 0, false
	}
	value := 0
	for _, digit := range []byte(text[:3]) {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		value = value*10 + int(digit-'0')
	}
	return byte(value), value <= 255
}

// newDiscoveredMac reads one mDNS answer. It returns false for an answer
// without a name or a usable address.
func newDiscoveredMac(entry *zeroconf.ServiceEntry) (discoveredMac, bool) {
	if entry == nil || entry.Port <= 0 || entry.Port > 65535 {
		return discoveredMac{}, false
	}
	mac := discoveredMac{
		Name: strings.TrimSpace(unescapeLabel(entry.Instance)),
		Host: strings.TrimSuffix(entry.HostName, "."),
		Port: entry.Port,
	}
	for _, ip := range entry.AddrIPv4 {
		mac.Addresses = append(mac.Addresses, ip.String())
	}
	for _, ip := range entry.AddrIPv6 {
		if ip.IsLinkLocalUnicast() {
			continue
		}
		mac.Addresses = append(mac.Addresses, ip.String())
	}
	if mac.Name == "" {
		mac.Name = strings.TrimSuffix(mac.Host, ".local")
	}
	if mac.Name == "" || (mac.Host == "" && len(mac.Addresses) == 0) {
		return discoveredMac{}, false
	}
	return mac, true
}

// key identifies a Mac across scans.
func (m discoveredMac) key() string {
	return strings.ToLower(m.Name) + "|" + m.Host
}

// dialHost is the name to connect to. A numeric address works without mDNS
// resolution in the system resolver, so it is preferred over the .local name.
func (m discoveredMac) dialHost() string {
	if len(m.Addresses) > 0 {
		return m.Addresses[0]
	}
	return m.Host
}

// target is the connection a "Connect" on this Mac starts. The account is
// asked for in the sign-in dialog.
func (m discoveredMac) target() target {
	return target{Host: m.dialHost(), Port: m.Port}
}

// subtitle is the second line of the "On this network" row.
func (m discoveredMac) subtitle() string {
	host := m.Host
	if host == "" {
		host = m.dialHost()
	}
	return host + " · Screen Sharing is on"
}

// hostKey is a host or address in the one form two spellings of it share: lower
// case, no trailing dot, no brackets or zone, an IP in its canonical text, and
// a name without ".local".
func hostKey(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	bare, _, _ := strings.Cut(host, "%")
	address, err := netip.ParseAddr(bare)
	if err == nil {
		return address.Unmap().String()
	}
	return host
}

// matches reports whether a saved connection is this Mac: the same port and
// either the full mDNS host name or any address the Mac announced (IPv4 or
// IPv6, however it is written). The desktop name never decides, two Macs can
// share one, and a plain DNS name only matches the identical name.
func (m discoveredMac) matches(saved savedConnection) bool {
	if saved.Port != m.Port {
		return false
	}
	host := hostKey(saved.Host)
	if host == "" {
		return false
	}
	if m.Host != "" && host == hostKey(m.Host) {
		return true
	}
	for _, address := range m.Addresses {
		if host == hostKey(address) {
			return true
		}
	}
	return false
}

// unsaved drops the Macs that are already in the saved list and sorts by name.
func unsaved(found []discoveredMac, saved []savedConnection) []discoveredMac {
	var result []discoveredMac
	for _, mac := range found {
		known := slices.ContainsFunc(saved, mac.matches)
		if !known {
			result = append(result, mac)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

// mergeScan folds the answers of one scan into a list without repeats. Answers
// from several network interfaces repeat; the first one with addresses wins and
// later ones only add addresses.
func mergeScan(into []discoveredMac, mac discoveredMac) []discoveredMac {
	for index := range into {
		if into[index].key() != mac.key() {
			continue
		}
		for _, address := range mac.Addresses {
			if !slices.Contains(into[index].Addresses, address) {
				into[index].Addresses = append(into[index].Addresses, address)
			}
		}
		// IPv4 first, so dialHost prefers it.
		sort.SliceStable(into[index].Addresses, func(i, j int) bool {
			return isIPv4(into[index].Addresses[i]) && !isIPv4(into[index].Addresses[j])
		})
		return into
	}
	return append(into, mac)
}

func isIPv4(address string) bool {
	ip := net.ParseIP(address)
	return ip != nil && ip.To4() != nil
}

// scanMacs browses once and returns what answered within scanWindow, calling
// onFound for each new Mac as soon as it answers.
func scanMacs(ctx context.Context, onFound func(discoveredMac)) ([]discoveredMac, error) {
	ctx, cancel := context.WithTimeout(ctx, scanWindow)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry)
	var found []discoveredMac
	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entries {
			mac, ok := newDiscoveredMac(entry)
			if !ok {
				continue
			}
			before := len(found)
			found = mergeScan(found, mac)
			if len(found) > before && onFound != nil {
				onFound(mac)
			}
		}
	}()
	err := zeroconf.Browse(ctx, rfbService, "local.", entries)
	<-done
	if err != nil && ctx.Err() == nil {
		return found, E.Cause(err, "look for Macs on the network")
	}
	return found, nil
}
