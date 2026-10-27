// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/isotp"
)

func kernelISOTP(t *testing.T, ifc string, cfg isotp.Config) *ISOTPConn {
	t.Helper()
	c, err := DialISOTP(ifc, cfg)
	if errors.Is(err, syscall.EPROTONOSUPPORT) && os.Getenv("CANBUS_REQUIRE_ISOTP") != "1" {
		// Some kernels (GitHub's Azure runner kernel) do not ship can-isotp.
		t.Skip("kernel ISO-TP (can-isotp) is not available; CANBUS_REQUIRE_ISOTP=1 makes this a failure")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func userISOTP(t *testing.T, ifc string, cfg isotp.Config) *isotp.Conn {
	t.Helper()
	opts := []Option{}
	if cfg.FD {
		opts = append(opts, WithFD())
	}
	sock, err := Open(ifc, opts...)
	if err != nil {
		t.Fatal(err)
	}
	bus, _ := canbus.NewBus(sock)
	c, err := isotp.Dial(bus, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); bus.Close() })
	return c
}

func pattern(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i % 251)
	}
	return p
}

func transfer(t *testing.T, from, to isotp.Endpoint, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	want := pattern(n)
	errc := make(chan error, 1)
	go func() { errc <- from.Send(ctx, want) }()
	got, err := to.Receive(ctx)
	if err != nil {
		t.Fatalf("%d bytes: receive: %v", n, err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("%d bytes: send: %v", n, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%d bytes: payload differs (got %d bytes)", n, len(got))
	}
}

// TestKernelVersusUserspace exchanges messages between the Linux kernel's
// ISO-TP implementation and this package's, in both directions.
func TestKernelVersusUserspace(t *testing.T) {
	ifc := testIface(t)
	modes := []struct {
		name string
		mod  func(a, b *isotp.Config)
	}{
		{"normal", nil},
		{"29-bit", func(a, b *isotp.Config) {
			a.ExtendedIDs, b.ExtendedIDs = true, true
		}},
		{"extended-addressing", func(a, b *isotp.Config) { a.Addressing, b.Addressing = isotp.Extended, isotp.Extended }},
		{"mixed-addressing", func(a, b *isotp.Config) { a.Addressing, b.Addressing = isotp.Mixed, isotp.Mixed }},
		{"padding", func(a, b *isotp.Config) { a.Padding, b.Padding = true, true }},
		{"bs-stmin", func(a, b *isotp.Config) {
			a.BlockSize, a.STmin, b.BlockSize, b.STmin = 4, time.Millisecond, 2, 200*time.Microsecond
		}},
		{"fd", func(a, b *isotp.Config) { a.FD, b.FD = true, true }},
		{"fd-txdl16", func(a, b *isotp.Config) { a.FD, b.FD, a.TxDL, b.TxDL = true, true, 16, 16 }},
	}
	for i, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			base := canbus.ID(0x600 + 2*i)
			ka := isotp.Config{TxID: base, RxID: base + 1, TxAddress: 0xA1, RxAddress: 0xB2, MaxMessageSize: 8000}
			ub := isotp.Config{TxID: base + 1, RxID: base, TxAddress: 0xB2, RxAddress: 0xA1, MaxMessageSize: 8000}
			if m.mod != nil {
				m.mod(&ka, &ub)
			}
			if ka.ExtendedIDs {
				ka.TxID, ka.RxID, ub.TxID, ub.RxID = 0x18DA0000|base, 0x18DA0000|base+1, 0x18DA0000|base+1, 0x18DA0000|base
			}
			k := kernelISOTP(t, ifc, ka)
			u := userISOTP(t, ifc, ub)
			for _, n := range []int{1, 7, 8, 62, 100, 4095, 4096, 8000} {
				transfer(t, k, u, n)
				transfer(t, u, k, n)
			}
		})
	}
}

func TestKernelISOTPErrors(t *testing.T) {
	ifc := testIface(t)
	// No receiver: the kernel reports N_Bs as ETIMEDOUT.
	k := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6F0, RxID: 0x6F1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := k.Send(ctx, pattern(100)); !errors.Is(err, isotp.ErrTimeout) {
		t.Fatal("N_Bs:", err)
	}
	// The userspace receiver refuses a message beyond its limit: the
	// kernel sender sees the OVERFLOW flow control.
	k2 := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6F2, RxID: 0x6F3})
	u := userISOTP(t, ifc, isotp.Config{TxID: 0x6F3, RxID: 0x6F2, MaxMessageSize: 100})
	if err := k2.Send(ctx, pattern(500)); !errors.Is(err, isotp.ErrOverflow) {
		t.Fatal("overflow:", err)
	}
	if _, err := u.Receive(ctx); !errors.Is(err, isotp.ErrTooLarge) {
		t.Fatal(err)
	}
	// And the reverse: the kernel receiver refuses.
	k3 := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6F4, RxID: 0x6F5, MaxMessageSize: 4095})
	u3 := userISOTP(t, ifc, isotp.Config{TxID: 0x6F5, RxID: 0x6F4, MaxMessageSize: 1 << 20})
	_ = k3
	err := u3.Send(ctx, pattern(1<<17))
	t.Logf("userspace sender, kernel receiver, 128 KiB: %v", err)
	if err == nil {
		t.Fatal("kernel accepted a message beyond its buffer")
	}
	short, c2 := context.WithTimeout(ctx, 20*time.Millisecond)
	defer c2()
	if _, err := k3.Receive(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("kernel receive after refusal: %v", err)
	}
	if _, err := DialISOTP(ifc, isotp.Config{SendFlowWait: 1}); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := DialISOTP("nosuchcan0", isotp.Config{}); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
	if _, err := DialISOTP(ifc, isotp.Config{STmin: time.Hour}); err == nil {
		t.Fatal("bad STmin")
	}
	k.Close()
	if err := k.Send(ctx, pattern(1)); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := k.Receive(ctx); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	_ = fmt.Sprint
}
