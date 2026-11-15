// SPDX-License-Identifier: MIT

package slcan

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
)

// countingStream counts what a constructor does with the stream it owns.
type countingStream struct {
	writes, closes atomic.Int32
	writeErr       error
	unblock        chan struct{} // closed by Close: ends Read
}

func newCountingStream(writeErr error) *countingStream {
	return &countingStream{writeErr: writeErr, unblock: make(chan struct{})}
}

func (s *countingStream) Read([]byte) (int, error) {
	<-s.unblock
	return 0, os.ErrClosed
}

func (s *countingStream) Write(p []byte) (int, error) {
	s.writes.Add(1)
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return len(p), nil
}

func (s *countingStream) Close() error {
	if s.closes.Add(1) == 1 {
		close(s.unblock)
	}
	return nil
}

// openDescriptors returns the number of open file descriptors of the
// process, or false where it cannot be counted.
func openDescriptors() (int, bool) {
	es, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(es), true
}

// TestConstructionFailure (contract L14): a constructor that returns an
// error leaves nothing behind. New closes the stream it owns once, on a
// configuration it cannot use (before it writes anything) and on a setup
// command that fails; Open checks the configuration before it opens the
// port, so no descriptor stays open; no goroutine is left in either case.
func TestConstructionFailure(t *testing.T) {
	leakcheck.Check(t)
	for _, c := range []struct {
		name   string
		cfg    Config
		writes int32
	}{
		{"bit rate", Config{Bitrate: 42}, 0},
		{"bit rate, listen only", Config{Bitrate: -1, ListenOnly: true}, 0},
		{"data bit rate", Config{Bitrate: 500000, DataBitrate: 4000000}, 0},
	} {
		s := newCountingStream(nil)
		conn, err := New(s, c.cfg)
		if conn != nil || !errors.Is(err, canbus.ErrInvalidOption) {
			t.Errorf("%s: %v, %v", c.name, conn, err)
		}
		if w, cl := s.writes.Load(), s.closes.Load(); w != c.writes || cl != 1 {
			t.Errorf("%s: %d writes and %d closes of the stream, want %d and 1", c.name, w, cl, c.writes)
		}
	}
	// A setup command that fails: the first write, then the close.
	refused := errors.New("refused")
	s := newCountingStream(refused)
	if conn, err := New(s, Config{Bitrate: 500000}); conn != nil || !errors.Is(err, refused) {
		t.Errorf("failing stream: %v, %v", conn, err)
	}
	if w, cl := s.writes.Load(), s.closes.Load(); w != 1 || cl != 1 {
		t.Errorf("failing stream: %d writes and %d closes, want 1 and 1", w, cl)
	}
	// The configuration is not a reason to keep a valid one from starting:
	// with NoCommands the bit rates are not sent and not checked.
	s = newCountingStream(nil)
	conn, err := New(s, Config{NoCommands: true, Bitrate: 42})
	if err != nil || s.writes.Load() != 0 {
		t.Fatalf("NoCommands: %v, %d writes", err, s.writes.Load())
	}
	if err := conn.Close(); err != nil || s.closes.Load() != 1 {
		t.Errorf("NoCommands: Close %v, %d closes", err, s.closes.Load())
	}

	// Open on a serial port that exists.
	m, path, err := openPTY()
	if err != nil {
		t.Skip("pseudo-terminal:", err)
	}
	defer func() { _ = m.Close() }()
	before, ok := openDescriptors()
	if !ok {
		t.Skip("open descriptors cannot be counted on this platform")
	}
	for range 20 {
		for _, cfg := range []Config{{Bitrate: 42}, {DataBitrate: 4000000}} {
			if conn, err := Open(path, 0, cfg); conn != nil || !errors.Is(err, canbus.ErrInvalidOption) {
				t.Fatalf("Open(%+v): %v, %v", cfg, conn, err)
			}
		}
	}
	if after, _ := openDescriptors(); after != before {
		t.Errorf("%d descriptors open before 40 failed Open calls, %d after", before, after)
	}
}
