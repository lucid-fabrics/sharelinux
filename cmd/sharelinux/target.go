package main

import (
	"net"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

const defaultPort = 5900

// target is where and as whom a session connects.
type target struct {
	User string
	Host string
	Port int
}

// parseTarget reads [vnc://][USER@]HOST[:PORT]. A user name may itself contain
// an at sign, so the last one separates it from the host. IPv6 hosts go in brackets.
func parseTarget(value string) (target, error) {
	var result target
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "vnc://")
	value = strings.TrimSuffix(value, "/")
	if value == "" {
		return result, E.New("no host given")
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		result.User, value = value[:at], value[at+1:]
	}
	result.Port = defaultPort
	switch {
	case strings.HasPrefix(value, "["):
		end := strings.Index(value, "]")
		if end < 0 {
			return result, E.New("unterminated IPv6 address in ", strconv.Quote(value))
		}
		result.Host = value[1:end]
		rest := value[end+1:]
		if rest != "" {
			port, err := parsePort(strings.TrimPrefix(rest, ":"), rest[0] == ':')
			if err != nil {
				return result, err
			}
			result.Port = port
		}
	case strings.Count(value, ":") == 1:
		host, portText, _ := strings.Cut(value, ":")
		port, err := parsePort(portText, true)
		if err != nil {
			return result, err
		}
		result.Host, result.Port = host, port
	default:
		result.Host = value
	}
	if result.Host == "" {
		return result, E.New("no host given")
	}
	return result, nil
}

func parsePort(text string, colon bool) (int, error) {
	if !colon {
		return 0, E.New("unexpected text after the address: ", strconv.Quote(text))
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, E.New("port ", strconv.Quote(text), " is not a number from 1 to 65535")
	}
	return port, nil
}

// Address is the host:port string to dial.
func (t target) Address() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// ID is the stable key of a saved connection and of its keyring entry.
func (t target) ID() string {
	return t.User + "@" + t.Address()
}

func (t target) String() string {
	if t.User == "" {
		return t.Address()
	}
	return t.User + "@" + t.Address()
}
