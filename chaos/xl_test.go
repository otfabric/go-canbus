// SPDX-License-Identifier: MIT

package chaos

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/virtual"
)

// xlBridge bridges two virtual buses whose ports carry CAN XL (or not,
// for the destination side when dstXL is false).
func xlBridge(t *testing.T, cfg Config, dstXL bool) (*Bridge, *virtual.Port, *virtual.Port, func()) {
	t.Helper()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	x, y := virtual.New(virtual.Config{Name: "x"}), virtual.New(virtual.Config{Name: "y"})
	xb, _ := x.Open(virtual.WithXL(), virtual.WithQueueSize(8192))
	xn, _ := x.Open(virtual.WithXL(), virtual.WithQueueSize(8192))
	yopt := virtual.WithFD()
	if dstXL {
		yopt = virtual.WithXL()
	}
	yb, _ := y.Open(yopt, virtual.WithQueueSize(8192))
	yn, _ := y.Open(virtual.WithXL(), virtual.WithQueueSize(8192))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx, xb, yb) }()
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		x.Close()
		y.Close()
	}
	return b, xn, yn, stop
}

func TestXLForwardedWithFaults(t *testing.T) {
	leakcheck.Check(t)
	b, src, dst, stop := xlBridge(t, Config{Seed: 3, Drop: 0.1, Duplicate: 0.1, Reorder: 0.1, Corrupt: 0.2}, true)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const n = 500
	payload := func(i int) []byte {
		d := bytes.Repeat([]byte{0}, 64+i)
		binary.BigEndian.PutUint16(d, uint16(i))
		return d
	}
	for i := 0; i < n; i++ {
		if err := src.SendXL(ctx, &canbus.XLFrame{Priority: 0x300, VCID: 2, SDT: 1, AF: uint32(i), Data: payload(i)}); err != nil {
			t.Fatal(err)
		}
		f, _ := canbus.NewStandard(0x100, []byte{byte(i)})
		_ = src.Send(ctx, &f)
	}
	waitStats(t, b, 2*n)
	st := b.Stats()
	if st.Dropped == 0 || st.Duplicated == 0 || st.Reordered == 0 || st.Corrupted == 0 || st.SkippedXL != 0 {
		t.Fatalf("faults not exercised: %+v", st)
	}
	var xls, flipped int
	for {
		var f canbus.Frame
		var x canbus.XLFrame
		short, done := context.WithTimeout(ctx, 100*time.Millisecond)
		isXL, err := dst.ReceiveAny(short, &f, &x)
		done()
		if err != nil {
			break
		}
		if !isXL {
			continue
		}
		xls++
		i := int(x.AF)
		if x.Priority != 0x300 || x.VCID != 2 || len(x.Data) != 64+i {
			t.Fatalf("frame %d header changed: %v", i, x)
		}
		if !bytes.Equal(x.Data, payload(i)) {
			flipped++
		}
	}
	if xls == 0 || flipped == 0 {
		t.Fatalf("%d CAN XL frames, %d corrupted", xls, flipped)
	}
}

func waitStats(t *testing.T, b *Bridge, received uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for b.Stats().Received < received {
		if time.Now().After(deadline) {
			t.Fatalf("bridge stats %+v", b.Stats())
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
}

func TestXLFilterAndSkipped(t *testing.T) {
	leakcheck.Check(t)
	// Frames outside the filter pass untouched, even with every frame
	// dropped otherwise.
	b, src, dst, stop := xlBridge(t, Config{Drop: 1, Filter: &canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(0x7FF)}}}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = src.SendXL(ctx, &canbus.XLFrame{Priority: 0x123, Data: []byte{1}})
	var x canbus.XLFrame
	if err := dst.ReceiveXL(ctx, &x); err != nil || x.Priority != 0x123 {
		t.Fatalf("unfiltered frame: %v %v", x, err)
	}
	_ = src.SendXL(ctx, &canbus.XLFrame{Priority: 0x7FF, Data: []byte{1}})
	waitStats(t, b, 2)
	if st := b.Stats(); st.Dropped != 1 || st.Forwarded != 1 {
		t.Fatalf("stats %+v", st)
	}
	stop()

	// A destination without CAN XL counts the frames it cannot take.
	b, src, dst, stop = xlBridge(t, Config{}, false)
	defer stop()
	_ = src.SendXL(ctx, &canbus.XLFrame{Priority: 1, Data: []byte{1}})
	f, _ := canbus.NewStandard(0x1, []byte{1})
	_ = src.Send(ctx, &f)
	var g canbus.Frame
	if _, err := dst.ReceiveAny(ctx, &g, &x); err != nil || g.ID != 1 {
		t.Fatalf("classic frame: %v %v", g, err)
	}
	waitStats(t, b, 2)
	if st := b.Stats(); st.SkippedXL != 1 || st.Forwarded != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// classicOnly hides a port's CAN XL methods.
type classicOnly struct{ canbus.Transport }

func TestXLToNonXLTransport(t *testing.T) {
	leakcheck.Check(t)
	b, _ := New(Config{Reorder: 1})
	x, y := virtual.New(virtual.Config{}), virtual.New(virtual.Config{})
	defer x.Close()
	defer y.Close()
	xb, _ := x.Open(virtual.WithXL())
	xn, _ := x.Open(virtual.WithXL())
	yb, _ := y.Open()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx, xb, classicOnly{yb}) }()
	_ = xn.SendXL(context.Background(), &canbus.XLFrame{Data: []byte{1}})
	_ = xn.SendXL(context.Background(), &canbus.XLFrame{Data: []byte{2}})
	waitStats(t, b, 2)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := b.Stats(); st.SkippedXL != 2 || st.Reordered != 1 {
		t.Fatalf("stats %+v", st)
	}
}
