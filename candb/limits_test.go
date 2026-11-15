// SPDX-License-Identifier: MIT

package candb

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus"
)

// TestMessageLengthDiagnostics: a length no frame of the message's format
// carries is an error diagnostic; a CAN FD length between two frame
// lengths is a warning; the frame lengths themselves give no diagnostic.
func TestMessageLengthDiagnostics(t *testing.T) {
	const none = Severity(255)
	for _, c := range []struct {
		length int
		fd     bool
		want   Severity
	}{
		{0, false, none}, {1, false, none}, {7, false, none}, {8, false, none},
		{9, false, Error}, {64, false, Error}, {-1, false, Error}, {math.MinInt, false, Error},
		{0, true, none}, {8, true, none}, {12, true, none}, {48, true, none}, {64, true, none},
		{9, true, Warning}, {13, true, Warning}, {63, true, Warning},
		{65, true, Error}, {70, true, Error}, {math.MaxInt32, true, Error}, {math.MaxInt, true, Error},
		{-1, true, Error}, {math.MinInt32, true, Error}, {math.MinInt, true, Error},
	} {
		db := &Database{Messages: []*Message{{Name: "M", ID: 1, Length: c.length, FD: c.fd}}}
		ds := db.Validate()
		got := none
		if len(ds) == 1 {
			got = ds[0].Severity
		}
		if len(ds) > 1 || got != c.want {
			t.Errorf("length %d, FD %v: %v, want severity %v", c.length, c.fd, ds, c.want)
		}
		if c.want == Error && !strings.Contains(ds[0].Message, "is invalid for a") {
			t.Errorf("length %d, FD %v: %q", c.length, c.fd, ds[0].Message)
		}
	}
}

// sharedBit returns a message of n one-bit signals on bit 0.
func sharedBit(name string, id canbus.ID, n int) *Message {
	m := &Message{Name: name, ID: id, Length: 8}
	for i := range n {
		m.Signals = append(m.Signals, &Signal{Name: "S" + strconv.Itoa(i), Length: 1, Factor: 1})
	}
	return m
}

// TestOverlapCheckBounded: the overlap check of a database takes at most
// maxOverlapWork steps. Where they end, the message gets the pairs listed
// so far, a count of at least the others seen and one warning; signals and
// messages after it are not checked for overlaps, and the other checks
// still run.
func TestOverlapCheckBounded(t *testing.T) {
	// n(n-1)/2 pairs, one step each: more than the bound.
	const n = 6000
	if n*(n-1)/2 <= maxOverlapWork {
		t.Fatal("the message fits the bound: the test proves nothing")
	}
	after := sharedBit("After", 2, 3)
	after.Signals = append(after.Signals, &Signal{Name: "Wide", Length: 70})
	db := &Database{Messages: []*Message{sharedBit("M", 1, n), after}}
	ds := db.Validate()
	if len(ds) != maxOverlapDiagnostics+3 {
		t.Fatalf("%d diagnostics, want %d: last %v", len(ds), maxOverlapDiagnostics+3, ds[len(ds)-1])
	}
	count, ended, later := ds[maxOverlapDiagnostics], ds[maxOverlapDiagnostics+1], ds[maxOverlapDiagnostics+2]
	wantCount := "at least " + strconv.Itoa(maxOverlapWork-maxOverlapDiagnostics) + " more overlapping signal pairs not listed"
	if count.Object != "message M" || count.Message != wantCount || count.Severity != Warning {
		t.Errorf("count %+v, want %q", count, wantCount)
	}
	if ended.Object != "message M" || ended.Severity != Warning ||
		!strings.HasPrefix(ended.Message, "overlap check ended after "+strconv.Itoa(maxOverlapWork)+" steps") {
		t.Errorf("end %+v", ended)
	}
	// The message after the end: its layout error is reported, its
	// overlapping signals are not.
	if later.Object != "signal After.Wide" || later.Severity != Error {
		t.Errorf("later %+v", later)
	}
}

// TestOverlapWork: the steps of the overlap check, at the boundaries of
// the work left: one per pair of signals whose bit ranges meet, one per
// multiplexer condition built and one per condition compared.
func TestOverlapWork(t *testing.T) {
	chain := &Message{Name: "C", ID: 3, Length: 8}
	for i := range 4 {
		s := &Signal{Name: "S" + strconv.Itoa(i), Length: 1, Factor: 1, IsMultiplexer: true}
		if i > 0 {
			s.Multiplexer, s.MuxValues = "S"+strconv.Itoa(i-1), []MuxRange{{0, 0}}
		}
		chain.Signals = append(chain.Signals, s)
	}
	apart := &Message{Name: "A", ID: 4, Length: 8, Signals: []*Signal{
		{Name: "X", Length: 8, Factor: 1}, {Name: "Y", StartBit: 8, Length: 8, Factor: 1}, {Name: "Z", StartBit: 16, Length: 8, Factor: 1},
	}}
	for _, c := range []struct {
		name  string
		m     *Message
		steps int // the steps the whole check takes
		pairs int // the overlapping pairs it then lists
	}{
		{"no signals", &Message{Name: "E", ID: 5, Length: 8}, 0, 0},
		{"one signal", sharedBit("M", 1, 1), 0, 0},
		{"signals apart", apart, 0, 0},
		{"two on a bit", sharedBit("M", 1, 2), 1, 1},
		{"three on a bit", sharedBit("M", 1, 3), 3, 3},
		// Paths of 0, 1, 2 and 3 conditions (6 built); 6 pairs, each
		// compared along the shorter path: 0+0+0+1+1+2 conditions.
		{"chain of four", chain, 6 + 6 + 4, 6},
	} {
		for _, left := range []int{c.steps, c.steps + 1, c.steps - 1, 0} {
			if left < 0 {
				continue
			}
			work := left
			ds := (&Database{Messages: []*Message{c.m}}).validate(&work)
			ended := len(ds) > 0 && strings.HasPrefix(ds[len(ds)-1].Message, "overlap check ended")
			switch {
			case left >= c.steps:
				if ended || len(ds) != c.pairs || work != left-c.steps {
					t.Errorf("%s, %d steps left: %d diagnostics, %d steps left after, ended %v", c.name, left, len(ds), work, ended)
				}
			default:
				if !ended || work != overlapsEnded {
					t.Errorf("%s, %d steps left: %v, work %d", c.name, left, ds, work)
				}
			}
		}
	}
	// Once the check has ended it stays ended, without another warning.
	work := 0
	db := &Database{Messages: []*Message{sharedBit("M", 1, 2), sharedBit("N", 2, 2), sharedBit("O", 3, 2)}}
	if ds := db.validate(&work); len(ds) != 1 || ds[0].Object != "message M" {
		t.Errorf("%v", ds)
	}
}

// TestSignalLayoutBounds: a signal has 1 to 64 bits and starts at bit 0 to
// 2^24. Outside either bound the codec returns ErrLayout and Validate
// reports an error; at the bounds the layout is valid (and then needs a
// payload that long).
func TestSignalLayoutBounds(t *testing.T) {
	for _, c := range []struct {
		start, length int
		ok            bool
	}{
		{0, 1, true}, {0, 64, true}, {maxStartBit, 1, true}, {maxStartBit - 1, 64, true},
		{0, 0, false}, {0, 65, false}, {0, -1, false}, {0, math.MinInt, false}, {0, math.MaxInt, false},
		{-1, 8, false}, {maxStartBit + 1, 1, false}, {math.MaxInt32, 8, false}, {math.MaxInt, 8, false}, {math.MinInt, 8, false},
	} {
		for _, order := range []ByteOrder{LittleEndian, BigEndian} {
			s := &Signal{Name: "S", StartBit: c.start, Length: c.length, ByteOrder: order, Factor: 1}
			_, err := s.Extract(make([]byte, 8))
			if layout := errors.Is(err, ErrLayout); layout == c.ok {
				t.Errorf("start %d, %d bits, order %v: Extract %v", c.start, c.length, order, err)
			}
			if err := s.Insert(make([]byte, 8), 0); errors.Is(err, ErrLayout) == c.ok {
				t.Errorf("start %d, %d bits, order %v: Insert %v", c.start, c.length, order, err)
			}
			m := &Message{Name: "M", ID: 1, Length: 8, Signals: []*Signal{s}}
			ds := (&Database{Messages: []*Message{m}}).Validate()
			invalid := len(ds) == 1 && ds[0].Severity == Error && strings.Contains(ds[0].Message, "invalid signal layout")
			if invalid == c.ok {
				t.Errorf("start %d, %d bits, order %v: diagnostics %v", c.start, c.length, order, ds)
			}
		}
	}
}
