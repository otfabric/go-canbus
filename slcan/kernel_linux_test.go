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

// TestKernelSlcan attaches the Linux slcan line discipline (the driver
// slcand uses) to the slave of a pseudo-terminal: the kernel creates a
// slcanN interface whose frames it writes to, and reads from, the tty in
// slcan records. A Conn on the master plays the adapter, and frames go
// both ways between it and a SocketCAN socket on slcanN — an
// independent implementation of the protocol. Needs CAP_NET_ADMIN and the
// slcan module; runs with CANBUS_TEST_ADMIN=1 (root), and
// CANBUS_REQUIRE_SLCAN=1 turns a missing module into a failure.
func TestKernelSlcan(t *testing.T) {
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
	defer slave.Close() // removes the interface
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
