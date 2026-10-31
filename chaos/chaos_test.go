// SPDX-License-Identifier: MIT

package chaos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/virtual"
)

// segment is one side of a bridge: a virtual bus with the bridge's port
// and a node's port.
type segment struct {
	bus    *virtual.Bus
	bridge *virtual.Port
	node   *virtual.Port
}

func newSegment(t *testing.T, name string) segment {
	t.Helper()
	b := virtual.New(virtual.Config{Name: name})
	t.Cleanup(func() { b.Close() })
	br, _ := b.Open(virtual.WithFD(), virtual.WithQueueSize(8192))
	n, _ := b.Open(virtual.WithFD(), virtual.WithQueueSize(8192))
	return segment{b, br, n}
}

func run(t *testing.T, cfg Config) (*Bridge, segment, segment, context.CancelFunc) {
	t.Helper()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	x, y := newSegment(t, "x"), newSegment(t, "y")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Both(ctx, x.bridge, y.bridge) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return b, x, y, cancel
}

func sendSeq(t *testing.T, p *virtual.Port, n int) {
	for i := 0; i < n; i++ {
		f, _ := canbus.NewStandard(0x100, []byte{byte(i >> 8), byte(i)})
		if err := p.Send(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
	}
}

func collect(p *virtual.Port, quiet time.Duration) []int {
	var out []int
	for {
		ctx, cancel := context.WithTimeout(context.Background(), quiet)
		var f canbus.Frame
		err := p.Receive(ctx, &f)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, int(f.Data[0])<<8|int(f.Data[1]))
	}
}

func TestTransparent(t *testing.T) {
	leakcheck.Check(t)
	b, x, y, _ := run(t, Config{})
	sendSeq(t, x.node, 200)
	got := collect(y.node, 100*time.Millisecond)
	if len(got) != 200 {
		t.Fatalf("%d of 200", len(got))
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("order: %d at %d", v, i)
		}
	}
	sendSeq(t, y.node, 3) // the other direction
	if got := collect(x.node, 100*time.Millisecond); len(got) != 3 {
		t.Fatal(got)
	}
	if st := b.Stats(); st.Received != 203 || st.Forwarded != 203 {
		t.Fatalf("%+v", st)
	}
}

func TestFaultsAccounting(t *testing.T) {
	b, x, y, _ := run(t, Config{Seed: 9, Drop: 0.1, Duplicate: 0.1, Reorder: 0.05, Corrupt: 0.05, Delay: 0.02, DelayBy: time.Millisecond})
	sendSeq(t, x.node, 1000)
	got := collect(y.node, 200*time.Millisecond)
	st := b.Stats()
	if st.Dropped == 0 || st.Duplicated == 0 || st.Reordered == 0 || st.Corrupted == 0 || st.Delayed == 0 {
		t.Fatalf("faults not applied: %+v", st)
	}
	if uint64(len(got)) != st.Forwarded || st.Received != 1000 || st.Forwarded != 1000-st.Dropped+st.Duplicated {
		t.Fatalf("got %d, %+v", len(got), st)
	}
}

func TestDeterministic(t *testing.T) {
	pattern := func() []int {
		_, x, y, _ := run(t, Config{Seed: 4, Drop: 0.2, Duplicate: 0.2, Reorder: 0.1})
		sendSeq(t, x.node, 300)
		return collect(y.node, 150*time.Millisecond)
	}
	a, b := pattern(), pattern()
	if len(a) != len(b) {
		t.Fatalf("%d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("differ at %d", i)
		}
	}
}

func TestFilterLimitsFaults(t *testing.T) {
	b, x, y, _ := run(t, Config{Drop: 1, Filter: &canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(0x200)}}})
	sendSeq(t, x.node, 10) // 0x100: not selected, passes
	f, _ := canbus.NewStandard(0x200, nil)
	_ = x.node.Send(context.Background(), &f)
	if got := collect(y.node, 100*time.Millisecond); len(got) != 10 || b.Stats().Dropped != 1 {
		t.Fatal(len(got), b.Stats())
	}
}

func TestErrorsAndOptions(t *testing.T) {
	if _, err := New(Config{Drop: 1.5}); !errors.Is(err, canbus.ErrInvalidOption) {
		t.Fatal(err)
	}
	b, _ := New(Config{})
	// The source failing ends Run with its error; ErrClosed ends it quietly.
	x := newSegment(t, "x")
	y := newSegment(t, "y")
	x.bridge.Close()
	if err := b.Run(context.Background(), x.bridge, y.bridge); err != nil {
		t.Fatal(err)
	}
	// A failing destination ends Run with the send error.
	x2 := newSegment(t, "x2")
	classic := newSegment(t, "c")
	cp, _ := classic.bus.Open() // classical port: CAN FD frames fail
	fd, _ := canbus.NewFD(1, false, false, make([]byte, 12))
	go func() { _ = x2.node.Send(context.Background(), &fd) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.Run(ctx, x2.bridge, cp); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal(err)
	}
	// Error frames are not forwarded; a held (reordered) frame is
	// flushed when the bridge stops.
	r, _ := New(Config{Reorder: 1})
	x3, y3 := newSegment(t, "x3"), newSegment(t, "y3")
	x3b, _ := x3.bus.Open(virtual.WithFD(), virtual.WithFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll}))
	ctx3, cancel3 := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx3, x3b, y3.bridge) }()
	_ = x3.bus.InjectError(context.Background(), canbus.ErrorInfo{Class: canbus.ErrClassBusOff})
	sendSeq(t, x3.node, 1)
	time.Sleep(50 * time.Millisecond)
	cancel3()
	<-done
	if got := collect(y3.node, 100*time.Millisecond); len(got) != 1 {
		t.Fatal("held frame not flushed:", got)
	}
}

// edgeTransport fails Receive with a deadline error just before ctx's
// deadline, as a socket's file deadline can.
type edgeTransport struct{ canbus.Transport }

func (e edgeTransport) Receive(ctx context.Context, f *canbus.Frame) error {
	dl, _ := ctx.Deadline()
	time.Sleep(time.Until(dl))
	return context.DeadlineExceeded
}

func TestRunEndsQuietlyAtDeadline(t *testing.T) {
	b, _ := New(Config{})
	x := newSegment(t, "x")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Run(ctx, edgeTransport{x.bridge}, x.node); err != nil {
		t.Fatal(err)
	}
	if err := b.Run(context.Background(), failing{}, x.node); err == nil {
		t.Fatal("a real receive error must be returned")
	}
}

type failing struct{ canbus.Transport }

func (failing) Receive(context.Context, *canbus.Frame) error { return errors.New("boom") }
