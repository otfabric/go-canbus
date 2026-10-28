// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"bytes"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leak"
)

// xlIface returns the CAN XL test interface: $CANBUS_TEST_XL_IFACE or
// vcanxl0, which must be up with the CAN XL MTU (ip link add vcanxl0 type
// vcan; ip link set vcanxl0 mtu 2060 up; Linux ≥ 6.2). Without it the
// tests skip, unless $CANBUS_REQUIRE_XL=1, where they fail.
func xlIface(t *testing.T) string {
	t.Helper()
	name := os.Getenv("CANBUS_TEST_XL_IFACE")
	if name == "" {
		name = "vcanxl0"
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil || ifi.Flags&net.FlagUp == 0 || ifi.MTU < canXLMinMTU {
		msg := "no usable " + name + " (needs a vcan interface, up, mtu 2060, Linux ≥ 6.2)"
		if os.Getenv("CANBUS_REQUIRE_XL") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return name
}

func xlFrame(prio uint16, n int) canbus.XLFrame {
	d := make([]byte, n)
	for i := range d {
		d[i] = byte(i*7 + int(prio))
	}
	return canbus.XLFrame{Priority: prio, SDT: byte(n), AF: uint32(prio) << 8, Data: d}
}

func sameXL(a, b *canbus.XLFrame) bool {
	return a.Priority == b.Priority && a.VCID == b.VCID && a.Flags == b.Flags && a.SDT == b.SDT && a.AF == b.AF && bytes.Equal(a.Data, b.Data)
}

func TestXLSendReceive(t *testing.T) {
	leak.Check(t)
	ifc := xlIface(t)
	tx := open(t, ifc, WithXL())
	rx := open(t, ifc, WithXL())
	if c := rx.Capabilities(); !c.XL || !c.FD {
		t.Fatalf("capabilities %+v", c)
	}
	ctx := ctxT(t, 5*time.Second)
	frames := []canbus.XLFrame{xlFrame(0x001, 1), xlFrame(0x123, 64), xlFrame(0x456, 65), xlFrame(0x7FF, canbus.MaxXLLen)}
	frames[1].Flags = canbus.XLSEC
	frames[2].Flags = canbus.XLSEC | canbus.XLRRS
	for i := range frames {
		if err := tx.SendXL(ctx, &frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	var g canbus.XLFrame
	for i := range frames {
		if err := rx.ReceiveXL(ctx, &g); err != nil {
			t.Fatal(err)
		}
		if !sameXL(&g, &frames[i]) || g.Interface != ifc || g.Timestamp.IsZero() || g.Echo {
			t.Fatalf("frame %d: %v, want %v", i, g, frames[i])
		}
	}
	if s := tx.Stats(); s.TxFrames != 4 || s.TxBytes != 1+64+65+2048 {
		t.Fatalf("tx stats %+v", s)
	}
}

// TestXLMixed: an XL socket receives CAN, CAN FD and CAN XL frames in
// order through ReceiveAny; Receive skips CAN XL frames; a socket without
// WithXL never sees them.
func TestXLMixed(t *testing.T) {
	leak.Check(t)
	ifc := xlIface(t)
	tx := open(t, ifc, WithXL())
	any := open(t, ifc, WithXL())
	cc := open(t, ifc, WithXL())
	plain := open(t, ifc, WithFD())
	ctx := ctxT(t, 5*time.Second)
	c1, _ := canbus.NewStandard(0x100, []byte{1})
	f1, _ := canbus.NewFD(0x101, false, true, make([]byte, 12))
	x1 := xlFrame(0x102, 100)
	c2, _ := canbus.NewStandard(0x103, nil)
	if err := tx.Send(ctx, &c1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Send(ctx, &f1); err != nil {
		t.Fatal(err)
	}
	if err := tx.SendXL(ctx, &x1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Send(ctx, &c2); err != nil {
		t.Fatal(err)
	}
	var f canbus.Frame
	var x canbus.XLFrame
	for i, want := range []string{c1.String(), f1.String(), x1.String(), c2.String()} {
		xl, err := any.ReceiveAny(ctx, &f, &x)
		if err != nil {
			t.Fatal(err)
		}
		got := f.String()
		if xl {
			got = x.String()
		}
		if got != want {
			t.Fatalf("ReceiveAny %d: %s, want %s", i, got, want)
		}
	}
	for _, r := range []*Conn{cc, plain} {
		for _, want := range []string{c1.String(), f1.String(), c2.String()} {
			if err := r.Receive(ctx, &f); err != nil || f.String() != want {
				t.Fatalf("Receive: %v %v, want %s", f, err, want)
			}
		}
	}
	if err := plain.ReceiveXL(ctx, &x); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("ReceiveXL without WithXL: %v", err)
	}
}

func TestXLEcho(t *testing.T) {
	ifc := xlIface(t)
	c := open(t, ifc, WithXL(), WithRecvOwn())
	ctx := ctxT(t, 5*time.Second)
	x := xlFrame(0x321, 9)
	if err := c.SendXL(ctx, &x); err != nil {
		t.Fatal(err)
	}
	var g canbus.XLFrame
	if err := c.ReceiveXL(ctx, &g); err != nil || !g.Echo || !sameXL(&g, &x) {
		t.Fatalf("%v %v echo=%v", g, err, g.Echo)
	}
}

// TestXLVCID: VCIDs pass through (Linux ≥ 6.9 with CAN_RAW_XL_VCID_OPTS;
// earlier kernels ignore them) and WithVCIDFilter selects them.
func TestXLVCID(t *testing.T) {
	ifc := xlIface(t)
	filtered, err := Open(ifc, WithVCIDFilter(0x42, 0xFF))
	if errors.Is(err, canbus.ErrUnsupported) {
		t.Skipf("kernel without CAN XL VCID options (Linux < 6.9): %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer filtered.Close()
	tx := open(t, ifc, WithXL())
	all := open(t, ifc, WithXL())
	ctx := ctxT(t, 5*time.Second)
	a, b := xlFrame(0x200, 3), xlFrame(0x201, 4)
	a.VCID, b.VCID = 0x42, 0x07
	for _, f := range []*canbus.XLFrame{&a, &b} {
		if err := tx.SendXL(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	var g canbus.XLFrame
	for _, want := range []*canbus.XLFrame{&a, &b} {
		if err := all.ReceiveXL(ctx, &g); err != nil || !sameXL(&g, want) {
			t.Fatalf("all: %v %v, want %v", g, err, want)
		}
	}
	if err := filtered.ReceiveXL(ctx, &g); err != nil || !sameXL(&g, &a) {
		t.Fatalf("filtered: %v %v, want %v", g, err, a)
	}
	short := ctxT(t, 200*time.Millisecond)
	if err := filtered.ReceiveXL(short, &g); err == nil {
		t.Fatalf("filter let %v through", g)
	}
}

func TestXLErrors(t *testing.T) {
	ifc := xlIface(t)
	ctx := ctxT(t, 5*time.Second)
	fd := open(t, ifc, WithFD())
	x := xlFrame(0x10, 2)
	if err := fd.SendXL(ctx, &x); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("SendXL without WithXL: %v", err)
	}
	xl := open(t, ifc, WithXL())
	bad := canbus.XLFrame{Priority: 0x800, Data: []byte{1}}
	if err := xl.SendXL(ctx, &bad); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatalf("invalid frame: %v", err)
	}
	all := open(t, "any", WithXL())
	if err := all.SendXL(ctx, &x); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("SendXL on any: %v", err)
	}
	_ = xl.Close()
	if err := xl.SendXL(ctx, &x); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("SendXL after Close: %v", err)
	}
	// An interface with the CAN FD MTU cannot carry CAN XL.
	if ifi, err := net.InterfaceByName("vcan0"); err == nil && ifi.MTU < canXLMinMTU {
		if _, err := Open("vcan0", WithXL()); !errors.Is(err, canbus.ErrUnsupported) {
			t.Fatalf("WithXL on vcan0 (mtu %d): %v", ifi.MTU, err)
		}
	}
}

// TestXLKernelFilterParity checks that FilterSet.MatchXL decides as the
// kernel's CAN_RAW_FILTER does on CAN XL frames: every filter set is
// installed on a socket, and the frames it receives are compared with the
// software decision. VCIDs are included where the kernel passes them
// (Linux ≥ 6.9).
func TestXLKernelFilterParity(t *testing.T) {
	ifc := xlIface(t)
	vcids := []uint8{0}
	if c, err := Open(ifc, WithVCIDFilter(0, 0)); err == nil {
		c.Close()
		vcids = append(vcids, 5)
	}
	var frames []canbus.XLFrame
	for _, v := range vcids {
		for _, p := range []uint16{0x5A2, 0x5A3} {
			x := xlFrame(p, 2)
			x.VCID = v
			frames = append(frames, x)
		}
	}
	sets := []canbus.FilterSet{
		{Filters: []canbus.Filter{canbus.ExactStandard(0x5A2)}},
		{Filters: []canbus.Filter{{ID: 0x5A2, Mask: 0x7FF}}},
		{Filters: []canbus.Filter{{ID: 0x5A2, Mask: 0x7FE}}},
		{Filters: []canbus.Filter{{ID: 0x5A2, Mask: 0x7FF, Invert: true}, {ID: 0x5A0, Mask: 0x7F0}}, Join: true},
		{Filters: []canbus.Filter{{ID: 0x505A2, Mask: 0xFF07FF}}},
		{Filters: []canbus.Filter{{ID: 0x5A2, Mask: 0x7FF, Format: canbus.FormatExtended}, {ID: 0x5A3, Mask: 0x7FF}}},
		{Filters: []canbus.Filter{{ID: 0x5A2, Mask: 0x7FF, Remote: canbus.RemoteOnly}, {ID: 0x5A2, Mask: 0x7FF, Remote: canbus.RemoteDataOnly}}},
	}
	tx := open(t, ifc, WithXL(), WithVCIDFilter(0, 0))
	if len(vcids) == 1 {
		tx = open(t, ifc, WithXL())
	}
	for i, fs := range sets {
		opts := []Option{WithXL(), WithFilters(fs)}
		if len(vcids) > 1 {
			opts = append(opts, WithVCIDFilter(0, 0))
		}
		rx := open(t, ifc, opts...)
		ctx := ctxT(t, 5*time.Second)
		for j := range frames {
			if err := tx.SendXL(ctx, &frames[j]); err != nil {
				t.Fatal(err)
			}
		}
		// Frames of other users of the interface are ignored by priority.
		var want []string
		for j := range frames {
			if fs.MatchXL(&frames[j]) {
				want = append(want, frames[j].String())
			}
		}
		var got []string
		for {
			short := ctxT(t, 300*time.Millisecond)
			var g canbus.XLFrame
			if err := rx.ReceiveXL(short, &g); err != nil {
				break
			}
			if g.Priority&0x7F0 == 0x5A0 {
				got = append(got, g.String())
			}
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("set %d %v: kernel delivered %v, MatchXL selects %v", i, fs, got, want)
		}
		rx.Close()
	}
}

// TestXLBus runs a canbus.Bus on CAN XL sockets: ReceiveAny feeds the
// subscriptions.
func TestXLBus(t *testing.T) {
	leak.Check(t)
	ifc := xlIface(t)
	tx, err := canbus.NewBus(open(t, ifc, WithXL()))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	rx, _ := canbus.NewBus(open(t, ifc, WithXL()))
	defer rx.Close()
	s, _ := rx.Subscribe(canbus.WithXL(), canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{ID: 0x5B0, Mask: 0x7F0}}}))
	ctx := ctxT(t, 5*time.Second)
	want := []canbus.XLFrame{xlFrame(0x5B1, 2048), xlFrame(0x5B2, 1)}
	for i := range want {
		if err := tx.SendXL(ctx, &want[i]); err != nil {
			t.Fatal(err)
		}
	}
	fd, _ := canbus.NewFD(0x5B3, false, true, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	if err := tx.Send(ctx, &fd); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		g, err := s.ReceiveXL(ctx)
		if err != nil || !sameXL(&g, &want[i]) || g.Interface != ifc || g.Timestamp.IsZero() {
			t.Fatalf("frame %d: %v %v", i, g, err)
		}
	}
	if g, err := s.Receive(ctx); err != nil || g.String() != fd.String() {
		t.Fatalf("CAN FD frame: %v %v", g, err)
	}
}
