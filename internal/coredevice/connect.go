package coredevice

import (
	"context"
	"log/slog"
	"net"
	"net/netip"

	"github.com/google/uuid"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/pairing"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/tlspsk"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/tunnel"
	E "github.com/sagernet/sing/common/exceptions"
)

type RemotePairingOptions struct {
	Address netip.AddrPort
	Record  *pairing.Record
	Logger  *slog.Logger
}

var ErrLoopbackAddress = E.New("remote pairing through a loopback address is refused: " +
	"it leaves the device's remotepairingd unable to set up tunnels until Developer Mode is turned off and on again; " +
	"use the VPN's loopback address (such as 10.7.0.1) instead")

func ConnectRemotePairing(ctx context.Context, options RemotePairingOptions) (*Device, error) {
	if address := options.Address.Addr().Unmap(); address.IsLoopback() || address.IsUnspecified() {
		return nil, ErrLoopbackAddress
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", options.Address.String())
	if err != nil {
		return nil, E.Cause(err, "connect to remote pairing")
	}
	session, err := pairing.Verify(ctx, conn, options.Record, logger)
	if err != nil {
		return nil, err
	}
	port, err := session.CreateListener(ctx)
	if err != nil {
		session.Close()
		return nil, err
	}
	logger.Debug("remote pairing listener", slog.Int("port", int(port)))
	tunnelConn, err := dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(options.Address.Addr(), port).String())
	if err != nil {
		session.Close()
		return nil, E.Cause(err, "connect to tunnel port")
	}
	secureConn, err := tlspsk.Client(ctx, tunnelConn, session.Key())
	if err != nil {
		session.Close()
		return nil, E.Cause(err, "tunnel TLS")
	}
	tun, err := tunnel.Open(ctx, secureConn, logger)
	if err != nil {
		session.Close()
		return nil, err
	}
	return newDevice(ctx, tun, session, handshakeIdentity(options.Record.Identifier), logger)
}

type LockdownOptions struct {
	Address netip.Addr
	Record  *lockdown.PairRecord
	Logger  *slog.Logger
}

func ConnectLockdown(ctx context.Context, options LockdownOptions) (*Device, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	dial := func(ctx context.Context, port uint16) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(options.Address, port).String())
	}
	identity := handshakeIdentity(options.Record.HostID)
	return connectLockdown(ctx, options.Record, dial, identity, logger)
}

func connectLockdown(ctx context.Context, record *lockdown.PairRecord, dial lockdown.Dialer, identity uuid.UUID, logger *slog.Logger) (*Device, error) {
	conn, err := lockdown.DialCoreDeviceProxy(ctx, record, dial, logger)
	if err != nil {
		return nil, err
	}
	tun, err := tunnel.Open(ctx, conn, logger)
	if err != nil {
		return nil, err
	}
	return newDevice(ctx, tun, nil, identity, logger)
}

// pymobiledevice3 sends a stable host UUID in RSD handshakes
// (pymobiledevice3 9c33842, pymobiledevice3/remote/remotexpc.py:82-110).
func handshakeIdentity(name string) uuid.UUID {
	identity, err := uuid.Parse(name)
	if err != nil {
		identity = uuid.NewSHA1(uuid.NameSpaceOID, []byte("open-screen-sharing:"+name))
	}
	return identity
}
