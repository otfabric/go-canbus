// SPDX-License-Identifier: MIT

package slcan

import (
	"testing"

	"github.com/otfabric/go-canbus"
)

// errFrame builds an expected error frame: class, then Data[0..7].
func errFrame(class canbus.ErrorClass, data ...byte) canbus.Frame {
	f := canbus.Frame{ID: canbus.ID(class), Flags: canbus.FlagError, Len: 8}
	copy(f.Data[:], data)
	return f
}

func sameErr(a, b canbus.Frame) bool {
	return a.ID == b.ID && a.Flags == b.Flags && a.Len == b.Len && a.Data == b.Data
}

// The expected frames are those of Linux's slcan_bump_err.
func TestParseErrorRecord(t *testing.T) {
	const prot = canbus.ErrClassProtocol | canbus.ErrClassBusError
	for _, c := range []struct {
		line string
		want canbus.Frame
	}{
		{"e1a", errFrame(prot|canbus.ErrClassNoAck, 0, 0, 0, 0x19)},
		{"e3bcO", errFrame(prot|canbus.ErrClassController, 0, 0x02, 0x09, 0x08)},
		{"e2fs", errFrame(prot, 0, 0, 0x06)},
		{"e1o", errFrame(prot|canbus.ErrClassController, 0, 0x01)},
		{"e1B", errFrame(prot, 0, 0, 0x10)},
		{"e2ca", errFrame(prot|canbus.ErrClassNoAck, 0, 0, 0x01, 0x19)}, // the last location wins
		{"e2oO", errFrame(prot|canbus.ErrClassController, 0, 0x02)},     // the last overrun wins
		{"e0Z", errFrame(prot)},
		{"e1aZZ", errFrame(prot|canbus.ErrClassNoAck, 0, 0, 0, 0x19)}, // trailing characters ignored
	} {
		var f canbus.Frame
		ok, err := ParseErrorRecord([]byte(c.line), &f)
		if !ok || err != nil || !sameErr(f, c.want) {
			t.Errorf("%s: %v %v %v, want %v", c.line, f, ok, err, c.want)
		}
	}
	for _, bad := range []string{"e", "e0", "e9abcdefghi", "ex", "e3ab", "e1x", "e2a?"} {
		var f canbus.Frame
		if ok, err := ParseErrorRecord([]byte(bad), &f); ok || err == nil {
			t.Errorf("%q: %v %v, want an error", bad, ok, err)
		}
	}
	var f canbus.Frame
	if ok, err := ParseErrorRecord([]byte("t1230"), &f); ok || err != nil {
		t.Errorf("a data record: %v %v", ok, err)
	}
}

// The expected frames are those of Linux's slcan_bump_state and
// can_change_state.
func TestStateRecords(t *testing.T) {
	const ctrl = canbus.ErrClassController | canbus.ErrClassCounters
	prev := canbus.StateActive
	for _, c := range []struct {
		line    string
		changed bool
		want    canbus.Frame
	}{
		{"sa000000", false, canbus.Frame{}},
		{"sw100050", true, errFrame(ctrl, 0, 0x04, 0, 0, 0, 0, 50, 100)},  // RX side
		{"sp127127", true, errFrame(ctrl, 0, 0x30, 0, 0, 0, 0, 127, 127)}, // tie: both sides
		{"sp000130", false, canbus.Frame{}},                               // same state, new counters
		{"sw000100", true, errFrame(ctrl, 0, 0x08, 0, 0, 0, 0, 100, 0)},   // TX side
		{"sp000300", true, errFrame(ctrl, 0, 0x20, 0, 0, 0, 0, 300-256)},  // wraps
		{"sa000000", true, errFrame(ctrl, 0, 0x40)},
		{"sb256256", true, errFrame(canbus.ErrClassBusOff)},
	} {
		st, tx, rx, ok, err := ParseStateRecord([]byte(c.line))
		if !ok || err != nil {
			t.Fatalf("%s: %v %v", c.line, ok, err)
		}
		f, changed := StateFrame(prev, st, tx, rx)
		if changed != c.changed || changed && !sameErr(f, c.want) {
			t.Errorf("%s from %v: %v %v, want %v %v", c.line, prev, f, changed, c.want, c.changed)
		}
		prev = st
	}
	for _, bad := range []string{"s", "sa00000", "sa0000000", "sx000000", "sa0a0000", "sa000-00"} {
		if _, _, _, ok, err := ParseStateRecord([]byte(bad)); ok || err == nil {
			t.Errorf("%q: %v %v, want an error", bad, ok, err)
		}
	}
	if _, _, _, ok, err := ParseStateRecord([]byte("t1230")); ok || err != nil {
		t.Errorf("a data record: %v %v", ok, err)
	}
}

func TestConnStatus(t *testing.T) {
	c, p := newPeer(t, Config{NoCommands: true})
	ctx := ctxT(t)
	if !c.Capabilities().ErrorFrames {
		t.Fatal("no ErrorFrames capability")
	}
	// No error mask: error frames are not delivered, the state is kept.
	p.write(t, "e1a\rsw100050\rt1230\r")
	var f canbus.Frame
	if err := c.Receive(ctx, &f); err != nil || f.IsError() || f.ID != 0x123 {
		t.Fatalf("got %v %v, want 123#", f, err)
	}
	if st, tx, rx := c.State(); st != canbus.StateWarning || tx != 50 || rx != 100 {
		t.Fatalf("State = %v %d %d", st, tx, rx)
	}
	if err := c.SetFilters(canbus.FilterSet{Filters: []canbus.Filter{{ID: 0x7FF, Mask: 0x7FF}},
		ErrorMask: canbus.ErrClassController | canbus.ErrClassBusOff}); err != nil {
		t.Fatal(err)
	}
	// e1a is protocol class only: filtered. The state change and bus-off
	// pass; the data frame 123 does not match the filter, 7FF does.
	p.write(t, "e1a\rsp130130\rt1230\rsb255255\rt7FF0\re9\r")
	want := []string{"error", "error", "7FF"}
	for i, w := range want {
		if err := c.Receive(ctx, &f); err != nil {
			t.Fatal(err)
		}
		if w == "error" && !f.IsError() || w != "error" && (f.IsError() || f.ID != 0x7FF) {
			t.Fatalf("frame %d: %v, want %s", i, f, w)
		}
		if f.Interface == "" || f.Timestamp.IsZero() {
			t.Fatalf("frame %d: no interface or timestamp", i)
		}
	}
	if st, _, _ := c.State(); st != canbus.StateBusOff {
		t.Fatalf("State = %v, want bus-off", st)
	}
	s := c.Stats()
	if s.RxErrors != 2 || s.RxFrames != 2 {
		t.Fatalf("stats %+v", s)
	}
	deadline := ctxT(t)
	for c.Stats().RxFailure != 1 {
		select {
		case <-deadline.Done():
			t.Fatalf("RxFailure = %d, want 1 (e9)", c.Stats().RxFailure)
		default:
		}
	}
}

// TestBusStateOverSlcan: a Bus derives its state from the error frames.
func TestBusStateOverSlcan(t *testing.T) {
	c, p := newPeer(t, Config{NoCommands: true})
	_ = c.SetFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll})
	bus, err := canbus.NewBus(c, canbus.WithStartOnSubscribe())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	sub, err := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll}))
	if err != nil {
		t.Fatal(err)
	}
	p.write(t, "sp000140\r")
	f, err := sub.Receive(ctxT(t))
	if err != nil || !f.IsError() {
		t.Fatalf("%v %v", f, err)
	}
	if st := bus.State(); st != canbus.StatePassive {
		t.Fatalf("bus state %v, want passive", st)
	}
}

func FuzzParseStatus(f *testing.F) {
	for _, s := range []string{"e1a", "e3bcO", "e0", "sw100050", "sb256256", "sa000000"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		var fr canbus.Frame
		if ok, err := ParseErrorRecord(line, &fr); ok {
			if err != nil || !fr.IsError() || fr.Validate() != nil {
				t.Fatalf("%q: %v %v", line, fr, err)
			}
		}
		st, tx, rx, ok, err := ParseStateRecord(line)
		if !ok {
			return
		}
		if err != nil || tx > 999 || rx > 999 || st < canbus.StateActive || st > canbus.StateBusOff {
			t.Fatalf("%q: %v %d %d %v", line, st, tx, rx, err)
		}
		for prev := canbus.StateActive; prev <= canbus.StateBusOff; prev++ {
			g, changed := StateFrame(prev, st, tx, rx)
			if changed != (prev != st) || changed && (!g.IsError() || g.Validate() != nil) {
				t.Fatalf("%q from %v: %v %v", line, prev, g, changed)
			}
		}
	})
}
