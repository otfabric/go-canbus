// SPDX-License-Identifier: MIT

package candb

import (
	"fmt"
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
func (db *Database) Validate() []Diagnostic {
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
		switch {
		case m.FD && !canbus.ValidFDLen(m.Length):
			add(Warning, obj, "length %d is not a CAN FD length; frames carry %d bytes", m.Length, canbus.RoundUpFDLen(m.Length))
		case m.FD && m.Length > canbus.MaxFDLen, !m.FD && m.Length > canbus.MaxClassicalLen, m.Length < 0:
			add(Error, obj, "length %d is invalid for a %s frame", m.Length, map[bool]string{true: "CAN FD", false: "classical"}[m.FD])
		}
		if m.BRS && !m.FD {
			add(Warning, obj, "bit rate switching on a classical frame")
		}
		ds = append(ds, m.validateSignals()...)
	}
	return ds
}

func (m *Message) validateSignals() []Diagnostic {
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
	ds = append(ds, m.overlaps()...)
	return ds
}

// coexist reports whether two signals can be present in the same payload.
func (m *Message) coexist(a, b *Signal) bool {
	ca, cb := m.conditions(a), m.conditions(b)
	for sw, ra := range ca {
		if rb, ok := cb[sw]; ok && !rangesIntersect(ra, rb) {
			return false
		}
	}
	return true
}

// conditions maps every switch on the path from s to the root to the
// values it must have for s to be present.
func (m *Message) conditions(s *Signal) map[string][]MuxRange {
	out := map[string][]MuxRange{}
	for depth := 0; s != nil && s.Multiplexer != "" && depth < len(m.Signals); depth++ {
		out[s.Multiplexer] = s.MuxValues
		s = m.Signal(s.Multiplexer)
	}
	return out
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

// overlaps reports signals that share bits while both present.
func (m *Message) overlaps() []Diagnostic {
	type span struct {
		s    *Signal
		bits []int // occupied linear bit indexes, little-endian numbering
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
		sort.Ints(bs)
		spans = append(spans, span{s, bs})
	}
	var ds []Diagnostic
	for i := range spans {
		for j := i + 1; j < len(spans); j++ {
			a, b := spans[i], spans[j]
			if !intersects(a.bits, b.bits) || !m.coexist(a.s, b.s) {
				continue
			}
			ds = append(ds, Diagnostic{Severity: Warning, Object: "signal " + m.Name + "." + b.s.Name,
				Message: "overlaps signal " + a.s.Name})
		}
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
