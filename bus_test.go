// SPDX-License-Identifier: MIT

package canbus_test

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/virtual"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newPair(t *testing.T, opts ...virtual.PortOption) (*virtual.Bus, *canbus.Bus, *canbus.Bus) {
	t.Helper()
	vb := virtual.New(virtual.Config{Name: "vtest"})
	pa, err := vb.Open(opts...)
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := vb.Open(opts...)
	a, err := canbus.NewBus(pa)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := canbus.NewBus(pb)
	t.Cleanup(func() { a.Close(); b.Close(); vb.Close() })
	return vb, a, b
}

func seqFrame(i uint32) canbus.Frame {
	var d [4]byte
	binary.BigEndian.PutUint32(d[:], i)
	f, _ := canbus.NewStandard(0x100, d[:])
	return f
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBusFanOutAndFilters(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	_, a, b := newPair(t)
	all, _ := b.Subscribe()
	only, _ := b.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactExtended(0x18DAF110)}}))
	f1, _ := canbus.NewStandard(0x18, []byte{1})
	f2, _ := canbus.NewExtended(0x18DAF110, []byte{2})
	for _, f := range []canbus.Frame{f1, f2} {
		if err := a.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []canbus.Frame{f1, f2} {
		got, err := all.Receive(ctx)
		if err != nil || got.ID != want.ID || got.Flags != want.Flags || got.Interface != "vtest" || got.Timestamp.IsZero() || got.Echo {
			t.Fatalf("all: %+v %v", got, err)
		}
	}
	got, err := only.Receive(ctx)
	if err != nil || got.ID != 0x18DAF110 {
		t.Fatalf("filtered: %+v %v", got, err)
	}
	if only.Received() != 1 || all.Received() != 2 {
		t.Errorf("received counters %d %d", only.Received(), all.Received())
	}
	select {
	case f := <-only.Frames():
		t.Fatalf("unexpected %v", f)
	default:
	}
	st := a.Stats()
	if st.TxFrames != 2 || st.TxBytes != 2 {
		t.Errorf("tx stats %+v", st)
	}
	waitFor(t, func() bool { return b.Stats().RxFrames == 2 })
}

func TestBusSendValidates(t *testing.T) {
	_, a, _ := newPair(t)
	bad := canbus.Frame{ID: 0x800}
	if err := a.Send(ctxT(t), &bad); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatal(err)
	}
}

func TestBusOverflowPolicies(t *testing.T) {
	ctx := ctxT(t)
	_, a, b := newPair(t)
	newest, _ := b.Subscribe(canbus.WithQueueSize(2))
	oldest, _ := b.Subscribe(canbus.WithQueueSize(2), canbus.WithOverflow(canbus.DropOldest))
	for i := uint32(0); i < 5; i++ {
		f := seqFrame(i)
		if err := a.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return b.Stats().RxFrames == 5 })
	check := func(s *canbus.Subscription, want ...uint32) {
		t.Helper()
		for _, w := range want {
			f, err := s.Receive(ctx)
			if err != nil || binary.BigEndian.Uint32(f.Data[:4]) != w {
				t.Fatalf("got %v %v, want seq %d", f, err, w)
			}
		}
	}
	check(newest, 0, 1)
	check(oldest, 3, 4)
	if newest.Dropped() != 3 || oldest.Dropped() != 3 || b.Stats().Dropped != 6 {
		t.Errorf("dropped %d %d %d", newest.Dropped(), oldest.Dropped(), b.Stats().Dropped)
	}
}

func TestBusBlockingSubscriberLosesNothing(t *testing.T) {
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	pa, _ := vb.Open()
	pb, _ := vb.Open(virtual.WithBlocking(), virtual.WithQueueSize(4))
	vb2, _ := canbus.NewBus(pb)
	defer vb2.Close()
	s, _ := vb2.Subscribe(canbus.WithQueueSize(1), canbus.WithOverflow(canbus.Block))
	const n = 2000
	go func() {
		for i := uint32(0); i < n; i++ {
			f := seqFrame(i)
			_ = pa.Send(ctx, &f)
		}
	}()
	for i := uint32(0); i < n; i++ {
		f, err := s.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := binary.BigEndian.Uint32(f.Data[:4]); got != i {
			t.Fatalf("frame %d: got sequence %d (loss or reordering)", i, got)
		}
	}
	if s.Dropped() != 0 {
		t.Fatal("blocking subscription dropped frames")
	}
}

func TestBusCloseSemantics(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	p, _ := vb.Open()
	b, _ := canbus.NewBus(p)
	s, _ := b.Subscribe()
	errc := make(chan error, 1)
	go func() {
		_, err := s.Receive(ctx)
		errc <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("blocked Receive: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal("second Close", err)
	}
	if _, err := b.Subscribe(); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal("Subscribe after Close", err)
	}
	f := seqFrame(1)
	if err := b.Send(ctx, &f); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal("Send after Close", err)
	}
	if !errors.Is(b.Err(), canbus.ErrClosed) || b.State() != canbus.StateClosed {
		t.Fatal(b.Err(), b.State())
	}
	<-b.Done()
}

func TestSubscriptionCloseDrainsQueue(t *testing.T) {
	ctx := ctxT(t)
	_, a, b := newPair(t)
	s, _ := b.Subscribe()
	f := seqFrame(7)
	_ = a.Send(ctx, &f)
	waitFor(t, func() bool { return s.Received() == 1 })
	s.Close()
	if got, err := s.Receive(ctx); err != nil || got.Data[3] != 7 {
		t.Fatal("queued frame lost", err)
	}
	if _, err := s.Receive(ctx); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if !errors.Is(s.Err(), canbus.ErrClosed) {
		t.Fatal(s.Err())
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed")
	}
}

func TestReceiveContextCancel(t *testing.T) {
	_, _, b := newPair(t)
	s, _ := b.Subscribe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Receive(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestEchoHandling(t *testing.T) {
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	p, _ := vb.Open(virtual.WithRecvOwn())
	b, _ := canbus.NewBus(p)
	defer b.Close()
	withEcho, _ := b.Subscribe()
	noEcho, _ := b.Subscribe(canbus.WithoutEcho())
	f := seqFrame(1)
	_ = b.Send(ctx, &f)
	got, err := withEcho.Receive(ctx)
	if err != nil || !got.Echo {
		t.Fatal("echo not delivered", err)
	}
	time.Sleep(20 * time.Millisecond)
	if noEcho.Received() != 0 {
		t.Fatal("WithoutEcho delivered the echo")
	}
}

type countingMetrics struct {
	canbus.NopMetrics
	sent, recv, drops, errs, queued atomic.Int64
	mu                              sync.Mutex
	states                          []string
}

func (m *countingMetrics) OnFrameSent(string, *canbus.Frame)     { m.sent.Add(1) }
func (m *countingMetrics) OnFrameReceived(string, *canbus.Frame) { m.recv.Add(1) }
func (m *countingMetrics) OnDrop(_ string, _ canbus.DropReason, n uint64) {
	m.drops.Add(int64(n))
}
func (m *countingMetrics) OnError(string, string, error) { m.errs.Add(1) }
func (m *countingMetrics) OnQueue(string, int, int)      { m.queued.Add(1) }
func (m *countingMetrics) OnStateChange(_ string, from, to canbus.State) {
	m.mu.Lock()
	m.states = append(m.states, from.String()+">"+to.String())
	m.mu.Unlock()
}

func TestMetricsAndErrorState(t *testing.T) {
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{Name: "m0"})
	defer vb.Close()
	p, _ := vb.Open(virtual.WithFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll}))
	var logs strings.Builder
	var lmu sync.Mutex
	m := &countingMetrics{}
	b, err := canbus.NewBus(p, canbus.WithMetrics(m), canbus.WithLogger(slog.New(slog.NewTextHandler(lockedWriter{&lmu, &logs}, nil))), canbus.WithInterfaceName("named"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Name() != "named" || b.Transport() != p {
		t.Fatal("name/transport")
	}
	s, _ := b.Subscribe(canbus.WithFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll}), canbus.WithQueueSize(1))
	steps := []struct {
		info canbus.ErrorInfo
		want canbus.State
	}{
		{canbus.ErrorInfo{Class: canbus.ErrClassController, Controller: canbus.CtrlTxWarning}, canbus.StateWarning},
		{canbus.ErrorInfo{Class: canbus.ErrClassController, Controller: canbus.CtrlRxPassive}, canbus.StatePassive},
		{canbus.ErrorInfo{Class: canbus.ErrClassBusOff}, canbus.StateBusOff},
		{canbus.ErrorInfo{Class: canbus.ErrClassRestarted}, canbus.StateActive},
		{canbus.ErrorInfo{Class: canbus.ErrClassController, Controller: canbus.CtrlTxPassive}, canbus.StatePassive},
		{canbus.ErrorInfo{Class: canbus.ErrClassController, Controller: canbus.CtrlActive}, canbus.StateActive},
	}
	for _, st := range steps {
		if err := vb.InjectError(ctx, st.info); err != nil {
			t.Fatal(err)
		}
		want := st.want
		waitFor(t, func() bool { return b.State() == want })
	}
	if b.Stats().RxErrors != uint64(len(steps)) {
		t.Errorf("RxErrors %d", b.Stats().RxErrors)
	}
	// The state changes before a frame reaches the subscriptions: wait
	// until the last one was delivered (the queue of 1 holds the first,
	// the others are dropped).
	waitFor(t, func() bool { return m.drops.Load() == int64(len(steps)-1) })
	if _, err := s.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	f := seqFrame(1)
	_ = b.Send(ctx, &f)
	if m.sent.Load() != 1 || m.recv.Load() != int64(len(steps)) || m.drops.Load() != int64(len(steps)-1) || m.queued.Load() != 1 {
		t.Errorf("metrics sent=%d recv=%d drops=%d queued=%d", m.sent.Load(), m.recv.Load(), m.drops.Load(), m.queued.Load())
	}
	m.mu.Lock()
	got := strings.Join(m.states, ",")
	m.mu.Unlock()
	if !strings.HasPrefix(got, "unknown>active,active>warning,warning>passive,passive>bus-off,bus-off>active") {
		t.Errorf("states %s", got)
	}
	lmu.Lock()
	defer lmu.Unlock()
	if !strings.Contains(logs.String(), "state changed") {
		t.Errorf("logs: %s", logs.String())
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func TestInterfaceDownAndUp(t *testing.T) {
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	pa, _ := vb.Open()
	pb, _ := vb.Open()
	m := &countingMetrics{}
	b, _ := canbus.NewBus(pb, canbus.WithMetrics(m))
	defer b.Close()
	s, _ := b.Subscribe()
	vb.SetUp(false)
	vb.SetUp(false) // no-op
	waitFor(t, func() bool { return b.State() == canbus.StateDown })
	f := seqFrame(1)
	if err := pa.Send(ctx, &f); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal("send while down", err)
	}
	vb.SetUp(true)
	if err := pa.Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	if b.State() != canbus.StateActive || m.errs.Load() != 1 || b.Stats().RxFailure != 1 {
		t.Fatal("state after up", b.State(), m.errs.Load())
	}
}

// failingTransport fails Receive with a non-recoverable-looking error to
// exercise the back-off path, and Send always.
type failingTransport struct {
	closed chan struct{}
	once   sync.Once
	n      atomic.Int32
}

func (f *failingTransport) Send(context.Context, *canbus.Frame) error { return errors.New("boom") }
func (f *failingTransport) Receive(ctx context.Context, _ *canbus.Frame) error {
	f.n.Add(1)
	select {
	case <-f.closed:
		return canbus.ErrClosed
	default:
		return errors.New("transient")
	}
}
func (f *failingTransport) Close() error { f.once.Do(func() { close(f.closed) }); return nil }
func (f *failingTransport) Drops() (uint64, canbus.DropReason) {
	return uint64(f.n.Load()), canbus.DropKernelOverflow
}

func TestBusTransportFailures(t *testing.T) {
	leakcheck.Check(t)
	ft := &failingTransport{closed: make(chan struct{})}
	m := &countingMetrics{}
	b, _ := canbus.NewBus(ft, canbus.WithMetrics(m))
	waitFor(t, func() bool { return ft.n.Load() >= 3 })
	f := seqFrame(1)
	if err := b.Send(ctxT(t), &f); err == nil || b.Stats().TxErrors != 1 {
		t.Fatal("send error not counted")
	}
	b.Close()
	if m.drops.Load() == 0 || b.Stats().Dropped == 0 {
		t.Fatal("DropCounter not reported")
	}
	if _, err := canbus.NewBus(nil); !errors.Is(err, canbus.ErrInvalidOption) {
		t.Fatal(err)
	}
	bad := func(*canbus.Bus) error { return canbus.ErrInvalidOption }
	if _, err := canbus.NewBus(ft, bad); err == nil {
		t.Fatal("option error ignored")
	}
}

// TestBusStress runs concurrent senders, consumers and subscription churn
// and checks that no frame is lost or reordered per sender. Run with -race.
func TestBusStress(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	rx, _ := vb.Open(virtual.WithBlocking())
	b, _ := canbus.NewBus(rx)
	defer b.Close()
	const senders, perSender = 4, 500
	subs := make([]*canbus.Subscription, 3)
	for i := range subs {
		subs[i], _ = b.Subscribe(canbus.WithOverflow(canbus.Block), canbus.WithQueueSize(16))
	}
	var wg sync.WaitGroup
	stopChurn := make(chan struct{})
	go func() { // subscription churn
		for {
			select {
			case <-stopChurn:
				return
			default:
			}
			s, err := b.Subscribe(canbus.WithQueueSize(1))
			if err == nil {
				s.Close()
			}
		}
	}()
	for s := 0; s < senders; s++ {
		port, _ := vb.Open()
		wg.Add(1)
		go func(id canbus.ID, p *virtual.Port) {
			defer wg.Done()
			for i := uint32(0); i < perSender; i++ {
				f := seqFrame(i)
				f.ID = id
				if err := p.Send(ctx, &f); err != nil {
					t.Error(err)
					return
				}
			}
		}(canbus.ID(0x100+s), port)
	}
	var cwg sync.WaitGroup
	for _, s := range subs {
		cwg.Add(1)
		go func(s *canbus.Subscription) {
			defer cwg.Done()
			next := map[canbus.ID]uint32{}
			for n := 0; n < senders*perSender; n++ {
				f, err := s.Receive(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				seq := binary.BigEndian.Uint32(f.Data[:4])
				if seq != next[f.ID] {
					t.Errorf("sender %X: got %d want %d", f.ID, seq, next[f.ID])
					return
				}
				next[f.ID]++
			}
		}(s)
	}
	wg.Wait()
	cwg.Wait()
	close(stopChurn)
}

func TestBusStartOnSubscribe(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	vb := virtual.New(virtual.Config{Name: "vtest"})
	defer vb.Close()
	pa, _ := vb.Open()
	pb, _ := vb.Open()
	defer pa.Close()
	b, err := canbus.NewBus(pb, canbus.WithStartOnSubscribe())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// Frames sent before the first subscription wait in the port.
	for i := 0; i < 50; i++ {
		f, _ := canbus.NewStandard(canbus.ID(i), []byte{byte(i)})
		if err := pa.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond) // an eager reader would have taken them
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		f, err := sub.Receive(ctx)
		if err != nil || f.ID != canbus.ID(i) {
			t.Fatalf("frame %d: %v %v", i, f.ID, err)
		}
	}
}

func TestBusStartOnSubscribeCloseUnstarted(t *testing.T) {
	leakcheck.Check(t)
	vb := virtual.New(virtual.Config{Name: "vtest"})
	defer vb.Close()
	p, _ := vb.Open()
	b, _ := canbus.NewBus(p, canbus.WithStartOnSubscribe())
	done := make(chan error, 1)
	go func() { done <- b.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close of a bus without subscriptions hangs")
	}
	if _, err := b.Subscribe(); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("Subscribe after Close: %v", err)
	}
	select {
	case <-b.Done():
	default:
		t.Fatal("Done not closed")
	}
}
