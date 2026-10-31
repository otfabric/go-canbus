// SPDX-License-Identifier: MIT

package canbus_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/virtual"
)

type xlMetrics struct {
	canbus.NopMetrics
	sent, recv, xlSent, xlRecv atomic.Int64
}

func (m *xlMetrics) OnFrameSent(string, *canbus.Frame)         { m.sent.Add(1) }
func (m *xlMetrics) OnFrameReceived(string, *canbus.Frame)     { m.recv.Add(1) }
func (m *xlMetrics) OnXLFrameSent(string, *canbus.XLFrame)     { m.xlSent.Add(1) }
func (m *xlMetrics) OnXLFrameReceived(string, *canbus.XLFrame) { m.xlRecv.Add(1) }

func mustXL(t *testing.T, s string) canbus.XLFrame {
	t.Helper()
	x, err := canbus.ParseXLFrame(s)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestBusXL(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{Name: "vxl"})
	defer vb.Close()
	pa, _ := vb.Open(virtual.WithXL())
	pb, _ := vb.Open(virtual.WithXL())
	m := &xlMetrics{}
	a, _ := canbus.NewBus(pa)
	b, _ := canbus.NewBus(pb, canbus.WithMetrics(m))
	defer a.Close()
	defer b.Close()

	all, _ := b.Subscribe(canbus.WithXL())
	classic, _ := b.Subscribe()
	byPrio, _ := b.Subscribe(canbus.WithXL(), canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(0x242)}}))
	byVCID, _ := b.Subscribe(canbus.WithXL(), canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{ID: 5 << 16, Mask: 0xFF << 16}}}))

	frames := []canbus.XLFrame{mustXL(t, "05242#81:01:12345678#AABBCCDDEE"), mustXL(t, "00243#80:00:00000000#11"), mustXL(t, "07242#80:00:00000000#22")}
	for i := range frames {
		if err := a.SendXL(ctx, &frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := canbus.NewStandard(0x100, []byte{1})
	if err := a.Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	expect := func(s *canbus.Subscription, want ...string) {
		t.Helper()
		for _, w := range want {
			x, err := s.ReceiveXL(ctx)
			if err != nil || x.String() != w {
				t.Fatalf("got %v %v, want %s", x, err, w)
			}
			if x.Interface != "vxl" || x.Timestamp.IsZero() {
				t.Fatalf("metadata %q %v", x.Interface, x.Timestamp)
			}
		}
	}
	expect(all, frames[0].String(), frames[1].String(), frames[2].String())
	expect(byPrio, frames[0].String(), frames[2].String())
	expect(byVCID, frames[0].String())
	if g, err := all.Receive(ctx); err != nil || g.String() != f.String() {
		t.Fatalf("classic frame on the XL subscription: %v %v", g, err)
	}
	if g, err := classic.Receive(ctx); err != nil || g.String() != f.String() {
		t.Fatalf("classic subscription: %v %v", g, err)
	}
	if _, err := classic.ReceiveXL(ctx); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("ReceiveXL without WithXL: %v", err)
	}
	if classic.Frames() == nil || all.Frames() != nil {
		t.Fatal("Frames channels")
	}
	waitFor(t, func() bool { return b.Stats().RxFrames == 4 })
	if st := b.Stats(); st.RxBytes != 5+1+1+1 {
		t.Fatalf("bus stats %+v", st)
	}
	if st := a.Stats(); st.TxFrames != 4 || st.TxBytes != 8 {
		t.Fatalf("sender stats %+v", st)
	}
	if m.xlRecv.Load() != 3 || m.recv.Load() != 1 {
		t.Fatalf("metrics: %d XL, %d classic", m.xlRecv.Load(), m.recv.Load())
	}
	if all.Received() != 4 || classic.Received() != 1 || byPrio.Received() != 2 {
		t.Fatalf("received %d %d %d", all.Received(), classic.Received(), byPrio.Received())
	}
	// Each subscription owns its payload.
	_ = a.SendXL(ctx, &frames[0])
	x1, _ := all.ReceiveXL(ctx)
	x1.Data[0] = 0
	x2, _ := byPrio.ReceiveXL(ctx)
	if x2.Data[0] != 0xAA {
		t.Fatal("subscriptions share the payload")
	}
}

func TestBusXLUnsupportedAndClosed(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	_, a, _ := newPair(t, virtual.WithFD())
	x := mustXL(t, "242#80:00:00000000#11")
	if err := a.SendXL(ctx, &x); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("XL on an FD-only port: %v", err)
	}
	if err := a.SendXL(ctx, &canbus.XLFrame{}); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatalf("invalid: %v", err)
	}

	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	p, _ := vb.Open() // the bus is an XLTransport, the port refuses CAN XL
	m := &countingMetrics{}
	b, _ := canbus.NewBus(p, canbus.WithMetrics(m))
	if err := b.SendXL(ctx, &x); !errors.Is(err, canbus.ErrUnsupported) || m.errs.Load() != 1 || b.Stats().TxErrors != 1 {
		t.Fatalf("port without WithXL: %v, %d errors", err, m.errs.Load())
	}
	s, _ := b.Subscribe(canbus.WithXL())
	_ = b.Close()
	if err := b.SendXL(ctx, &x); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("closed bus: %v", err)
	}
	if _, err := s.ReceiveXL(ctx); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("closed subscription: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	s2 := mustSub(t, ctx)
	if _, err := s2.ReceiveXL(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

// mustSub returns an XL subscription on a fresh, idle bus.
func mustSub(t *testing.T, ctx context.Context) *canbus.Subscription {
	t.Helper()
	vb := virtual.New(virtual.Config{})
	p, _ := vb.Open(virtual.WithXL())
	b, _ := canbus.NewBus(p)
	t.Cleanup(func() { b.Close(); vb.Close() })
	s, err := b.Subscribe(canbus.WithXL())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// nonXL is a transport without CAN XL (not an XLTransport).
type nonXL struct{ canbus.Transport }

func TestBusXLNotXLTransport(t *testing.T) {
	leakcheck.Check(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	p, _ := vb.Open()
	b, _ := canbus.NewBus(nonXL{p})
	defer b.Close()
	if err := b.SendXL(ctxT(t), &canbus.XLFrame{Data: []byte{1}}); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestBusXLOverflowAndEcho(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	p, _ := vb.Open(virtual.WithXL(), virtual.WithRecvOwn())
	b, _ := canbus.NewBus(p, canbus.WithStartOnSubscribe())
	defer b.Close()
	newest, _ := b.Subscribe(canbus.WithXL(), canbus.WithQueueSize(2))
	oldest, _ := b.Subscribe(canbus.WithXL(), canbus.WithQueueSize(2), canbus.WithOverflow(canbus.DropOldest))
	noEcho, _ := b.Subscribe(canbus.WithXL(), canbus.WithoutEcho())
	for i := 0; i < 4; i++ {
		x := mustXL(t, fmt.Sprintf("123#80:00:00000000#%02X", i))
		if err := b.SendXL(ctx, &x); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return newest.Dropped() == 2 && oldest.Dropped() == 2 })
	for _, c := range []struct {
		s    *canbus.Subscription
		want []byte
	}{{newest, []byte{0, 1}}, {oldest, []byte{2, 3}}} {
		for _, w := range c.want {
			x, err := c.s.ReceiveXL(ctx)
			if err != nil || x.Data[0] != w || !x.Echo {
				t.Fatalf("got %v %v echo=%v, want data %02X", x, err, x.Echo, w)
			}
		}
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if x, err := noEcho.ReceiveXL(short); err == nil {
		t.Fatalf("echo delivered: %v", x)
	}
	if b.Stats().Dropped != 4 {
		t.Fatalf("bus drops %d", b.Stats().Dropped)
	}
}

func TestBusXLBlocking(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	pa, _ := vb.Open(virtual.WithXL())
	pb, _ := vb.Open(virtual.WithXL(), virtual.WithQueueSize(4096))
	a, _ := canbus.NewBus(pa)
	b, _ := canbus.NewBus(pb, canbus.WithStartOnSubscribe())
	defer a.Close()
	defer b.Close()
	s, _ := b.Subscribe(canbus.WithXL(), canbus.WithQueueSize(1), canbus.WithOverflow(canbus.Block))
	const n = 200
	go func() {
		for i := 0; i < n; i++ {
			x := canbus.XLFrame{Priority: uint16(i % 0x800), Data: []byte{byte(i)}}
			_ = a.SendXL(ctx, &x)
		}
	}()
	for i := 0; i < n; i++ {
		x, err := s.ReceiveXL(ctx)
		if err != nil || x.Data[0] != byte(i) {
			t.Fatalf("frame %d: %v %v", i, x, err)
		}
	}
	if s.Dropped() != 0 {
		t.Fatalf("dropped %d", s.Dropped())
	}
	// A blocked delivery ends when the subscription closes.
	x := canbus.XLFrame{Data: []byte{1}}
	_ = a.SendXL(ctx, &x)
	_ = a.SendXL(ctx, &x)
	waitFor(t, func() bool { return s.Received() >= n+1 })
	_ = s.Close()
}

// TestBusXLOrder: a WithXL subscription returns CAN, CAN FD and CAN XL
// frames in arrival order through ReceiveAny, whatever their timestamps
// (a coarse clock gives many frames the same one).
func TestBusXLOrder(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	same := time.Unix(1800000000, 0)
	vb := virtual.New(virtual.Config{Clock: func() time.Time { return same }})
	defer vb.Close()
	tx, _ := vb.Open(virtual.WithXL())
	p, _ := vb.Open(virtual.WithXL())
	b, _ := canbus.NewBus(p, canbus.WithStartOnSubscribe())
	defer b.Close()
	s, _ := b.Subscribe(canbus.WithXL())
	const n = 300
	for i := 0; i < n; i++ {
		if i%3 == 0 {
			f, _ := canbus.NewStandard(canbus.ID(i%0x800), []byte{byte(i)})
			_ = tx.Send(ctx, &f)
		} else {
			_ = tx.SendXL(ctx, &canbus.XLFrame{Priority: uint16(i % 0x800), Data: []byte{byte(i)}})
		}
	}
	var f canbus.Frame
	var x canbus.XLFrame
	for i := 0; i < n; i++ {
		isXL, err := s.ReceiveAny(ctx, &f, &x)
		if err != nil {
			t.Fatal(err)
		}
		if isXL != (i%3 != 0) || isXL && x.Data[0] != byte(i) || !isXL && f.Data[0] != byte(i) {
			t.Fatalf("frame %d out of order (xl %v)", i, isXL)
		}
	}
	// Receive skips CAN XL frames; ReceiveAny without WithXL is Receive.
	f0, _ := canbus.NewStandard(1, []byte{1})
	_ = tx.SendXL(ctx, &canbus.XLFrame{Data: []byte{9}})
	_ = tx.Send(ctx, &f0)
	if g, err := s.Receive(ctx); err != nil || g.ID != 1 {
		t.Fatalf("Receive: %v %v", g, err)
	}
	c, _ := b.Subscribe()
	_ = tx.Send(ctx, &f0)
	if isXL, err := c.ReceiveAny(ctx, &f, &x); err != nil || isXL || f.ID != 1 {
		t.Fatalf("ReceiveAny without WithXL: %v %v", isXL, err)
	}
}
