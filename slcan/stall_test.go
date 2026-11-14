// SPDX-License-Identifier: MIT

package slcan

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/isotp"
)

// within runs fn and fails the test if it does not return within d.
func within(t *testing.T, d time.Duration, what string, fn func() error) error {
	t.Helper()
	ch := make(chan error, 1)
	start := time.Now()
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		t.Fatalf("%s still blocked after %v", what, time.Since(start))
		return nil
	}
}

// noDeadlines hides the write deadlines of a stream.
type noDeadlines struct{ io.ReadWriteCloser }

// TestStalledPeer: a peer that stops reading holds up neither Send past
// its context nor Close.
func TestStalledPeer(t *testing.T) {
	leakcheck.Check(t)
	f, _ := canbus.NewStandard(0x123, []byte{1})
	t.Run("Send ends at its deadline", func(t *testing.T) {
		c, _ := newPeer(t, Config{})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := within(t, time.Second, "Send", func() error { return c.Send(ctx, &f) })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if c.Stats().TxErrors != 1 {
			t.Fatal(c.Stats())
		}
		if err := within(t, 500*time.Millisecond, "Close", c.Close); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Send ends at cancellation", func(t *testing.T) {
		c, _ := newPeer(t, Config{})
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(30*time.Millisecond, cancel)
		err := within(t, time.Second, "Send", func() error { return c.Send(ctx, &f) })
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("Send waiting behind a blocked Send", func(t *testing.T) {
		c, _ := newPeer(t, Config{})
		blocked := make(chan error, 1)
		go func() { blocked <- c.Send(context.Background(), &f) }()
		time.Sleep(20 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if err := within(t, time.Second, "Send", func() error { return c.Send(ctx, &f) }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if err := within(t, 500*time.Millisecond, "Close", c.Close); err != nil {
			t.Fatal(err)
		}
		if err := <-blocked; !errors.Is(err, canbus.ErrClosed) {
			t.Fatal(err)
		}
	})
	t.Run("Close with a Send blocked without deadline", func(t *testing.T) {
		c, _ := newPeer(t, Config{})
		blocked := make(chan error, 1)
		go func() { blocked <- c.Send(context.Background(), &f) }()
		time.Sleep(20 * time.Millisecond)
		if err := within(t, 500*time.Millisecond, "Close", c.Close); err != nil {
			t.Fatal(err)
		}
		if err := <-blocked; !errors.Is(err, canbus.ErrClosed) {
			t.Fatal(err)
		}
	})
	t.Run("stream without write deadlines", func(t *testing.T) {
		a, b := net.Pipe()
		defer b.Close()
		c, err := New(noDeadlines{a}, Config{NoCommands: true})
		if err != nil {
			t.Fatal(err)
		}
		blocked := make(chan error, 1)
		go func() { blocked <- c.Send(context.Background(), &f) }()
		time.Sleep(20 * time.Millisecond)
		if err := within(t, 500*time.Millisecond, "Close", c.Close); err != nil {
			t.Fatal(err)
		}
		if err := <-blocked; !errors.Is(err, canbus.ErrClosed) {
			t.Fatal(err)
		}
	})
	t.Run("closing C to a stream without write deadlines", func(t *testing.T) {
		a, b := net.Pipe()
		defer b.Close()
		c, err := New(noDeadlines{a}, Config{NoCommands: true})
		if err != nil {
			t.Fatal(err)
		}
		c.cfg.NoCommands = false // as if the setup had been read
		start := time.Now()
		if err := within(t, 500*time.Millisecond, "Close", c.Close); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d < closeWriteTimeout {
			t.Fatalf("Close returned after %v, before the closing C had its %v", d, closeWriteTimeout)
		}
	})
	t.Run("Bus and isotp on a stalled stream", func(t *testing.T) {
		c, p := newPeer(t, Config{})
		bus, err := canbus.NewBus(c)
		if err != nil {
			t.Fatal(err)
		}
		ic, err := isotp.Dial(bus, isotp.Config{TxID: 0x7E0, RxID: 0x7E8, TimeoutBs: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		p.write(t, "t7E881014010203040506\r") // first frame: the flow control write stalls
		time.Sleep(50 * time.Millisecond)
		if err := within(t, 500*time.Millisecond, "isotp Close", ic.Close); err != nil {
			t.Fatal(err)
		}
		if err := within(t, 500*time.Millisecond, "Bus Close", bus.Close); err != nil {
			t.Fatal(err)
		}
	})
}

// TestWriteCutShort: after a write that ended part way through a record,
// the next record starts with "\r" so the adapter drops the fragment.
func TestWriteCutShort(t *testing.T) {
	leakcheck.Check(t)
	c, p := newPeer(t, Config{})
	f, _ := canbus.NewStandard(0x123, []byte{1})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- c.Send(ctx, &f) }()
	frag := make([]byte, 3) // read past bufio: the writer sees 3 bytes taken
	if _, err := io.ReadFull(p.conn, frag); err != nil || string(frag) != "t12" {
		t.Fatalf("%q, %v", frag, err)
	}
	if err := <-errc; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	go func() { errc <- c.Send(context.Background(), &f) }()
	if s := p.record(t); s != "" {
		t.Fatalf("fragment not ended: %q", s)
	}
	if s := p.record(t); s != "t123101" {
		t.Fatalf("record %q", s)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}
