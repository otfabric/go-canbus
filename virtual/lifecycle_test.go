// SPDX-License-Identifier: MIT

package virtual

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
)

// The lifecycle contract tests of Bus and Port (TESTING.md, rows L1 to
// L16). They run in a testing/synctest bubble: the bus and its ports block
// on channels and timers only, so synctest.Wait returns when every
// goroutine waits (the test decides the interleaving, not a sleep) and
// deadlines take fake time.

// fullBus returns a bus whose delivery has stopped at a blocking port that
// nobody reads, with its queue full: the next Send waits for room. tx is a
// port to send from, rx the port that holds the bus up.
func fullBus(t *testing.T) (b *Bus, tx, rx *Port) {
	t.Helper()
	b = New(Config{QueueSize: 1})
	rx, _ = b.Open(WithBlocking(), WithQueueSize(1))
	tx, _ = b.Open()
	// One frame in rx's queue, one held by the dispatcher, one in the bus
	// queue.
	for i := range 3 {
		f, _ := canbus.NewStandard(canbus.ID(i+1), nil)
		if err := tx.Send(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
	}
	synctest.Wait()
	return b, tx, rx
}

// blockedSend starts a Send of identifier id on p that waits (the bus is
// full) and returns its result channel once it does.
func blockedSend(p *Port, id canbus.ID) <-chan error {
	errc := make(chan error, 1)
	go func() {
		f, _ := canbus.NewStandard(id, nil)
		errc <- p.Send(context.Background(), &f)
	}()
	synctest.Wait()
	return errc
}

func notReturned(t *testing.T, what string, errc <-chan error) {
	t.Helper()
	synctest.Wait()
	select {
	case err := <-errc:
		t.Fatalf("%s returned: %v", what, err)
	default:
	}
}

// received returns the identifiers of the frames p has queued, read until
// a millisecond of fake time passes without one.
func received(p *Port) (ids []canbus.ID) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		var f canbus.Frame
		err := p.Receive(ctx, &f)
		cancel()
		if err != nil {
			return ids
		}
		ids = append(ids, f.ID)
	}
}

// TestSendWaitingFollowsContext (L6): on a full bus, a Send that waits for
// room and a Send that waits behind it each end at their own deadline,
// exactly; neither frame is delivered, and the bus carries on once the
// receiver reads.
func TestSendWaitingFollowsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, tx, rx := fullBus(t)
		defer b.Close()
		first := blockedSend(tx, 0x10) // waits for room, without a deadline
		other, _ := b.Open()

		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		f, _ := canbus.NewStandard(0x20, nil)
		if err := other.Send(ctx, &f); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Send behind a waiting Send: %v, want context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d != 50*time.Millisecond {
			t.Fatalf("Send behind a waiting Send returned after %v, want its 50ms", d)
		}
		// The same for a frame injected into the bus.
		ictx, icancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer icancel()
		if err := b.Inject(ictx, f); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 80*time.Millisecond {
			t.Fatalf("Inject behind a waiting Send: %v after %v", err, time.Since(start))
		}
		notReturned(t, "the Send without a deadline", first)
		// The receiver reads: everything queued is delivered, the waiting
		// Send included, and the two that ended are not.
		if got := received(rx); len(got) != 4 || got[3] != 0x10 {
			t.Fatalf("delivered %v, want the identifiers 1, 2, 3 and 0x10", got)
		}
		if err := <-first; err != nil {
			t.Fatalf("the Send without a deadline: %v", err)
		}
	})
}

// TestPortCloseEndsSend (L4): closing a port ends its Send that waits for
// room, and its Send that waits behind another port's, with ErrClosed; the
// other port's Send still waits, and the frame of a Send that ended is not
// delivered.
func TestPortCloseEndsSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, tx, rx := fullBus(t)
		defer b.Close()
		waiting := blockedSend(tx, 0x10)
		if err := tx.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-waiting:
			if !errors.Is(err, canbus.ErrClosed) {
				t.Fatalf("Send that waited for room, at Port.Close: %v, want ErrClosed", err)
			}
		default:
			t.Fatal("Port.Close did not end the Send that waited for room")
		}

		// A Send of another port holds the turn; a Send of p waits behind it.
		holder, _ := b.Open()
		p, _ := b.Open()
		held := blockedSend(holder, 0x20)
		behind := blockedSend(p, 0x30)
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-behind:
			if !errors.Is(err, canbus.ErrClosed) {
				t.Fatalf("Send that waited for its turn, at Port.Close: %v, want ErrClosed", err)
			}
		default:
			t.Fatal("Port.Close did not end the Send that waited for its turn")
		}
		notReturned(t, "the Send of the port that stays open", held)
		if got := received(rx); len(got) != 4 || got[3] != 0x20 {
			t.Fatalf("delivered %v, want the identifiers 1, 2, 3 and 0x20", got)
		}
		if err := <-held; err != nil {
			t.Fatal(err)
		}
	})
}

// TestBusCloseEndsSend (L4): closing the bus ends a Send that waits for
// room and a Send that waits behind it, with ErrClosed.
func TestBusCloseEndsSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, tx, _ := fullBus(t)
		other, _ := b.Open()
		first := blockedSend(tx, 0x10)
		second := blockedSend(other, 0x20)
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		for name, errc := range map[string]<-chan error{"the Send that waited for room": first, "the Send behind it": second} {
			if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
				t.Errorf("%s, at Bus.Close: %v, want ErrClosed", name, err)
			}
		}
	})
}

// TestCloseTwice (L1, L2, L3): Close of a Port and of a Bus returns nil
// every time, one call after the other and from several goroutines at
// once; Close of a port ends its Receive that waits, and only its own.
func TestCloseTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := New(Config{})
		p, _ := b.Open()
		q, _ := b.Open()
		type result struct {
			port *Port
			err  error
		}
		results := make(chan result, 2)
		for _, port := range []*Port{p, q} {
			go func() {
				var f canbus.Frame
				results <- result{port, port.Receive(context.Background(), &f)}
			}()
		}
		synctest.Wait() // both calls wait for a frame
		errs := make(chan error, 16)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { errs <- p.Close() })
		}
		wg.Wait()
		if r := <-results; r.port != p || !errors.Is(r.err, canbus.ErrClosed) {
			t.Fatalf("Receive at Port.Close: %v (the closed port's: %v)", r.err, r.port == p)
		}
		synctest.Wait()
		select {
		case r := <-results:
			t.Fatalf("Close of one port ended the Receive of another: %v", r.err)
		default:
		}
		for range 4 {
			wg.Go(func() { errs <- b.Close() })
		}
		wg.Wait()
		errs <- b.Close()
		errs <- p.Close()
		errs <- q.Close() // closed by the bus already
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("Close: %v", err)
			}
		}
		if r := <-results; r.port != q || !errors.Is(r.err, canbus.ErrClosed) {
			t.Fatalf("Receive at Bus.Close: %v", r.err)
		}
		f, _ := canbus.NewStandard(1, nil)
		if err := q.Send(context.Background(), &f); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Send after Close: %v", err)
		}
		if _, err := b.Open(); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Open after Close: %v", err)
		}
	})
}
