//go:build darwin && !ios

package coredevice

import (
	"context"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
)

func ConnectUSBMux(ctx context.Context, udid string, logger *slog.Logger) (*Device, error) {
	// pymobiledevice3 uses its stable host ID for RSD on a tunnel of its own
	// (pymobiledevice3 9c33842, pymobiledevice3/remote/remotexpc.py:100-110).
	hostName, _ := os.Hostname()
	identity := uuid.NewSHA1(uuid.NameSpaceOID, []byte("open-screen-sharing:usbmux:"+hostName))
	record, dial, err := lockdown.OpenUSBMux(ctx, udid)
	if err != nil {
		return nil, err
	}
	return connectLockdown(ctx, record, dial, identity, logger)
}
