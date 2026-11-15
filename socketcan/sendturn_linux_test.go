// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/isotp"
)

// TestSendTurn (L4, L6): sends are serialized, and a Send or SendXL that
// waits for the one in progress ends at its own deadline or at Close,
// without writing. The Conn has no socket: the test holds the turn itself
// (as a send that retries on a full transmit queue holds it), and a call
// that went on to write would fail on the missing file. It needs no CAN
// interface and runs in a testing/synctest bubble, so the deadline takes
// fake time.
func TestSendTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &Conn{name: "none0", ifindex: 1, cfg: config{fd: true, xl: true},
			wsem: make(chan struct{}, 1), done: make(chan struct{}), xbuf: make([]byte, canXLMTU)}
		c.wsem <- struct{}{} // a send is in progress
		f, err := canbus.NewStandard(0x123, []byte{1})
		if err != nil {
			t.Fatal(err)
		}
		x := canbus.XLFrame{Priority: 1, Data: []byte{1}}

		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := c.Send(ctx, &f); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 50*time.Millisecond {
			t.Fatalf("Send behind a send: %v after %v, want context.DeadlineExceeded after 50ms", err, time.Since(start))
		}
		xctx, xcancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer xcancel()
		if err := c.SendXL(xctx, &x); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 70*time.Millisecond {
			t.Fatalf("SendXL behind a send: %v after %v, want context.DeadlineExceeded after 20ms more", err, time.Since(start))
		}
		cctx, ccancel := context.WithCancel(context.Background())
		errs := make(chan error, 3)
		go func() { errs <- c.Send(cctx, &f) }()
		synctest.Wait()
		ccancel()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Send behind a send: %v", err)
		}

		// Close ends the calls that wait for their turn.
		go func() { errs <- c.Send(context.Background(), &f) }()
		go func() { errs <- c.SendXL(context.Background(), &x) }()
		synctest.Wait()
		_ = c.Close() // no file to close
		for range 2 {
			if err := <-errs; !errors.Is(err, canbus.ErrClosed) {
				t.Errorf("send waiting at Close: %v, want ErrClosed", err)
			}
		}
		if err := c.Send(context.Background(), &f); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Send after Close: %v", err)
		}
		if len(c.wsem) != 1 {
			t.Errorf("the turn of the send in progress was taken by a call that ended")
		}
	})
	// Close together with a free turn: the turn is given back, whichever
	// the call sees first.
	for range 100 {
		c := &Conn{wsem: make(chan struct{}, 1), done: make(chan struct{})}
		c.closed.Store(true)
		close(c.done)
		if err := c.lockSend(context.Background()); !errors.Is(err, canbus.ErrClosed) || len(c.wsem) != 0 {
			t.Fatalf("lockSend on a closed Conn: %v, turn held %v", err, len(c.wsem) == 1)
		}
	}
	// A free turn is taken at once and given back by the caller.
	c := &Conn{wsem: make(chan struct{}, 1), done: make(chan struct{})}
	if err := c.lockSend(context.Background()); err != nil || len(c.wsem) != 1 {
		t.Fatalf("lockSend: %v", err)
	}
}

// TestCloseConcurrently (L1, L2): Close of a raw socket and of a kernel
// ISO-TP socket returns nil every time after the first, one call after
// the other and from several goroutines at once; a Receive that waits ends
// with ErrClosed.
func TestCloseConcurrently(t *testing.T) {
	ifc := testIface(t)
	closeAll := func(t *testing.T, closer interface{ Close() error }) {
		t.Helper()
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { errs <- closer.Close() })
		}
		wg.Wait()
		errs <- closer.Close()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("Close: %v", err)
			}
		}
	}
	t.Run("raw socket", func(t *testing.T) {
		c, err := Open(ifc, onlyID(uniqueID(t), true))
		if err != nil {
			t.Fatal(err)
		}
		errc := make(chan error, 1)
		go func() { errc <- c.Receive(context.Background(), &canbus.Frame{}) }()
		closeAll(t, c)
		if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Receive at Close: %v", err)
		}
	})
	t.Run("kernel ISO-TP socket", func(t *testing.T) {
		c := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6B0, RxID: 0x6B8})
		errc := make(chan error, 1)
		go func() { _, err := c.Receive(context.Background()); errc <- err }()
		closeAll(t, c)
		if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Receive at Close: %v", err)
		}
	})
}
