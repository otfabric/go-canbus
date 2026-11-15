// SPDX-License-Identifier: MIT

package slcan

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/otfabric/go-canbus"
)

// TestCloseConcurrently (L1, L2): Close returns nil every time, one call
// after the other and from several goroutines at once, with a Receive
// waiting; the stream is closed once and the closing "C" is written once.
// It runs in a testing/synctest bubble: a Conn on a net.Pipe blocks on
// channels only.
func TestCloseConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := net.Pipe()
		defer b.Close()
		stream := &countedCloser{ReadWriteCloser: a}
		written := make(chan string, 1)
		go func() {
			bs, _ := io.ReadAll(b) // what the Conn writes until it closes the stream
			written <- string(bs)
		}()
		c, err := New(stream, Config{})
		if err != nil {
			t.Fatal(err)
		}
		errc := make(chan error, 1)
		go func() { errc <- c.Receive(context.Background(), &canbus.Frame{}) }()
		synctest.Wait() // Receive waits for a frame
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { errs <- c.Close() })
		}
		wg.Wait()
		errs <- c.Close()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("Close: %v", err)
			}
		}
		if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Receive at Close: %v", err)
		}
		if got := <-written; got != "C\rO\rC\r" {
			t.Errorf("written: %q, want the setup and one closing C", got)
		}
		if n := stream.n; n != 1 {
			t.Errorf("the stream was closed %d times", n)
		}
	})
}

// countedCloser counts the Close calls of the stream it wraps. The
// counter is read after every Close has returned.
type countedCloser struct {
	io.ReadWriteCloser
	mu sync.Mutex
	n  int
}

func (c *countedCloser) Close() error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.ReadWriteCloser.Close()
}
