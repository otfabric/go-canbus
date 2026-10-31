// SPDX-License-Identifier: MIT

package virtual

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func recv(t *testing.T, p *Port, d time.Duration) (canbus.Frame, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var f canbus.Frame
	err := p.Receive(ctx, &f)
	return f, err
}

func TestLoopbackRules(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	quiet, _ := b.Open(WithoutLoopback())
	own, _ := b.Open(WithRecvOwn())
	other, _ := b.Open()
	f, _ := canbus.NewStandard(1, []byte{1})
	_ = quiet.Send(ctx, &f)
	if _, err := recv(t, other, 30*time.Millisecond); err == nil {
		t.Fatal("frame of a port without loopback delivered")
	}
	_ = own.Send(ctx, &f)
	g, err := recv(t, own, time.Second)
	if err != nil || !g.Echo {
		t.Fatal("own echo", err)
	}
	g, err = recv(t, other, time.Second)
	if err != nil || g.Echo || g.Interface != "virtual0" {
		t.Fatal("other", g, err)
	}
	g, err = recv(t, quiet, time.Second)
	if err != nil || g.Echo {
		t.Fatal("quiet port still receives others", err)
	}
	if own.Name() != "virtual0" || !own.Capabilities().RecvOwn {
		t.Fatal("name/capabilities")
	}
}

func TestFDPorts(t *testing.T) {
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	classic, _ := b.Open()
	fd, _ := b.Open(WithFD())
	f, _ := canbus.NewFD(0x10, false, true, make([]byte, 64))
	if err := classic.Send(ctx, &f); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal(err)
	}
	fd2, _ := b.Open(WithFD())
	if err := fd.Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if g, err := recv(t, fd2, time.Second); err != nil || g.Len != 64 || g.Flags&canbus.FlagBRS == 0 {
		t.Fatal(g, err)
	}
	if _, err := recv(t, classic, 30*time.Millisecond); err == nil {
		t.Fatal("FD frame delivered to a classical port")
	}
	ef := canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassBusOff})
	if err := fd.Send(ctx, &ef); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatal("error frame sent", err)
	}
	bad := canbus.Frame{ID: 0x800}
	if err := fd.Send(ctx, &bad); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatal(err)
	}
}

func TestFiltersAndErrorMask(t *testing.T) {
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	tx, _ := b.Open()
	rx, _ := b.Open(WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(2)}}))
	for id := canbus.ID(1); id <= 3; id++ {
		f, _ := canbus.NewStandard(id, nil)
		_ = tx.Send(ctx, &f)
	}
	if g, err := recv(t, rx, time.Second); err != nil || g.ID != 2 {
		t.Fatal(g, err)
	}
	_ = b.InjectError(ctx, canbus.ErrorInfo{Class: canbus.ErrClassNoAck})
	if _, err := recv(t, rx, 30*time.Millisecond); err == nil {
		t.Fatal("error frame passed an empty error mask")
	}
	_ = rx.SetFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassNoAck})
	_ = b.InjectError(ctx, canbus.ErrorInfo{Class: canbus.ErrClassNoAck})
	if g, err := recv(t, rx, time.Second); err != nil || !g.IsError() {
		t.Fatal(g, err)
	}
}

func TestQueueOverflowCountsDrops(t *testing.T) {
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	tx, _ := b.Open()
	rx, _ := b.Open(WithQueueSize(2))
	for i := 0; i < 5; i++ {
		f, _ := canbus.NewStandard(1, []byte{byte(i)})
		_ = tx.Send(ctx, &f)
	}
	deadline := time.Now().Add(2 * time.Second)
	for b.FaultStats().Delivered < 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n, reason := rx.Drops(); n != 3 || reason != canbus.DropQueueFull {
		t.Fatal(n, reason)
	}
	if st := rx.Stats(); st.Dropped != 3 {
		t.Fatal(st)
	}
	if st := tx.Stats(); st.TxFrames != 5 || st.TxBytes != 5 {
		t.Fatal(st)
	}
}

// pattern sends n frames through a bus with faults and returns the
// sequence numbers received.
func pattern(t *testing.T, seed int64, faults Faults, n int) []byte {
	ctx := ctxT(t)
	b := New(Config{Seed: seed})
	defer b.Close()
	b.SetFaults(faults)
	tx, _ := b.Open()
	rx, _ := b.Open(WithQueueSize(4 * n))
	for i := 0; i < n; i++ {
		f, _ := canbus.NewStandard(1, []byte{byte(i), 0})
		if err := tx.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	var got []byte
	for {
		f, err := recv(t, rx, 50*time.Millisecond)
		if err != nil {
			return got
		}
		got = append(got, f.Data[0])
		if f.Data[1] != 0 {
			got = append(got, 0xFF) // corruption marker
		}
	}
}

func TestFaultsAreDeterministic(t *testing.T) {
	faults := Faults{Drop: 0.2, Duplicate: 0.2, Corrupt: 0.1}
	a := pattern(t, 42, faults, 100)
	b := pattern(t, 42, faults, 100)
	if string(a) != string(b) {
		t.Fatal("same seed, different outcome")
	}
	c := pattern(t, 43, faults, 100)
	if string(a) == string(c) {
		t.Fatal("different seeds, same outcome")
	}
	if len(a) == 100 {
		t.Fatal("faults had no effect")
	}
	if got := pattern(t, 1, Faults{DropNext: 3}, 5); string(got) != "\x03\x04" {
		t.Fatalf("DropNext: % x", got)
	}
}

func TestDelayPreservesOrder(t *testing.T) {
	ctx := ctxT(t)
	b := New(Config{})
	defer b.Close()
	b.SetFaults(Faults{Delay: 1, DelayBy: 30 * time.Millisecond})
	tx, _ := b.Open()
	rx, _ := b.Open()
	start := time.Now()
	for i := 0; i < 2; i++ {
		f, _ := canbus.NewStandard(1, []byte{byte(i)})
		_ = tx.Send(ctx, &f)
	}
	for i := 0; i < 2; i++ {
		f, err := recv(t, rx, time.Second)
		if err != nil || f.Data[0] != byte(i) {
			t.Fatal(f, err)
		}
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("delay not applied")
	}
	if st := b.FaultStats(); st.Delayed != 2 {
		t.Fatal(st)
	}
}

func TestCloseUnblocks(t *testing.T) {
	leakcheck.Check(t)
	b := New(Config{})
	p, _ := b.Open()
	errc := make(chan error, 1)
	go func() {
		var f canbus.Frame
		errc <- p.Receive(context.Background(), &f)
	}()
	time.Sleep(10 * time.Millisecond)
	b.Close()
	if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	f, _ := canbus.NewStandard(1, nil)
	if err := p.Send(context.Background(), &f); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := b.Open(); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if err := b.Inject(context.Background(), f); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal("inject after close", err)
	}
	b.Close()
}

func TestInjectAndBlockedSendCancel(t *testing.T) {
	b := New(Config{QueueSize: 1})
	defer b.Close()
	rx, _ := b.Open(WithBlocking(), WithQueueSize(1))
	tx, _ := b.Open()
	bad := canbus.Frame{ID: 0x800}
	if err := b.Inject(context.Background(), bad); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatal(err)
	}
	raw := canbus.Frame{ID: 1, Len: 9}
	if err := b.InjectRaw(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if f, err := recv(t, rx, time.Second); err != nil || f.Len != 9 {
		t.Fatal(f, err)
	}
	// Fill the port queue, the dispatcher and the bus queue, then a send
	// must block until its context ends.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var err error
	for i := 0; i < 10 && err == nil; i++ {
		f, _ := canbus.NewStandard(1, nil)
		err = tx.Send(ctx, &f)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	rx.Close()
	rx.Close()
}

func TestReceiveDownError(t *testing.T) {
	b := New(Config{})
	defer b.Close()
	p, _ := b.Open()
	b.SetUp(false)
	if _, err := recv(t, p, time.Second); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
	if err := b.Inject(context.Background(), canbus.Frame{ID: 1}); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
	if _, err := recv(t, p, 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("down error must be reported once", err)
	}
}
