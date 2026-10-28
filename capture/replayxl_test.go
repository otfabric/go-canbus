// SPDX-License-Identifier: MIT

package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
)

const mixedLog = `(1700000000.000000) can0 123#01
(1700000000.010000) can0 05242#81:01:12345678#AABB
(1700000000.020000) can0 00243#80:00:00000000#CC
(1700000000.030000) can0 124#02
`

func TestReplayXL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	tx, _ := vb.Open(virtual.WithXL())
	rx, _ := vb.Open(virtual.WithXL())
	var sentXL []string
	st, err := Replay(ctx, NewCandumpReader(strings.NewReader(mixedLog)), tx, ReplayOptions{
		Speed:     1,
		OnXLFrame: func(x *canbus.XLFrame) { sentXL = append(sentXL, x.String()) },
	})
	if err != nil || st.Read != 4 || st.Sent != 4 || st.SkippedXL != 0 {
		t.Fatalf("stats %+v, %v", st, err)
	}
	if st.Elapsed < 25*time.Millisecond {
		t.Fatalf("timing not kept: %v", st.Elapsed)
	}
	if len(sentXL) != 2 || sentXL[0] != "05242#81:01:12345678#AABB" {
		t.Fatalf("OnXLFrame %v", sentXL)
	}
	var got []string
	for range 4 {
		var f canbus.Frame
		var x canbus.XLFrame
		isXL, err := rx.ReceiveAny(ctx, &f, &x)
		if err != nil {
			t.Fatal(err)
		}
		if isXL {
			got = append(got, x.String())
		} else {
			got = append(got, f.String())
		}
	}
	if strings.Join(got, " ") != "123#01 05242#81:01:12345678#AABB 00243#80:00:00000000#CC 124#02" {
		t.Fatalf("received %v", got)
	}

	// Filters select CAN XL frames by priority; a port without CAN XL
	// skips them, counted.
	fs := canbus.FilterSet{Filters: []canbus.Filter{{ID: 0x242, Mask: 0x7FF}, canbus.ExactStandard(0x123)}}
	st, err = Replay(ctx, NewCandumpReader(strings.NewReader(mixedLog)), tx, ReplayOptions{Filter: &fs})
	if err != nil || st.Sent != 2 || st.Filtered != 2 {
		t.Fatalf("filtered: %+v %v", st, err)
	}
	classic, _ := vb.Open(virtual.WithFD())
	st, err = Replay(ctx, NewCandumpReader(strings.NewReader(mixedLog)), classic, ReplayOptions{})
	if err != nil || st.Sent != 2 || st.SkippedXL != 2 {
		t.Fatalf("classic port: %+v %v", st, err)
	}
	// A sender without SendXL at all.
	st, err = Replay(ctx, NewCandumpReader(strings.NewReader(mixedLog)), sendOnly{classic}, ReplayOptions{})
	if err != nil || st.Sent != 2 || st.SkippedXL != 2 {
		t.Fatalf("sender without SendXL: %+v %v", st, err)
	}
	// Per-interface senders apply to CAN XL frames too.
	st, err = Replay(ctx, NewCandumpReader(strings.NewReader(mixedLog)), classic, ReplayOptions{Interfaces: map[string]Sender{"can0": tx}})
	if err != nil || st.Sent != 4 || st.SkippedXL != 0 {
		t.Fatalf("interface map: %+v %v", st, err)
	}
	// Send failures other than ErrUnsupported end the replay.
	_ = tx.Close()
	if _, err := Replay(ctx, NewCandumpReader(strings.NewReader("(1.0) can0 242#80:00:00000000#01\n")), tx, ReplayOptions{}); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("closed sender: %v", err)
	}
	// MaxDuration ends a replay waiting for a CAN XL frame.
	slow := "(1.0) can0 242#80:00:00000000#01\n(9.0) can0 242#80:00:00000000#02\n"
	st, err = Replay(ctx, NewCandumpReader(strings.NewReader(slow)), rx, ReplayOptions{Speed: 1, MaxDuration: 30 * time.Millisecond})
	if err != nil || !st.Limited || st.Sent != 1 {
		t.Fatalf("limited: %+v %v", st, err)
	}
}

type sendOnly struct{ s Sender }

func (o sendOnly) Send(ctx context.Context, f *canbus.Frame) error { return o.s.Send(ctx, f) }

func TestRecordAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	tx, _ := vb.Open(virtual.WithXL())
	p, _ := vb.Open(virtual.WithXL())
	bus, _ := canbus.NewBus(p)
	defer bus.Close()
	sub, _ := bus.Subscribe(canbus.WithXL())
	// Both queues hold frames before recording starts: the recording
	// keeps the order of arrival anyway (repeated, as a random choice
	// between the queues would pass now and then).
	for round := 0; round < 20; round++ {
		if _, err := Replay(ctx, NewCandumpReader(strings.NewReader(mixedLog)), tx, ReplayOptions{}); err != nil {
			t.Fatal(err)
		}
		for len(sub.Frames()) < 2 || len(sub.XLFrames()) < 2 {
			time.Sleep(time.Millisecond)
		}
		var b strings.Builder
		w := NewCandumpWriter(&b, "")
		n, err := RecordAll(ctx, sub, w, 4)
		_ = w.Close()
		if err != nil || n != 4 {
			t.Fatalf("recorded %d: %v", n, err)
		}
		var got []string
		for _, l := range strings.Split(strings.TrimSpace(b.String()), "\n") {
			got = append(got, strings.Fields(l)[2])
		}
		if strings.Join(got, " ") != "123#01 05242#81:01:12345678#AABB 00243#80:00:00000000#CC 124#02" {
			t.Fatalf("round %d: recorded %v", round, got)
		}
	}
	var b strings.Builder
	w := NewCandumpWriter(&b, "")
	// The end of the subscription ends the recording after the queue
	// drained; ctx ending returns no error.
	_ = tx.SendXL(ctx, &canbus.XLFrame{Data: []byte{1}})
	for len(sub.XLFrames()) == 0 {
		time.Sleep(time.Millisecond)
	}
	_ = sub.Close()
	if n, err := RecordAll(ctx, sub, w, 0); n != 1 || !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("after close: %d %v", n, err)
	}
	short, c2 := context.WithTimeout(ctx, 20*time.Millisecond)
	defer c2()
	sub2, _ := bus.Subscribe(canbus.WithXL())
	if n, err := RecordAll(short, sub2, w, 0); n != 0 || err != nil {
		t.Fatalf("ctx end: %d %v", n, err)
	}
	_ = tx.SendXL(ctx, &canbus.XLFrame{Data: []byte{1}})
	if n, err := RecordAll(ctx, sub2, failing{}, 0); err == nil || n != 0 {
		t.Fatalf("failing writer: %d %v", n, err)
	}
}

// failing is an XLWriter whose writes fail.
type failing struct{}

func (failing) Write(*canbus.Frame) error     { return errors.New("disk full") }
func (failing) WriteXL(*canbus.XLFrame) error { return errors.New("disk full") }
func (failing) Close() error                  { return nil }
