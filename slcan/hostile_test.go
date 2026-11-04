// SPDX-License-Identifier: MIT

package slcan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
)

// TestHostileAdapter: an adapter that emits garbage, overlong lines,
// invalid hex and state records with the largest counters. The Conn
// counts the bad records, delivers only valid frames and keeps working.
func TestHostileAdapter(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	c, p := newPeer(t, Config{NoCommands: true, QueueSize: 64})
	if err := c.SetFilters(canbus.FilterSet{Filters: []canbus.Filter{{ID: 0, Mask: 0}},
		ErrorMask: canbus.ErrClassController | canbus.ErrClassBusOff}); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		strings.Repeat("t", 256),  // exactly the line limit, then a bad record
		strings.Repeat("A", 5000), // far beyond it, no terminator for a while
		"t12G0",                   // invalid hex identifier
		"t1232GG",                 // invalid hex data
		"t123F",                   // classical DLC 15 without data
		"t1238001122",             // fewer bytes than the DLC
		"T1FFFFFF",                // 8-digit ID needs 8 digits + DLC
		"T200000000",              // 29-bit identifier out of range
		"d123G",                   // invalid FD DLC
		"r123F",                   // remote DLC beyond 8
		"sx000000",                // unknown state
		"sw99999",                 // short state record
		"sw9999999",               // long state record
		"sw-10000",                // non-decimal counter
		"e",                       // empty error record
		"\x00\x00\x00",            // NUL bytes
	}
	for _, s := range bad {
		p.write(t, s+"\r")
	}
	// Largest counters the record can carry: kept in State, wrapped in the
	// error frame (as in Linux).
	p.write(t, "sp999998\n\r")
	var f canbus.Frame
	if err := c.Receive(ctx, &f); err != nil || !f.IsError() {
		t.Fatal(f, err)
	}
	if st, tx, rx := c.State(); st != canbus.StatePassive || tx != 998 || rx != 999 {
		t.Fatalf("State = %v %d %d", st, tx, rx)
	}
	info, _ := canbus.ParseError(&f)
	if !info.CountersValid || info.TxErrors != 998&0xFF || info.RxErrors != 999&0xFF {
		t.Fatalf("counters %+v", info)
	}
	p.write(t, "t1231AB\r")
	if err := c.Receive(ctx, &f); err != nil || f.ID != 0x123 || f.Len != 1 || f.Data[0] != 0xAB {
		t.Fatal(f, err)
	}
	if got := c.Stats().RxFailure; got < uint64(len(bad)) {
		t.Fatalf("RxFailure = %d, want at least %d", got, len(bad))
	}
}

// chunkRW replays chunks as reads, then reports io.EOF; writes are
// discarded. over makes a read claim more bytes than the buffer holds.
type chunkRW struct {
	mu     sync.Mutex
	chunks [][]byte
	over   bool
}

func (r *chunkRW) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	if r.over {
		return len(p) + 10, nil
	}
	return n, nil
}

func (r *chunkRW) Write(p []byte) (int, error) { return len(p), nil }
func (r *chunkRW) Close() error                { return nil }

// drain receives until the stream ends and checks every frame.
func drain(t *testing.T, c *Conn) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n := 0
	for {
		var f canbus.Frame
		err := c.Receive(ctx, &f)
		if errors.Is(err, canbus.ErrInterfaceDown) {
			return n
		}
		if err != nil {
			t.Fatal(err)
		}
		if !f.IsError() {
			if err := f.Validate(); err != nil {
				t.Fatalf("invalid frame delivered: %v: %v", f, err)
			}
		}
		n++
	}
}

// TestReaderOverreports is the regression test for a reader that returns
// more bytes than it was given room for: the Conn sliced its buffer with
// the count and panicked.
func TestReaderOverreports(t *testing.T) {
	leakcheck.Check(t)
	rw := &chunkRW{chunks: [][]byte{[]byte("t1230\r")}, over: true}
	c, err := New(rw, Config{NoCommands: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	drain(t, c)
}

// FuzzConnStream feeds an arbitrary byte stream, split at arbitrary
// points, to a Conn: it must not panic or hang, deliver only valid frames,
// and end with the stream.
func FuzzConnStream(f *testing.F) {
	f.Add([]byte("t1230\rT1234567821122\rd12390011223344556677\r"), uint8(3))
	f.Add([]byte("sp999999\rsb000000\re1a\r"), uint8(1))
	f.Add(bytes.Repeat([]byte("x"), 600), uint8(0))
	f.Add([]byte("t12G0\r\n\a\x00r7FF8\r"), uint8(7))
	f.Fuzz(func(t *testing.T, stream []byte, cut uint8) {
		rw := &chunkRW{}
		step := int(cut) + 1
		for len(stream) > 0 {
			k := min(step, len(stream))
			rw.chunks = append(rw.chunks, stream[:k])
			stream = stream[k:]
		}
		c, err := New(rw, Config{NoCommands: true, QueueSize: 1 << 14})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetFilters(canbus.FilterSet{Filters: []canbus.Filter{{}}, ErrorMask: canbus.ErrClassAll})
		drain(t, c)
		if err := c.Close(); err != nil && !errors.Is(err, canbus.ErrClosed) {
			t.Fatal(err)
		}
	})
}
