// SPDX-License-Identifier: MIT

//go:build !(linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64))

package socketcan

import (
	"context"
	"fmt"
	"runtime"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/isotp"
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

// SendXL reports [canbus.ErrUnsupported].
func (c *Conn) SendXL(context.Context, *canbus.XLFrame) error { return canbus.ErrUnsupported }

// ReceiveXL reports [canbus.ErrUnsupported].
func (c *Conn) ReceiveXL(context.Context, *canbus.XLFrame) error { return canbus.ErrUnsupported }

// ReceiveAny reports [canbus.ErrUnsupported].
func (c *Conn) ReceiveAny(context.Context, *canbus.Frame, *canbus.XLFrame) (bool, error) {
	return false, canbus.ErrUnsupported
}

// Close does nothing.
func (c *Conn) Close() error { return nil }

// Supported reports whether SocketCAN is available on this platform.
func Supported() bool { return false }

// ISOTPConn is a kernel ISO-TP socket. On this system it cannot be opened.
type ISOTPConn struct{}

// DialISOTP reports [canbus.ErrUnsupported].
func DialISOTP(ifname string, cfg isotp.Config) (*ISOTPConn, error) {
	return nil, &canbus.OpError{Op: "open isotp", Interface: ifname,
		Err: fmt.Errorf("%w: kernel ISO-TP is not available on %s/%s", canbus.ErrUnsupported, runtime.GOOS, runtime.GOARCH)}
}

// Send reports [canbus.ErrUnsupported].
func (c *ISOTPConn) Send(context.Context, []byte) error { return canbus.ErrUnsupported }

// Receive reports [canbus.ErrUnsupported].
func (c *ISOTPConn) Receive(context.Context) ([]byte, error) { return nil, canbus.ErrUnsupported }

// Close does nothing.
func (c *ISOTPConn) Close() error { return nil }
