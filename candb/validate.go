// SPDX-License-Identifier: MIT

package candb

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/otfabric/go-canbus"
)

// Severity grades a diagnostic.
type Severity uint8

// Severities.
const (
	Info    Severity = iota // noted, no effect on the model
	Warning                 // ignored or approximated input: the model lacks it
	Error                   // the model is wrong or unusable where it applies
)

// String returns "info", "warning" or "error".
func (s Severity) String() string {
	return [...]string{"info", "warning", "error"}[min(int(s), 2)]
}

// MarshalText implements encoding.TextMarshaler.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Diagnostic reports a problem found while parsing or validating a
// database.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Line     int      `json:"line,omitempty"`   // input line, 0 when not from parsing
	Object   string   `json:"object,omitempty"` // "message Engine", "signal Engine.Speed"
	Message  string   `json:"message"`
}

// String formats the diagnostic as "line 12: warning: message ...".
func (d Diagnostic) String() string {
	var b strings.Builder
	if d.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", d.Line)
	}
	b.WriteString(d.Severity.String())
	b.WriteString(": ")
	if d.Object != "" {
		b.WriteString(d.Object)
		b.WriteString(": ")
	}
	b.WriteString(d.Message)
	return b.String()
}

// HasErrors reports whether any diagnostic has severity Error.
func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Validate checks the database for problems the codec or the generator
// would trip over: identifiers out of range, invalid lengths, duplicate
// names, signals outside their message or of an impossible width, broken
// multiplexer references, and signals that overlap while both present.
//
// The overlap check is bounded: at most 64 overlapping pairs are listed
// per message, followed by a count of the others, and the check of one
// database takes at most 2^24 steps (one per pair of signals whose bit
// ranges meet, plus one per multiplexer condition built or compared).
// Where that ends, one warning says so and the signals and messages after
// it are not checked for overlaps.
func (db *Database) Validate() []Diagnostic {
	work := maxOverlapWork
	return db.validate(&work)
}

// validate is Validate with the steps left for the overlap check in work.
func (db *Database) validate(work *int) []Diagnostic {
	var ds []Diagnostic
	add := func(sev Severity, obj, format string, args ...any) {
		ds = append(ds, Diagnostic{Severity: sev, Object: obj, Message: fmt.Sprintf(format, args...)})
	}
	type key struct {
		id  canbus.ID
		ext bool
	}
	ids := map[key]string{}
	names := map[string]bool{}
	for _, m := range db.Messages {
		obj := "message " + m.Name
		if names[m.Name] {
			add(Error, obj, "duplicate message name")
		}
		names[m.Name] = true
		k := key{m.ID, m.Extended}
		if other, dup := ids[k]; dup {
			add(Error, obj, "identifier 0x%X also used by %s", uint32(m.ID), other)
		}
		ids[k] = m.Name
		if m.Extended && m.ID > canbus.MaxExtendedID || !m.Extended && m.ID > canbus.MaxStandardID {
			add(Error, obj, "identifier 0x%X out of range", uint32(m.ID))
		}
		// A length no frame of the format can carry is an error; a CAN FD
		// length between two frame lengths is a warning.
		switch {
		case m.Length < 0, m.FD && m.Length > canbus.MaxFDLen, !m.FD && m.Length > canbus.MaxClassicalLen:
			add(Error, obj, "length %d is invalid for a %s frame", m.Length, map[bool]string{true: "CAN FD", false: "classical"}[m.FD])
		case m.FD && !canbus.ValidFDLen(m.Length):
			add(Warning, obj, "length %d is not a CAN FD length; frames carry %d bytes", m.Length, canbus.RoundUpFDLen(m.Length))
		}
		if m.BRS && !m.FD {
			add(Warning, obj, "bit rate switching on a classical frame")
		}
		ds = append(ds, m.validateSignals(work)...)
	}
	return ds
}

func (m *Message) validateSignals(work *int) []Diagnostic {
	var ds []Diagnostic
	add := func(sev Severity, s *Signal, format string, args ...any) {
		ds = append(ds, Diagnostic{Severity: sev, Object: "signal " + m.Name + "." + s.Name, Message: fmt.Sprintf(format, args...)})
	}
	seen := map[string]bool{}
	byName := map[string]*Signal{}
	for _, s := range m.Signals {
		byName[s.Name] = s
	}
	for _, s := range m.Signals {
		if seen[s.Name] {
			add(Error, s, "duplicate signal name")
		}
		seen[s.Name] = true
		if _, err := s.layout(); err != nil {
			add(Error, s, "%v", err)
			continue
		}
		if s.Span() > m.Length {
			add(Error, s, "bits exceed the message length of %d bytes", m.Length)
		}
		if s.ValueType == Float32 && s.Length != 32 || s.ValueType == Float64 && s.Length != 64 {
			add(Error, s, "%s signal of %d bits", s.ValueType, s.Length)
		}
		if s.Factor == 0 {
			add(Warning, s, "factor 0 (encoding treats it as 1)")
		}
		if s.Multiplexer != "" {
			sw := byName[s.Multiplexer]
			switch {
			case sw == nil:
				add(Error, s, "multiplexer %q does not exist", s.Multiplexer)
			case !sw.IsMultiplexer:
				add(Error, s, "multiplexer %q is not a multiplexer switch", s.Multiplexer)
			case sw == s:
				add(Error, s, "multiplexed by itself")
			}
			if len(s.MuxValues) == 0 {
				add(Error, s, "multiplexed signal without multiplexer values")
			}
		}
	}
	if _, err := m.active(func(*Signal) (uint64, bool, error) { return 0, true, nil }); err != nil && !HasErrors(ds) {
		ds = append(ds, Diagnostic{Severity: Error, Object: "message " + m.Name, Message: err.Error()})
	}
	ds = append(ds, m.overlaps(work)...)
	return ds
}

// muxCond is one switch on the path from a signal to the root and the
// values it must have for the signal to be present.
type muxCond struct {
	sw     string
	ranges []MuxRange
}

// conditions returns, for every signal, the switches on its path to the
// root (as a list and as a map) in one pass with a name index, so the
// pairwise overlap check does not rebuild them per pair. Each switch
// followed takes one step of work; ok is false when work ran out.
func (m *Message) conditions(work *int) (list map[*Signal][]muxCond, set map[*Signal]map[string][]MuxRange, ok bool) {
	byName := make(map[string]*Signal, len(m.Signals))
	for _, s := range m.Signals {
		if _, dup := byName[s.Name]; !dup {
			byName[s.Name] = s
		}
	}
	list = make(map[*Signal][]muxCond, len(m.Signals))
	set = make(map[*Signal]map[string][]MuxRange, len(m.Signals))
	for _, s := range m.Signals {
		if s.Multiplexer == "" {
			continue
		}
		var cs []muxCond
		cm := map[string][]MuxRange{}
		for depth, c := 0, s; c != nil && c.Multiplexer != "" && depth < len(m.Signals); depth++ {
			if *work--; *work < 0 {
				return nil, nil, false
			}
			if _, seen := cm[c.Multiplexer]; !seen {
				cs = append(cs, muxCond{c.Multiplexer, nil})
			}
			cm[c.Multiplexer] = c.MuxValues
			c = byName[c.Multiplexer]
		}
		for i := range cs {
			cs[i].ranges = cm[cs[i].sw]
		}
		list[s], set[s] = cs, cm
	}
	return list, set, true
}

// coexist reports whether two signals can be present in the same payload,
// from their precomputed conditions.
func coexist(ca []muxCond, cb map[string][]MuxRange) bool {
	for _, c := range ca {
		if rb, ok := cb[c.sw]; ok && !rangesIntersect(c.ranges, rb) {
			return false
		}
	}
	return true
}

func rangesIntersect(a, b []MuxRange) bool {
	for _, x := range a {
		for _, y := range b {
			if x.Low <= y.High && y.Low <= x.High {
				return true
			}
		}
	}
	return false
}

// maxOverlapDiagnostics bounds the overlap warnings listed per message;
// the rest are counted in one summary warning, so the diagnostics stay
// linear in the size of the database.
const maxOverlapDiagnostics = 64

// maxOverlapWork bounds the steps of the overlap check of one database, so
// that validation takes time linear in the size of the database plus a
// constant: one step per pair of signals whose bit ranges meet, one per
// multiplexer condition built and one per condition compared. Databases of
// vehicles take a small part of it; see [Database.Validate].
const maxOverlapWork = 1 << 24

// overlapsEnded is left in the work counter once the end of the overlap
// check has been reported, so that it is reported once per database.
const overlapsEnded = math.MinInt

// overlaps reports signals that share bits while both present: at most
// maxOverlapDiagnostics pairs, then a count of the others. It takes its
// steps from work and ends, with a warning, where work runs out.
func (m *Message) overlaps(work *int) []Diagnostic {
	if *work == overlapsEnded {
		return nil
	}
	type span struct {
		s     *Signal
		bits  []int                 // occupied linear bit indexes, little-endian numbering
		conds []muxCond             // the switches on the signal's path to the root
		set   map[string][]MuxRange // the same by switch name
	}
	var spans []span
	for _, s := range m.Signals {
		pos, err := s.layout()
		if err != nil || s.Span() > m.Length {
			continue
		}
		var bs []int
		for i := 0; i < s.Length; i++ {
			if s.ByteOrder == BigEndian {
				lin := pos + i
				bs = append(bs, lin/8*8+7-lin%8)
			} else {
				bs = append(bs, pos+i)
			}
		}
		sort.Ints(bs) // layout succeeded, so Length >= 1
		spans = append(spans, span{s: s, bits: bs})
	}
	// Sweep the spans in order of their lowest bit: a span can only share
	// bits with the spans that start before its highest bit ends, so
	// signals that do not overlap cost O(n log n), not O(n^2).
	order := make([]int, len(spans))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool { return spans[order[x]].bits[0] < spans[order[y]].bits[0] })
	var ds []Diagnostic
	suppressed := 0
	list, set, ok := m.conditions(work)
	for i := range spans {
		spans[i].conds, spans[i].set = list[spans[i].s], set[spans[i].s]
	}
sweep:
	for x, i := range order {
		if !ok {
			break
		}
		hi := spans[i].bits[len(spans[i].bits)-1]
		for _, j := range order[x+1:] {
			if spans[j].bits[0] > hi {
				break
			}
			a, b := spans[i], spans[j]
			if j < i {
				a, b = b, a // report the later signal of the database
			}
			// One step for the pair and one per condition compared: the
			// shorter path is walked and looked up in the longer one.
			if *work -= 1 + min(len(a.conds), len(b.conds)); *work < 0 {
				ok = false
				break sweep
			}
			if !intersects(a.bits, b.bits) {
				continue
			}
			if len(a.conds) <= len(b.conds) && !coexist(a.conds, b.set) || len(a.conds) > len(b.conds) && !coexist(b.conds, a.set) {
				continue
			}
			if len(ds) >= maxOverlapDiagnostics {
				suppressed++
				continue
			}
			ds = append(ds, Diagnostic{Severity: Warning, Object: "signal " + m.Name + "." + b.s.Name,
				Message: "overlaps signal " + a.s.Name})
		}
	}
	if suppressed > 0 {
		format := "%d more overlapping signal pairs not listed"
		if !ok {
			format = "at least %d more overlapping signal pairs not listed"
		}
		ds = append(ds, Diagnostic{Severity: Warning, Object: "message " + m.Name, Message: fmt.Sprintf(format, suppressed)})
	}
	if !ok {
		*work = overlapsEnded
		ds = append(ds, Diagnostic{Severity: Warning, Object: "message " + m.Name,
			Message: fmt.Sprintf("overlap check ended after %d steps for the database: the signals and messages from here on are not checked for overlaps", maxOverlapWork)})
	}
	return ds
}

func intersects(a, b []int) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			return true
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return false
}
