// SPDX-License-Identifier: MIT

package canbus_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
)

// The lifecycle contract tests of Bus and Subscription (TESTING.md, rows
// L1 to L16). They run in a testing/synctest bubble: the bus, its
// subscriptions and the virtual bus under them block on channels and
// timers only, so synctest.Wait returns when every goroutine waits (the
// test decides the interleaving, not a sleep), deadlines take fake time,
// and a goroutine left blocked when the test ends fails it.

// bubbleBus returns a Bus on a port of a virtual bus and a second port to
// send it frames from, created in the bubble of the caller. The caller
// closes the Bus; the virtual bus is closed when the test ends.
func bubbleBus(t *testing.T, opts ...canbus.Option) (b *canbus.Bus, peer *virtual.Port, vb *virtual.Bus) {
	t.Helper()
	vb = virtual.New(virtual.Config{Name: "life0"})
	t.Cleanup(func() { _ = vb.Close() })
	port, err := vb.Open(virtual.WithXL())
	if err != nil {
		t.Fatal(err)
	}
	peer, err = vb.Open(virtual.WithXL())
	if err != nil {
		t.Fatal(err)
	}
	b, err = canbus.NewBus(port, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return b, peer, vb
}

func mustSend(t *testing.T, p *virtual.Port, id canbus.ID) {
	t.Helper()
	f, err := canbus.NewStandard(id, []byte{byte(id)})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

// TestBusCloseTwice (L1, L2): Close of a Bus and of a Subscription returns
// nil every time, one after the other and from several goroutines at once;
// the transport is closed once.
func TestBusCloseTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ct := &closeCounter{Transport: mustPort(t)}
		b, err := canbus.NewBus(ct)
		if err != nil {
			t.Fatal(err)
		}
		s, _ := b.Subscribe()
		other, _ := b.Subscribe()
		// Two goroutines close the subscription, then four the bus.
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for range 2 {
			wg.Go(func() { errs <- s.Close() })
		}
		wg.Wait()
		for range 4 {
			wg.Go(func() { errs <- b.Close() })
		}
		wg.Wait()
		errs <- b.Close()
		errs <- s.Close()
		errs <- other.Close() // ended by the bus already
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("Close: %v", err)
			}
		}
		if n := ct.closes.Load(); n != 1 {
			t.Errorf("the transport was closed %d times", n)
		}
		if !errors.Is(b.Err(), canbus.ErrClosed) || !errors.Is(s.Err(), canbus.ErrClosed) || !errors.Is(other.Err(), canbus.ErrClosed) || b.State() != canbus.StateClosed {
			t.Errorf("bus %v (%v), subscriptions %v and %v", b.Err(), b.State(), s.Err(), other.Err())
		}
		for name, done := range map[string]<-chan struct{}{"bus": b.Done(), "subscription": s.Done(), "other subscription": other.Done()} {
			select {
			case <-done:
			default:
				t.Errorf("Done of the %s is not closed", name)
			}
		}
	})
}

func mustPort(t *testing.T) *virtual.Port {
	t.Helper()
	vb := virtual.New(virtual.Config{})
	t.Cleanup(func() { _ = vb.Close() })
	p, err := vb.Open()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// closeCounter counts the Close calls of the transport it wraps.
type closeCounter struct {
	canbus.Transport
	closes atomic.Int32
}

func (c *closeCounter) Close() error {
	c.closes.Add(1)
	return c.Transport.Close()
}

// TestBusCloseDuringReceive (L3): Close of the bus, and Close of a
// subscription, end a Receive that waits, with ErrClosed; the Receive of
// another subscription is ended by the bus only. Calls after Close fail at
// once.
func TestBusCloseDuringReceive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _, _ := bubbleBus(t)
		s, _ := b.Subscribe()
		other, _ := b.Subscribe()
		any, _ := b.Subscribe(canbus.WithXL())
		type result struct {
			name string
			err  error
		}
		results := make(chan result, 3)
		go func() { _, err := s.Receive(context.Background()); results <- result{"subscription", err} }()
		go func() { _, err := other.Receive(context.Background()); results <- result{"other", err} }()
		go func() {
			var f canbus.Frame
			var x canbus.XLFrame
			_, err := any.ReceiveAny(context.Background(), &f, &x)
			results <- result{"any", err}
		}()
		synctest.Wait() // the three calls wait for a frame
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if r := <-results; r.name != "subscription" || !errors.Is(r.err, canbus.ErrClosed) {
			t.Fatalf("after Subscription.Close: Receive of %s returned %v", r.name, r.err)
		}
		synctest.Wait()
		select {
		case r := <-results:
			t.Fatalf("Close of one subscription ended the Receive of %s: %v", r.name, r.err)
		default:
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if r := <-results; !errors.Is(r.err, canbus.ErrClosed) {
				t.Errorf("after Bus.Close: Receive of %s returned %v", r.name, r.err)
			}
		}
		if _, err := other.Receive(context.Background()); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Receive after Close: %v", err)
		}
		if _, err := b.Subscribe(); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Subscribe after Close: %v", err)
		}
		f, _ := canbus.NewStandard(1, nil)
		if err := b.Send(context.Background(), &f); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Send after Close: %v", err)
		}
	})
}

// TestBusCloseDuringSend (L4): Close ends a Send that waits in the
// transport (a virtual bus whose queue is full), with ErrClosed, and the
// frame is not delivered.
func TestBusCloseDuringSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		vb := virtual.New(virtual.Config{QueueSize: 1})
		defer vb.Close()
		// A blocking port nobody reads holds the virtual bus up.
		slow, _ := vb.Open(virtual.WithBlocking(), virtual.WithQueueSize(1))
		port, _ := vb.Open()
		b, err := canbus.NewBus(port)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 3 { // slow's queue, the dispatcher, the bus queue
			f, _ := canbus.NewStandard(canbus.ID(i+1), nil)
			if err := b.Send(context.Background(), &f); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		errc := make(chan error, 1)
		go func() {
			f, _ := canbus.NewStandard(0x10, nil)
			errc <- b.Send(context.Background(), &f)
		}()
		synctest.Wait() // the fourth Send waits for room
		select {
		case err := <-errc:
			t.Fatalf("Send on a full bus returned: %v", err)
		default:
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
			t.Fatalf("Send at Close: %v, want ErrClosed", err)
		}
		if st := b.Stats(); st.TxFrames != 3 {
			t.Errorf("%d frames counted as sent, want 3", st.TxFrames)
		}
		var ids []canbus.ID
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			var f canbus.Frame
			err := slow.Receive(ctx, &f)
			cancel()
			if err != nil {
				break
			}
			ids = append(ids, f.ID)
		}
		if len(ids) != 3 {
			t.Errorf("delivered %v, want the three frames sent before", ids)
		}
	})
}

// TestReceiveCancel (L6): a Receive that waits ends with its context,
// at its deadline exactly or when it is cancelled; the subscription stays
// usable and loses no frame.
func TestReceiveCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, peer, _ := bubbleBus(t)
		defer b.Close()
		s, _ := b.Subscribe()
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := s.Receive(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 50*time.Millisecond {
			t.Fatalf("Receive: %v after %v, want context.DeadlineExceeded after 50ms", err, time.Since(start))
		}
		cctx, ccancel := context.WithCancel(context.Background())
		errc := make(chan error, 1)
		go func() { _, err := s.Receive(cctx); errc <- err }()
		synctest.Wait()
		ccancel()
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Receive: %v", err)
		}
		mustSend(t, peer, 0x42)
		if f, err := s.Receive(context.Background()); err != nil || f.ID != 0x42 {
			t.Fatalf("Receive after a cancelled one: %v, %v", f, err)
		}
	})
}

// hookMetrics calls the function set for a callback, if any.
type hookMetrics struct {
	canbus.NopMetrics
	sent, received func()
	xlSent, xlRecv func()
	drop, queue    func()
	state          func(from, to canbus.State)
	failure        func(op string)
}

func (m *hookMetrics) OnFrameSent(string, *canbus.Frame) {
	if m.sent != nil {
		m.sent()
	}
}

func (m *hookMetrics) OnFrameReceived(string, *canbus.Frame) {
	if m.received != nil {
		m.received()
	}
}

func (m *hookMetrics) OnXLFrameSent(string, *canbus.XLFrame) {
	if m.xlSent != nil {
		m.xlSent()
	}
}

func (m *hookMetrics) OnXLFrameReceived(string, *canbus.XLFrame) {
	if m.xlRecv != nil {
		m.xlRecv()
	}
}

func (m *hookMetrics) OnDrop(string, canbus.DropReason, uint64) {
	if m.drop != nil {
		m.drop()
	}
}

func (m *hookMetrics) OnQueue(string, int, int) {
	if m.queue != nil {
		m.queue()
	}
}

func (m *hookMetrics) OnStateChange(_ string, from, to canbus.State) {
	if m.state != nil {
		m.state(from, to)
	}
}

func (m *hookMetrics) OnError(_, op string, _ error) {
	if m.failure != nil {
		m.failure(op)
	}
}

// TestCallbackCallsBus (L9): a Metrics callback on the reader goroutine
// calls the methods the documentation allows (Stats, State, Err, Subscribe,
// Send, a subscription's Close) and they return what they return anywhere
// else; the frame that triggered the callback is delivered afterwards.
func TestCallbackCallsBus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &hookMetrics{}
		b, peer, _ := bubbleBus(t, canbus.WithMetrics(m))
		defer b.Close()
		s, _ := b.Subscribe()
		var calls int
		problems := make(chan string, 8)
		m.received = func() {
			calls++
			if st := b.Stats(); st.RxFrames != 1 {
				problems <- fmt.Sprintf("Stats in the callback: %+v", st)
			}
			if b.State() != canbus.StateActive || b.Err() != nil {
				problems <- "State or Err in the callback"
			}
			extra, err := b.Subscribe()
			if err != nil {
				problems <- "Subscribe in the callback: " + err.Error()
				return
			}
			if err := extra.Close(); err != nil {
				problems <- "Subscription.Close in the callback: " + err.Error()
			}
			f, _ := canbus.NewStandard(0x77, []byte{7})
			if err := b.Send(context.Background(), &f); err != nil {
				problems <- "Send in the callback: " + err.Error()
			}
		}
		mustSend(t, peer, 0x42)
		if f, err := s.Receive(context.Background()); err != nil || f.ID != 0x42 {
			t.Fatalf("the frame of the callback: %v, %v", f, err)
		}
		var answer canbus.Frame
		if err := peer.Receive(context.Background(), &answer); err != nil || answer.ID != 0x77 {
			t.Fatalf("the frame sent by the callback: %v, %v", answer, err)
		}
		close(problems)
		for p := range problems {
			t.Error(p)
		}
		if calls != 1 || b.Stats().TxFrames != 1 {
			t.Errorf("%d callbacks, %d frames sent", calls, b.Stats().TxFrames)
		}
	})
}

// TestCallbackClosesBus (L10): a Metrics callback on the reader goroutine
// closes the bus with "go bus.Close()", as the documentation says. That
// Close completes after the callback has returned, not before, and the bus
// is then closed like any other.
func TestCallbackClosesBus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &hookMetrics{}
		b, peer, _ := bubbleBus(t, canbus.WithMetrics(m))
		s, _ := b.Subscribe()
		closed := make(chan error, 1)
		release := make(chan struct{})
		m.received = func() {
			go func() { closed <- b.Close() }()
			<-release // the callback is still running
		}
		mustSend(t, peer, 0x42)
		synctest.Wait() // the callback waits; Close waits for the reader
		select {
		case err := <-closed:
			t.Fatalf("Close returned while the callback ran: %v", err)
		case <-b.Done():
			t.Fatal("the reader ended while its callback ran")
		default:
		}
		close(release)
		if err := <-closed; err != nil {
			t.Fatalf("Close started by the callback: %v", err)
		}
		select {
		case <-b.Done():
		default:
			t.Fatal("Done is not closed after Close returned")
		}
		if !errors.Is(b.Err(), canbus.ErrClosed) || b.State() != canbus.StateClosed {
			t.Errorf("after Close: %v, %v", b.Err(), b.State())
		}
		// The frame of the callback was read before the close: it is still
		// in the subscription's queue, followed by ErrClosed.
		if f, err := s.Receive(context.Background()); err != nil || f.ID != 0x42 {
			t.Errorf("the frame of the callback: %v, %v", f, err)
		}
		if _, err := s.Receive(context.Background()); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Receive after Close: %v", err)
		}
	})
}

// logRecorder collects the records of a bus logger.
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *logRecorder) WithGroup(string) slog.Handler            { return r }
func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}

// callbackFailures returns, per callback name, the stack logged for a
// callback that did not return normally.
func (r *logRecorder) callbackFailures(t *testing.T) map[string]string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]string{}
	for _, rec := range r.records {
		if !strings.HasSuffix(rec.Message, "callback did not return normally") {
			continue
		}
		if rec.Level != slog.LevelError {
			t.Errorf("%q logged at %v, want error", rec.Message, rec.Level)
		}
		var name, value, stack string
		rec.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "callback":
				name = a.Value.String()
			case "value":
				value = a.Value.String()
			case "stack":
				stack = a.Value.String()
			}
			return true
		})
		if value != "callback failed" {
			t.Errorf("%s: value %q logged", name, value)
		}
		out[name] = stack
	}
	return out
}

// TestCallbackFailureLogged: a Metrics callback that does not return
// normally is stopped where the bus calls it and logged at error level
// with its value and its stack; the bus goes on: the frame is delivered and counted, Send returns
// nil, the next frame is read. Every callback of Metrics and XLMetrics is
// covered.
func TestCallbackFailureLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fail := func() { panic("callback failed") }
		m := &hookMetrics{sent: fail, received: fail, xlSent: fail, xlRecv: fail, drop: fail, queue: fail,
			state: func(_, _ canbus.State) { fail() }, failure: func(string) { fail() }}
		logs := &logRecorder{}
		b, peer, vb := bubbleBus(t, canbus.WithMetrics(m), canbus.WithLogger(slog.New(logs)))
		s, _ := b.Subscribe(canbus.WithXL(), canbus.WithQueueSize(1))

		// Received: OnFrameReceived and OnQueue, then OnDrop for the
		// second frame (the queue holds one).
		mustSend(t, peer, 0x42)
		mustSend(t, peer, 0x43)
		synctest.Wait()
		var f canbus.Frame
		var x canbus.XLFrame
		if xl, err := s.ReceiveAny(context.Background(), &f, &x); err != nil || xl || f.ID != 0x42 {
			t.Fatalf("frame after a failed OnFrameReceived: %v, %v", f, err)
		}
		if st := b.Stats(); st.RxFrames != 2 || st.Dropped != 1 || s.Dropped() != 1 {
			t.Errorf("stats %v, subscription dropped %d", st, s.Dropped())
		}
		// CAN XL, received and sent.
		xf := canbus.XLFrame{Priority: 5, Data: []byte{1, 2, 3}}
		if err := peer.SendXL(context.Background(), &xf); err != nil {
			t.Fatal(err)
		}
		if xl, err := s.ReceiveAny(context.Background(), &f, &x); err != nil || !xl || x.Priority != 5 {
			t.Fatalf("CAN XL frame after a failed OnXLFrameReceived: %v, %v", x, err)
		}
		if err := b.SendXL(context.Background(), &xf); err != nil {
			t.Fatalf("SendXL with a failing OnXLFrameSent: %v", err)
		}
		// Sent, and a send that fails (OnError).
		out, _ := canbus.NewStandard(0x10, nil)
		if err := b.Send(context.Background(), &out); err != nil {
			t.Fatalf("Send with a failing OnFrameSent: %v", err)
		}
		vb.SetUp(false)
		if err := b.Send(context.Background(), &out); !errors.Is(err, canbus.ErrInterfaceDown) {
			t.Fatalf("Send on a bus that is down: %v", err)
		}
		synctest.Wait() // the reader saw the interface go down: OnError, OnStateChange
		vb.SetUp(true)
		mustSend(t, peer, 0x44)
		if xl, err := s.ReceiveAny(context.Background(), &f, &x); err != nil || xl || f.ID != 0x44 {
			t.Fatalf("frame after the failed callbacks: %v, %v", f, err)
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		got := logs.callbackFailures(t)
		for _, name := range []string{"Metrics.OnFrameSent", "Metrics.OnFrameReceived", "Metrics.OnDrop", "Metrics.OnError",
			"Metrics.OnQueue", "Metrics.OnStateChange", "XLMetrics.OnXLFrameSent", "XLMetrics.OnXLFrameReceived"} {
			stack, ok := got[name]
			if !ok {
				t.Errorf("no log entry for a failed %s", name)
				continue
			}
			if !strings.Contains(stack, "hookMetrics") || !strings.Contains(stack, "goroutine ") {
				t.Errorf("%s: the logged stack does not show the callback:\n%s", name, stack)
			}
		}
	})
}

// TestNewBusFailure (L14): a NewBus that returns an error has started no
// goroutine (the bubble would report one left behind) and has not closed
// the transport, which stays the caller's.
func TestNewBusFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if b, err := canbus.NewBus(nil); b != nil || !errors.Is(err, canbus.ErrInvalidOption) {
			t.Fatalf("NewBus(nil): %v, %v", b, err)
		}
		ct := &closeCounter{Transport: mustPort(t)}
		refused := errors.New("option refused")
		var applied int
		ok := func(*canbus.Bus) error { applied++; return nil }
		bad := func(*canbus.Bus) error { return refused }
		if b, err := canbus.NewBus(ct, ok, bad, ok); b != nil || !errors.Is(err, refused) {
			t.Fatalf("NewBus with a refused option: %v, %v", b, err)
		}
		if applied != 1 || ct.closes.Load() != 0 {
			t.Errorf("%d options applied, transport closed %d times; want 1 and 0", applied, ct.closes.Load())
		}
		// The transport is still usable: a Bus on it works.
		b, err := canbus.NewBus(ct)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Close(); err != nil || ct.closes.Load() != 1 {
			t.Errorf("Close: %v, transport closed %d times", err, ct.closes.Load())
		}
	})
}
