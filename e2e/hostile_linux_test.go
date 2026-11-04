// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package e2e

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/isotp"
	"github.com/otfabric/go-canbus/socketcan"
)

// Identifiers of the hostile-peer tests, apart from the vehicle scenario.
const (
	hostileTx = 0x6A0 // what the endpoint under test sends
	hostileRx = 0x6A8 // what the hostile peer sends
)

// rawPeer opens a CAN_RAW socket that plays a hostile ISO-TP peer.
func rawPeer(t *testing.T, ifc string) *socketcan.Conn {
	t.Helper()
	p, err := socketcan.Open(ifc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func peerFrame(t *testing.T, p *socketcan.Conn, data ...byte) {
	t.Helper()
	f, err := canbus.NewStandard(hostileRx, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

// fromEndpoint returns the next frame the endpoint under test sent.
func fromEndpoint(ctx context.Context, t *testing.T, p *socketcan.Conn) canbus.Frame {
	t.Helper()
	for {
		var f canbus.Frame
		if err := p.Receive(ctx, &f); err != nil {
			t.Fatal(err)
		}
		if f.ID == hostileTx && !f.IsError() {
			return f
		}
	}
}

func kernelISOTP(t *testing.T, ifc string) *socketcan.ISOTPConn {
	t.Helper()
	k, err := socketcan.DialISOTP(ifc, isotp.Config{TxID: hostileTx, RxID: hostileRx})
	if errors.Is(err, syscall.EPROTONOSUPPORT) && os.Getenv("CANBUS_REQUIRE_ISOTP") != "1" {
		t.Skip("kernel ISO-TP not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

// TestHostilePeerUserspaceISOTPVCAN: the userspace ISO-TP receiver on a
// SocketCAN bus refuses a first frame that announces 2^32-1 bytes with
// an OVERFLOW flow control on the wire, and a sender gives up on a WAIT
// flood.
func TestHostilePeerUserspaceISOTPVCAN(t *testing.T) {
	ifc := vcan(t)
	leakcheck.Check(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := rawPeer(t, ifc)
	tr, err := socketcan.Open(ifc)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := canbus.NewBus(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	c, err := isotp.Dial(bus, isotp.Config{TxID: hostileTx, RxID: hostileRx, MaxWait: 2, TimeoutBs: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	peerFrame(t, p, 0x10, 0x00, 0xFF, 0xFF, 0xFF, 0xFF, 1, 2)
	if f := fromEndpoint(ctx, t, p); f.Data[0] != 0x32 {
		t.Fatalf("flow control % X, want OVERFLOW", f.Payload())
	}
	if _, err := c.Receive(ctx); !errors.Is(err, isotp.ErrTooLarge) {
		t.Fatal(err)
	}

	errc := make(chan error, 1)
	go func() { errc <- c.Send(ctx, make([]byte, 100)) }()
	if f := fromEndpoint(ctx, t, p); f.Data[0]>>4 != 1 {
		t.Fatalf("first frame expected, got % X", f.Payload())
	}
	for range 50 {
		peerFrame(t, p, 0x31, 0, 0)
	}
	if err := <-errc; !errors.Is(err, isotp.ErrWaitLimit) {
		t.Fatal(err)
	}
}

// TestHostilePeerKernelISOTPSender: the kernel's ISO-TP sender facing an
// OVERFLOW and a reserved flow status. OVERFLOW maps to ErrOverflow. A
// reserved flow status makes the kernel (net/can/isotp.c, isotp_rcv_fc,
// default case) end the transfer without setting an error: Send returns
// nil although the message was not delivered (seen on Linux 7.0). The
// test pins that Send returns rather than its error.
func TestHostilePeerKernelISOTPSender(t *testing.T) {
	ifc := vcan(t)
	leakcheck.Check(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := rawPeer(t, ifc)
	k := kernelISOTP(t, ifc)
	send := func(fc byte) error {
		errc := make(chan error, 1)
		go func() { errc <- k.Send(ctx, make([]byte, 100)) }()
		if f := fromEndpoint(ctx, t, p); f.Data[0]>>4 != 1 {
			t.Fatalf("first frame expected, got % X", f.Payload())
		}
		peerFrame(t, p, fc, 0, 0)
		return <-errc
	}
	if err := send(0x32); !errors.Is(err, isotp.ErrOverflow) {
		t.Fatalf("OVERFLOW: %v", err)
	}
	if err := send(0x37); err != nil && !errors.Is(err, isotp.ErrProtocol) {
		t.Fatalf("reserved flow status: %v", err)
	}
}

// TestHostilePeerKernelISOTPReceiver: the kernel's ISO-TP receiver facing
// a wrong sequence number and first frames beyond its buffer; it keeps
// working afterwards.
func TestHostilePeerKernelISOTPReceiver(t *testing.T) {
	ifc := vcan(t)
	leakcheck.Check(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := rawPeer(t, ifc)
	k := kernelISOTP(t, ifc)

	peerFrame(t, p, 0x10, 0x14, 0, 1, 2, 3, 4, 5)
	if f := fromEndpoint(ctx, t, p); f.Data[0] != 0x30 {
		t.Fatalf("flow control % X, want CTS", f.Payload())
	}
	peerFrame(t, p, 0x25, 6, 7, 8, 9, 10, 11, 12)
	if _, err := k.Receive(ctx); !errors.Is(err, isotp.ErrSequence) {
		t.Fatal("wrong SN:", err)
	}

	// 65536 bytes, beyond the kernel's max_pdu_size (8300 by default):
	// OVERFLOW.
	peerFrame(t, p, 0x10, 0x00, 0x00, 0x01, 0x00, 0x00, 1, 2)
	if f := fromEndpoint(ctx, t, p); f.Data[0] != 0x32 {
		t.Fatalf("flow control % X, want OVERFLOW", f.Payload())
	}
	// 2^32-1 bytes: the kernel's u32 length check wraps and it ignores the
	// frame without a flow control. It must at least not accept it.
	peerFrame(t, p, 0x10, 0x00, 0xFF, 0xFF, 0xFF, 0xFF, 1, 2)
	quiet, cancelQuiet := context.WithTimeout(ctx, 300*time.Millisecond)
	for {
		var f canbus.Frame
		if err := p.Receive(quiet, &f); err != nil {
			break
		}
		if f.ID == hostileTx && f.Data[0] != 0x32 {
			t.Fatalf("2^32-1 byte first frame answered % X", f.Payload())
		}
	}
	cancelQuiet()

	peerFrame(t, p, 0x02, 0xAB, 0xCD)
	for {
		got, err := k.Receive(ctx)
		if err != nil {
			// The kernel may report the refused first frame.
			if errors.Is(err, isotp.ErrOverflow) || errors.Is(err, isotp.ErrTooLarge) {
				continue
			}
			t.Fatal(err)
		}
		if len(got) != 2 || got[0] != 0xAB || got[1] != 0xCD {
			t.Fatalf("% X", got)
		}
		return
	}
}
