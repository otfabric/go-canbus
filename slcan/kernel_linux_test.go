// SPDX-License-Identifier: MIT

package slcan

import (
	"bytes"
	"context"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
	"github.com/otfabric/go-canbus/socketcan/link"
)

const (
	nSLCAN     = 17     // N_SLCAN line discipline
	siocgifnam = 0x8910 // SIOCGIFNAME on a tty: the netdev of its line discipline
)

// kernelSlcan attaches the slcan line discipline to a pseudo-terminal's
// slave and brings the interface up. It returns the master — the
// adapter's side — and the interface name.
func kernelSlcan(t *testing.T) (*os.File, string) {
	t.Helper()
	if os.Getenv("CANBUS_TEST_ADMIN") != "1" {
		t.Skip("set CANBUS_TEST_ADMIN=1 and run as root (and modprobe slcan)")
	}
	m, path, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	ld := int32(nSLCAN)
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TIOCSETD, uintptr(unsafe.Pointer(&ld))); e != 0 {
		m.Close()
		slave.Close()
		if os.Getenv("CANBUS_REQUIRE_SLCAN") == "1" {
			t.Fatalf("TIOCSETD N_SLCAN: %v (is the slcan module available?)", e)
		}
		t.Skipf("no slcan line discipline: %v (modprobe slcan)", e)
	}
	t.Cleanup(func() { slave.Close() }) // removes the interface
	var name [16]byte
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), siocgifnam, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		m.Close()
		t.Fatalf("SIOCGIFNAME: %v", e)
	}
	ifc := string(name[:bytes.IndexByte(name[:], 0)])
	if err := link.SetUp(ifc); err != nil {
		m.Close()
		t.Fatalf("set %s up: %v", ifc, err)
	}
	return m, ifc
}

// TestKernelSlcan attaches the Linux slcan line discipline (the driver
// slcand uses) to the slave of a pseudo-terminal: the kernel creates a
// slcanN interface whose frames it writes to, and reads from, the tty in
// slcan records. A Conn on the master plays the adapter, and frames go
// both ways between it and a SocketCAN socket on slcanN — an
// independent implementation of the protocol. Needs CAP_NET_ADMIN and the
// slcan module; runs with CANBUS_TEST_ADMIN=1 (root), and
// CANBUS_REQUIRE_SLCAN=1 turns a missing module into a failure.
func TestKernelSlcan(t *testing.T) {
	m, ifc := kernelSlcan(t)
	dev, err := New(m, Config{NoCommands: true, Name: "adapter"})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	sock, err := socketcan.Open(ifc)
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	ctx := ctxT(t)
	must := mustFrame(t)
	frames := []canbus.Frame{
		must(canbus.NewStandard(0x123, []byte{0xA1, 0xB2})),
		must(canbus.NewStandard(0, nil)),
		must(canbus.NewExtended(0x18DAF110, []byte{1, 2, 3, 4, 5, 6, 7, 8})),
		must(canbus.NewRemote(0x7DF, false, 8)),
		must(canbus.NewRemote(0x1FFFFFFF, true, 0)),
	}
	// Kernel → adapter: the kernel writes slcan records to the tty.
	for i := range frames {
		if err := sock.Send(ctx, &frames[i]); err != nil {
			t.Fatal(err)
		}
		var g canbus.Frame
		if err := dev.Receive(ctx, &g); err != nil || g.String() != frames[i].String() || g.Flags != frames[i].Flags {
			t.Fatalf("kernel → go %d: %v %v, want %v", i, g, err, frames[i])
		}
	}
	// Adapter → kernel: the kernel parses the records go-canbus writes.
	for i := range frames {
		if err := dev.Send(ctx, &frames[i]); err != nil {
			t.Fatal(err)
		}
		var g canbus.Frame
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := sock.Receive(rctx, &g)
		cancel()
		if err != nil || g.String() != frames[i].String() || g.Flags != frames[i].Flags {
			t.Fatalf("go → kernel %d: %v %v, want %v", i, g, err, frames[i])
		}
	}
}

// TestKernelSlcanStatus writes error ("e") and state ("s") records to the
// Linux slcan driver and checks that the error frames it delivers on
// slcanN are those go-canbus decodes from the same records — or that
// neither delivers one. Linux ignores records of four characters or
// fewer (such as "e1a"), so all records here are longer.
func TestKernelSlcanStatus(t *testing.T) {
	m, ifc := kernelSlcan(t)
	defer m.Close()
	sock, err := socketcan.Open(ifc)
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	if err := sock.SetFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll}); err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	state := canbus.StateActive
	for _, rec := range []string{"e3bcO", "e2fsZ", "e5abBcs", "e1oZZ", "e2Oa0",
		"sw100050", "sp127127", "sp000130", "sw000100", "sp000300", "sa000000", "sa001002", "sb256256"} {
		var want canbus.Frame
		var ok bool
		if rec[0] == 'e' {
			ok, err = ParseErrorRecord([]byte(rec), &want)
		} else {
			var st canbus.State
			var tx, rx int
			st, tx, rx, ok, err = ParseStateRecord([]byte(rec))
			if err == nil {
				want, ok = StateFrame(state, st, tx, rx)
				state = st
			}
		}
		if err != nil {
			t.Fatalf("%s: %v", rec, err)
		}
		if _, err := m.Write([]byte(rec + "\r")); err != nil {
			t.Fatal(err)
		}
		var g canbus.Frame
		rctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := sock.Receive(rctx, &g)
		cancel()
		switch {
		case ok && err != nil:
			t.Fatalf("%s: Linux delivered nothing (%v), go-canbus %v", rec, err, want)
		case !ok && err == nil:
			t.Fatalf("%s: Linux delivered %v, go-canbus nothing", rec, g)
		case ok && !sameErr(g, want):
			t.Fatalf("%s: Linux % X class %v, go-canbus % X class %v", rec, g.Data, canbus.ErrorClass(g.ID), want.Data, canbus.ErrorClass(want.ID))
		}
		t.Logf("%-8s → %v", rec, g)
	}
}
