// SPDX-License-Identifier: MIT

package candb

import (
	"strconv"
	"strings"
	"testing"
)

// TestOverlapDiagnosticsBounded: a message of n signals all sharing bit 0
// has n(n-1)/2 overlapping pairs. Validate lists at most
// maxOverlapDiagnostics of them and counts the rest in one warning, so the
// diagnostics and their memory stay linear in the input. Before, every
// pair was a diagnostic: 2000 one-bit signals gave 1,999,000 of them.
func TestOverlapDiagnosticsBounded(t *testing.T) {
	const n = 2000
	m := &Message{Name: "M", ID: 1, Length: 8}
	for i := range n {
		m.Signals = append(m.Signals, &Signal{Name: "S" + strconv.Itoa(i), Length: 1, Factor: 1})
	}
	db := &Database{Messages: []*Message{m}}
	var ds []Diagnostic
	allocs := testing.AllocsPerRun(1, func() { ds = db.Validate() })
	if len(ds) != maxOverlapDiagnostics+1 {
		t.Fatalf("%d diagnostics, want %d", len(ds), maxOverlapDiagnostics+1)
	}
	last := ds[len(ds)-1]
	want := strconv.Itoa(n*(n-1)/2-maxOverlapDiagnostics) + " more overlapping signal pairs not listed"
	if last.Object != "message M" || last.Message != want || last.Severity != Warning {
		t.Fatalf("summary %+v, want %q", last, want)
	}
	if allocs > 50_000 {
		t.Fatalf("Validate made %.0f allocations for %d signals", allocs, n)
	}
	// Few overlaps: all listed, no summary.
	small := &Message{Name: "S", ID: 2, Length: 8, Signals: m.Signals[:3]}
	ds = (&Database{Messages: []*Message{small}}).Validate()
	if len(ds) != 3 || strings.Contains(ds[2].Message, "not listed") {
		t.Fatalf("%v", ds)
	}
}

// TestOverlapMuxChain: overlap checks of a deep extended-multiplexing chain
// compute each signal's conditions once per message instead of once per
// pair. Before, a 300-deep chain allocated about 1.8 GiB in Validate.
func TestOverlapMuxChain(t *testing.T) {
	const n = 300
	m := &Message{Name: "M", ID: 1, Length: 8}
	for i := range n {
		s := &Signal{Name: "S" + strconv.Itoa(i), Length: 1, Factor: 1, IsMultiplexer: true}
		if i > 0 {
			s.Multiplexer, s.MuxValues = "S"+strconv.Itoa(i-1), []MuxRange{{0, 0}}
		}
		m.Signals = append(m.Signals, s)
	}
	db := &Database{Messages: []*Message{m}}
	var ds []Diagnostic
	allocs := testing.AllocsPerRun(1, func() { ds = db.Validate() })
	if len(ds) != maxOverlapDiagnostics+1 || !strings.HasPrefix(ds[len(ds)-1].Message, strconv.Itoa(n*(n-1)/2-maxOverlapDiagnostics)+" more") {
		t.Fatalf("%d diagnostics, last %v", len(ds), ds[len(ds)-1])
	}
	if allocs > 50_000 {
		t.Fatalf("Validate made %.0f allocations for a %d-deep chain", allocs, n)
	}
}

// TestOverlapNestedExclusive: signals under different values of an outer
// switch never coexist, however deep they sit.
func TestOverlapNestedExclusive(t *testing.T) {
	m := &Message{Name: "M", ID: 1, Length: 8, Signals: []*Signal{
		{Name: "Root", Length: 4, Factor: 1, IsMultiplexer: true},
		{Name: "A", StartBit: 4, Length: 4, Factor: 1, IsMultiplexer: true, Multiplexer: "Root", MuxValues: []MuxRange{{1, 1}}},
		{Name: "B", StartBit: 4, Length: 4, Factor: 1, IsMultiplexer: true, Multiplexer: "Root", MuxValues: []MuxRange{{2, 2}}},
		{Name: "AX", StartBit: 8, Length: 8, Factor: 1, Multiplexer: "A", MuxValues: []MuxRange{{0, 3}}},
		{Name: "BX", StartBit: 8, Length: 8, Factor: 1, Multiplexer: "B", MuxValues: []MuxRange{{0, 3}}},
		{Name: "AY", StartBit: 12, Length: 8, Factor: 1, Multiplexer: "A", MuxValues: []MuxRange{{2, 5}}},
	}}
	ds := (&Database{Messages: []*Message{m}}).Validate()
	var got []string
	for _, d := range ds {
		got = append(got, d.Object+": "+d.Message)
	}
	want := []string{"signal M.AY: overlaps signal AX"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
