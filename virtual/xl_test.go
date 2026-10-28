// SPDX-License-Identifier: MIT

package virtual

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leak"
)

var _ canbus.XLTransport = (*Port)(nil)

func xl(t *testing.T, s string) canbus.XLFrame {
	t.Helper()
	x, err := canbus.ParseXLFrame(s)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// recvAny returns the next frame of any kind as can-utils text, or "" on
// timeout.
func recvAny(t *testing.T, p *Port, d time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var f canbus.Frame
	var x canbus.XLFrame
	isXL, err := p.ReceiveAny(ctx, &f, &x)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ""
	case err != nil:
		t.Fatal(err)
	case isXL:
		return x.String()
	}
	return f.String()
}

func TestXLDeliveryRules(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	b := New(Config{Name: "vxl"})
	defer b.Close()
	tx, _ := b.Open(WithXL(), WithRecvOwn())
	rx, _ := b.Open(WithXL())
	fd, _ := b.Open(WithFD())
	vc, _ := b.Open(WithVCIDFilter(5, 0xFF))
	flt, _ := b.Open(WithXL(), WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(0x242)}}))

	if !tx.Capabilities().XL || fd.Capabilities().XL || !tx.Capabilities().FD {
		t.Fatalf("capabilities %+v %+v", tx.Capabilities(), fd.Capabilities())
	}
	a := xl(t, "05242#81:01:12345678#AABBCCDDEE")
	c := xl(t, "00243#80:00:00000000#11")
	for _, x := range []canbus.XLFrame{a, c} {
		if err := tx.SendXL(ctx, &x); err != nil {
			t.Fatal(err)
		}
	}
	cl, _ := canbus.NewFD(0x10, false, false, []byte{1, 2, 3})
	if err := tx.Send(ctx, &cl); err != nil {
		t.Fatal(err)
	}
	// The sender gets its own frames back, marked as echo.
	var own canbus.XLFrame
	if err := tx.ReceiveXL(ctx, &own); err != nil || !own.Echo || own.String() != a.String() || own.Interface != "vxl" || own.Timestamp.IsZero() {
		t.Fatalf("own %v echo=%v %q %v", own, own.Echo, own.Interface, err)
	}
	// rx sees everything in order.
	for _, want := range []string{a.String(), c.String(), cl.String()} {
		if got := recvAny(t, rx, time.Second); got != want {
			t.Fatalf("rx got %q, want %q", got, want)
		}
	}
	// A CAN FD port never sees CAN XL.
	if got := recvAny(t, fd, time.Second); got != cl.String() {
		t.Fatalf("fd port got %q", got)
	}
	// The VCID filter passes VCID 5 only (and every CAN FD frame).
	if got := recvAny(t, vc, time.Second); got != a.String() {
		t.Fatalf("vcid port got %q", got)
	}
	if got := recvAny(t, vc, time.Second); got != cl.String() {
		t.Fatalf("vcid port got %q", got)
	}
	// CAN_RAW_FILTER semantics: 0x242 selects priority 0x242 of any VCID.
	if got := recvAny(t, flt, time.Second); got != a.String() {
		t.Fatalf("filtered port got %q", got)
	}
	if got := recvAny(t, flt, 50*time.Millisecond); got != "" {
		t.Fatalf("filtered port got %q", got)
	}
	// Receive skips CAN XL frames.
	_ = tx.SendXL(ctx, &c)
	_ = tx.Send(ctx, &cl)
	if f, err := recv(t, rx, time.Second); err != nil || f.String() != cl.String() {
		t.Fatalf("Receive %v %v", f, err)
	}
	if st := rx.Stats(); st.RxFrames != 5 || st.RxBytes != 5+1+3+1+3 {
		t.Fatalf("rx stats %+v", st)
	}
	if st := tx.Stats(); st.TxFrames != 5 || st.TxBytes != 5+1+3+1+3 {
		t.Fatalf("tx stats %+v", st)
	}
}

func TestXLPayloadIsCopied(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	tx, _ := b.Open(WithXL())
	r1, _ := b.Open(WithXL())
	r2, _ := b.Open(WithXL())
	x := canbus.XLFrame{Priority: 1, Data: bytes.Repeat([]byte{0xAA}, 2048)}
	if err := tx.SendXL(ctx, &x); err != nil {
		t.Fatal(err)
	}
	x.Data[0] = 0 // the bus took a copy
	var g1, g2 canbus.XLFrame
	if err := r1.ReceiveXL(ctx, &g1); err != nil {
		t.Fatal(err)
	}
	g1.Data[1] = 0 // a receiver's copy is its own
	if err := r2.ReceiveXL(ctx, &g2); err != nil {
		t.Fatal(err)
	}
	if g1.Data[0] != 0xAA || g2.Data[0] != 0xAA || g2.Data[1] != 0xAA || len(g2.Data) != 2048 {
		t.Fatal("payload shared")
	}
	// Storage is reused.
	buf := make([]byte, 0, 4096)
	g1.Data = buf
	_ = tx.SendXL(ctx, &canbus.XLFrame{Data: []byte{1}})
	if err := r1.ReceiveXL(ctx, &g1); err != nil || &g1.Data[0] != &buf[:1][0] {
		t.Fatalf("storage not reused: %v", err)
	}
}

func TestXLUnsupportedAndInvalid(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	p, _ := b.Open(WithFD())
	x := xl(t, "242#80:00:00000000#11")
	if err := p.SendXL(ctx, &x); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("SendXL on FD port: %v", err)
	}
	if err := p.ReceiveXL(ctx, &x); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("ReceiveXL on FD port: %v", err)
	}
	q, _ := b.Open(WithXL())
	if err := q.SendXL(ctx, &canbus.XLFrame{}); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatalf("empty frame: %v", err)
	}
	if err := b.InjectXL(ctx, canbus.XLFrame{Priority: 0x800, Data: []byte{1}}); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatalf("bad priority: %v", err)
	}
	_ = q.Close()
	if err := q.SendXL(ctx, &x); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
	if _, err := q.ReceiveAny(ctx, &canbus.Frame{}, &x); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("closed receive: %v", err)
	}
}

func TestXLInjectAndFaults(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	b := New(Config{Seed: 7})
	defer b.Close()
	rx, _ := b.Open(WithXL())
	if err := b.InjectXL(ctx, xl(t, "123#80:00:00000000#0102")); err != nil {
		t.Fatal(err)
	}
	if got := recvAny(t, rx, time.Second); got != "00123#80:00:00000000#0102" {
		t.Fatalf("injected %q", got)
	}
	// Corruption flips exactly one payload bit anywhere in a long payload.
	b.SetFaults(Faults{Corrupt: 1, Duplicate: 1})
	orig := bytes.Repeat([]byte{0}, 1500)
	if err := b.InjectXL(ctx, canbus.XLFrame{Priority: 9, Data: orig}); err != nil {
		t.Fatal(err)
	}
	var a, d canbus.XLFrame
	if err := rx.ReceiveXL(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if err := rx.ReceiveXL(ctx, &d); err != nil {
		t.Fatal(err)
	}
	ones := 0
	for i := range a.Data {
		for v := a.Data[i]; v != 0; v &= v - 1 {
			ones++
		}
	}
	if ones != 1 || !bytes.Equal(a.Data, d.Data) || orig[0] != 0 {
		t.Fatalf("corruption flipped %d bits; duplicate equal %v", ones, bytes.Equal(a.Data, d.Data))
	}
	if st := b.FaultStats(); st.Corrupted != 1 || st.Duplicated != 1 || st.Delivered != 3 {
		t.Fatalf("fault stats %+v", st)
	}
	b.SetFaults(Faults{DropNext: 1})
	_ = b.InjectXL(ctx, xl(t, "123#80:00:00000000#01"))
	if got := recvAny(t, rx, 50*time.Millisecond); got != "" {
		t.Fatalf("dropped frame delivered: %q", got)
	}
	// Interface down: no delivery, the error is reported once.
	b.SetFaults(Faults{})
	b.SetUp(false)
	if err := b.InjectXL(ctx, xl(t, "123#80:00:00000000#01")); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatalf("down: %v", err)
	}
	var x canbus.XLFrame
	if err := rx.ReceiveXL(ctx, &x); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatalf("down receive: %v", err)
	}
}

func TestXLWithoutLoopbackAndBlocking(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	quiet, _ := b.Open(WithXL(), WithoutLoopback())
	full, _ := b.Open(WithXL(), WithQueueSize(1))
	x := xl(t, "123#80:00:00000000#01")
	_ = quiet.SendXL(ctx, &x)
	if got := recvAny(t, full, 50*time.Millisecond); got != "" {
		t.Fatalf("loopback off delivered %q", got)
	}
	tx, _ := b.Open(WithXL())
	for i := 0; i < 3; i++ {
		_ = tx.SendXL(ctx, &x)
	}
	deadline := time.Now().Add(time.Second)
	for n, _ := full.Drops(); n != 2 && time.Now().Before(deadline); n, _ = full.Drops() {
		time.Sleep(time.Millisecond)
	}
	if n, _ := full.Drops(); n != 2 {
		t.Fatalf("drops %d", n)
	}
	blk, _ := b.Open(WithXL(), WithBlocking(), WithQueueSize(1))
	for i := 0; i < 3; i++ {
		_ = tx.SendXL(ctx, &x)
	}
	for i := 0; i < 3; i++ {
		if got := recvAny(t, blk, time.Second); got != x.String() {
			t.Fatalf("blocking port frame %d: %q", i, got)
		}
	}
}
