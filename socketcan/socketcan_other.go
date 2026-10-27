// SPDX-License-Identifier: MIT

//go:build !(linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64))

package socketcan

import (
	"context"
	"fmt"
	"runtime"

	"github.com/otfabric/go-canbus"
)

// Conn is a SocketCAN raw socket. On this system it cannot be opened.
type Conn struct{}

// Open reports [canbus.ErrUnsupported]: SocketCAN exists only on Linux.
func Open(ifname string, opts ...Option) (*Conn, error) {
	return nil, &canbus.OpError{Op: "open", Interface: ifname,
		Err: fmt.Errorf("%w: SocketCAN is not available on %s/%s", canbus.ErrUnsupported, runtime.GOOS, runtime.GOARCH)}
}

// Send reports [canbus.ErrUnsupported].
func (c *Conn) Send(context.Context, *canbus.Frame) error { return canbus.ErrUnsupported }

// Receive reports [canbus.ErrUnsupported].
func (c *Conn) Receive(context.Context, *canbus.Frame) error { return canbus.ErrUnsupported }

// Close does nothing.
func (c *Conn) Close() error { return nil }

// Supported reports whether SocketCAN is available on this platform.
func Supported() bool { return false }
