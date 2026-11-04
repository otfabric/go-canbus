// SPDX-License-Identifier: MIT

package isotp

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/virtual"
)

// hostile sets up one Conn (Tx 0x7E0, Rx 0x7E8) and a raw port that plays
// the peer.
func hostile(t testing.TB, cfg Config) (*Conn, *virtual.Port) {
	t.Helper()
	vb := virtual.New(virtual.Config{})
	port, err := vb.Open(virtual.WithFD(), virtual.WithQueueSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	bus, err := canbus.NewBus(port)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TxID, cfg.RxID = 0x7E0, 0x7E8
	c, err := Dial(bus, cfg)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := vb.Open(virtual.WithFD(), virtual.WithQueueSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); bus.Close(); vb.Close() })
	return c, peer
}

// peerSend sends a frame with the given payload to the Conn.
func peerSend(t testing.TB, p *virtual.Port, d []byte) {
	t.Helper()
	var f canbus.Frame
	f.ID = 0x7E8
	if len(d) > 64 {
		d = d[:64]
	}
	f.SetPayload(d)
	if len(d) > 8 {
		f.Flags = canbus.FlagFD
		f.Len = uint8(canbus.RoundUpFDLen(len(d)))
	}
	if err := p.Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

// peerNext returns the next frame the Conn sent.
func peerNext(ctx context.Context, p *virtual.Port) (canbus.Frame, error) {
	for {
		var f canbus.Frame
		if err := p.Receive(ctx, &f); err != nil {
			return f, err
		}
		if f.ID == 0x7E0 {
			return f, nil
		}
	}
}

// sendAgainst runs Send of n bytes; when the first frame arrives at the
// peer, the peer answers with fcs.
func sendAgainst(t *testing.T, cfg Config, n int, fcs ...[]byte) error {
	t.Helper()
	c, p := hostile(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- c.Send(ctx, payload(n)) }()
	f, err := peerNext(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if f.Data[0]>>4 != pciFF {
		t.Fatalf("first frame expected, got % X", f.Payload())
	}
	for _, fc := range fcs {
		peerSend(t, p, fc)
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		t.Fatal("Send hung")
		return nil
	}
}

// TestHostileFlowControl drives the sender with flow controls a broken or
// hostile receiver could send.
func TestHostileFlowControl(t *testing.T) {
	leakcheck.Check(t)
	zero := time.Duration(0)
	base := Config{TimeoutBs: 50 * time.Millisecond, ForceTxSTmin: &zero, MaxWait: 3}
	wait := []byte{0x31, 0, 0}
	t.Run("wait flood", func(t *testing.T) {
		var fcs [][]byte
		for range 200 { // more than the 64-entry flow control buffer
			fcs = append(fcs, wait)
		}
		if err := sendAgainst(t, base, 100, fcs...); !errors.Is(err, ErrWaitLimit) {
			t.Fatal(err)
		}
	})
	t.Run("waits within MaxWait then CTS", func(t *testing.T) {
		if err := sendAgainst(t, base, 100, wait, wait, wait, []byte{0x30, 0, 0}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		if err := sendAgainst(t, base, 100, []byte{0x32, 0, 0}); !errors.Is(err, ErrOverflow) {
			t.Fatal(err)
		}
	})
	t.Run("reserved flow status", func(t *testing.T) {
		for _, st := range []byte{3, 7, 0xF} {
			if err := sendAgainst(t, base, 100, []byte{0x30 | st, 0, 0}); !errors.Is(err, ErrProtocol) {
				t.Fatalf("status %d: %v", st, err)
			}
		}
	})
	t.Run("short flow control is ignored", func(t *testing.T) {
		if err := sendAgainst(t, base, 100, []byte{0x30}, []byte{0x30, 0}); !errors.Is(err, ErrTimeout) {
			t.Fatal(err)
		}
	})
	t.Run("block size then silence", func(t *testing.T) {
		// CTS for one block of 2, then nothing: N_Bs for the next block.
		if err := sendAgainst(t, base, 100, []byte{0x30, 2, 0}); !errors.Is(err, ErrTimeout) {
			t.Fatal(err)
		}
	})
	t.Run("reserved STmin is 127 ms", func(t *testing.T) {
		cfg := base
		cfg.ForceTxSTmin = nil
		start := time.Now()
		if err := sendAgainst(t, cfg, 27, []byte{0x30, 0, 0xFA}); err != nil { // FF + 3 CFs, 2 gaps
			t.Fatal(err)
		}
		if d := time.Since(start); d < 2*127*time.Millisecond {
			t.Fatalf("reserved STmin not treated as 127 ms: %v", d)
		}
	})
}

// TestHostileFirstFrames: first frames announcing lengths a receiver must
// refuse or ignore.
func TestHostileFirstFrames(t *testing.T) {
	leakcheck.Check(t)
	ctx := ctxT(t)
	c, p := hostile(t, Config{MaxMessageSize: 5000, TimeoutCr: 50 * time.Millisecond})
	expectFC := func(status byte) {
		t.Helper()
		f, err := peerNext(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if f.Data[0] != 0x30|status {
			t.Fatalf("flow control % X, want status %d", f.Payload(), status)
		}
	}
	// Escape announcing 2^32-1 bytes: OVERFLOW and ErrTooLarge.
	peerSend(t, p, []byte{0x10, 0, 0xFF, 0xFF, 0xFF, 0xFF, 1, 2})
	expectFC(fcOverflow)
	if _, err := c.Receive(ctx); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	// 12-bit length beyond MaxMessageSize is impossible (4095 < 5000), so
	// use an escape just above it.
	ff := []byte{0x10, 0, 0, 0, 0, 0, 1, 2}
	binary.BigEndian.PutUint32(ff[2:], 5001)
	peerSend(t, p, ff)
	expectFC(fcOverflow)
	if _, err := c.Receive(ctx); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	// Escape with a length that fits 12 bits, and a first frame whose
	// length fits a single frame: both ignored, no flow control.
	binary.BigEndian.PutUint32(ff[2:], 4095)
	peerSend(t, p, ff)
	peerSend(t, p, []byte{0x10, 0x05, 1, 2, 3, 4, 5, 6})
	// A consecutive frame without a reception is ignored.
	peerSend(t, p, []byte{0x21, 1, 2, 3, 4, 5, 6, 7})
	// A consecutive frame after N_Cr expired is ignored as well.
	peerSend(t, p, []byte{0x10, 0x14, 0, 1, 2, 3, 4, 5})
	expectFC(fcCTS)
	if _, err := c.Receive(ctx); !errors.Is(err, ErrTimeout) {
		t.Fatal("N_Cr:", err)
	}
	peerSend(t, p, []byte{0x21, 6, 7, 8, 9, 10, 11, 12})
	// Every sequence number but the expected one is refused.
	for sn := byte(0); sn < 16; sn++ {
		if sn == 1 {
			continue
		}
		peerSend(t, p, []byte{0x10, 0x14, 0, 1, 2, 3, 4, 5})
		expectFC(fcCTS)
		peerSend(t, p, []byte{0x20 | sn, 6, 7, 8, 9, 10, 11, 12})
		if _, err := c.Receive(ctx); !errors.Is(err, ErrSequence) {
			t.Fatalf("SN %d: %v", sn, err)
		}
	}
	// The connection still works.
	peerSend(t, p, []byte{0x02, 0xAB, 0xCD})
	if got, err := c.Receive(ctx); err != nil || len(got) != 2 || got[0] != 0xAB {
		t.Fatal(got, err)
	}
}

// TestFirstFrameDoesNotPreallocate is the regression test for a first
// frame that announced 2^32-1 bytes to a receiver accepting that much: the
// receiver allocated the announced length up front, so one 8-byte frame
// cost 4 GiB.
func TestFirstFrameDoesNotPreallocate(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("MaxMessageSize 2^32-1 needs a 64-bit int")
	}
	leakcheck.Check(t)
	ctx := ctxT(t)
	maxSize := int64(1)<<32 - 1
	c, p := hostile(t, Config{MaxMessageSize: int(maxSize), TimeoutCr: 50 * time.Millisecond})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	peerSend(t, p, []byte{0x10, 0, 0xFF, 0xFF, 0xFF, 0xFF, 1, 2})
	if f, err := peerNext(ctx, p); err != nil || f.Data[0] != 0x30 {
		t.Fatal("no CTS:", err)
	}
	if _, err := c.Receive(ctx); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if d := after.TotalAlloc - before.TotalAlloc; d > 64<<20 {
		t.Fatalf("a first frame cost %d bytes of allocation", d)
	}
}

// FuzzSenderFlowControl drives a sender with arbitrary frames on its
// receive identifier once its first frame is out: Send must return (done
// or with an error) and never hang or panic.
func FuzzSenderFlowControl(f *testing.F) {
	f.Add([]byte{0x30, 0, 0}, 100)
	f.Add([]byte{0x31, 0, 0, 0x31, 0, 0, 0x30, 2, 0, 0x30, 0, 0}, 100)
	f.Add([]byte{0x32, 0, 0}, 5000)
	f.Add([]byte{0x3F, 0xFF, 0xFF}, 8)
	f.Fuzz(func(t *testing.T, script []byte, n int) {
		n = 8 + abs(n)%600
		zero := time.Duration(0)
		c, p := hostile(t, Config{TimeoutBs: 10 * time.Millisecond, ForceTxSTmin: &zero, MaxMessageSize: 1 << 12, MaxWait: 4})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		errc := make(chan error, 1)
		go func() { errc <- c.Send(ctx, payload(min(n, 4095))) }()
		if _, err := peerNext(ctx, p); err != nil {
			t.Fatal(err)
		}
		for len(script) > 0 {
			k := min(len(script), 3)
			peerSend(t, p, script[:k])
			script = script[k:]
		}
		err := <-errc
		switch {
		case err == nil, errors.Is(err, ErrTimeout), errors.Is(err, ErrWaitLimit), errors.Is(err, ErrOverflow), errors.Is(err, ErrProtocol):
		default:
			t.Fatal(err)
		}
	})
}

func abs(n int) int {
	if n < 0 {
		return -(n + 1)
	}
	return n
}
