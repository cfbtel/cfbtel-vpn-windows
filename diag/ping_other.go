//go:build !windows

package diag

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// Ping is only available on Windows.
func Ping(context.Context, netip.Addr) (time.Duration, error) {
	return 0, errors.New("ping is only available on Windows")
}
