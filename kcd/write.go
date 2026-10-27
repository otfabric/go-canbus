// SPDX-License-Identifier: MIT

package kcd

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"

	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/candb/bits"
)

// Write writes db as a KCD document with one bus. KCD cannot carry
// extended multiplexing (a multiplexed multiplexer, or a signal present
// for a range of values): such a database is an error. What KCD has no
// place for (attributes, value tables, initial values, node comments,
// signal groups) is dropped and reported as warning diagnostics.
func Write(w io.Writer, db *candb.Database) ([]candb.Diagnostic, error) {
	var diags []candb.Diagnostic
	warn := func(obj, format string, args ...any) {
		diags = append(diags, candb.Diagnostic{Severity: candb.Warning, Object: obj, Message: fmt.Sprintf(format, args...)})
	}
	doc := xNetwork{Document: &xDocument{Version: db.Version, Text: db.Comment}}
	ids := map[string]string{}
	for i, n := range db.Nodes {
		id := strconv.Itoa(i + 1)
		ids[n.Name] = id
		doc.Nodes = append(doc.Nodes, xNode{ID: id, Name: n.Name})
		if n.Comment != "" || len(n.Attributes) > 0 {
			warn("node "+n.Name, "comment and attributes are not represented in KCD")
		}
	}
	ref := func(names []string) []xNodeRef {
		var out []xNodeRef
		for _, n := range names {
			if id, ok := ids[n]; ok {
				out = append(out, xNodeRef{ID: id})
			}
		}
		return out
	}
	if len(db.ValueTables) > 0 || len(db.Attributes) > 0 || len(db.AttributeDefinitions) > 0 {
		warn("network", "value tables and attributes are not represented in KCD")
	}
	bus := xBus{Name: "bus"}
	for _, m := range db.Messages {
		xm := xMessage{ID: fmt.Sprintf("0x%03X", uint32(m.ID)), Name: m.Name, Length: strconv.Itoa(m.Length), Notes: m.Comment, Producer: ref(m.Senders)}
		if m.Extended {
			xm.Format = "extended"
		}
		if m.CycleTime > 0 {
			xm.Interval = strconv.Itoa(m.CycleTime)
		}
		if len(m.SignalGroups) > 0 || len(m.Attributes) > 0 {
			warn("message "+m.Name, "signal groups and attributes are not represented in KCD")
		}
		muxes := map[string]*xMultiplex{}
		var order []string
		for _, s := range m.Signals {
			if s.IsMultiplexer {
				if s.Multiplexer != "" {
					return diags, fmt.Errorf("kcd: %s.%s: extended multiplexing cannot be written as KCD", m.Name, s.Name)
				}
				muxes[s.Name] = &xMultiplex{xSignal: signal(s, ref)}
				order = append(order, s.Name)
			}
			if s.Initial != nil {
				warn("signal "+m.Name+"."+s.Name, "initial value is not represented in KCD")
			}
		}
		for _, s := range m.Signals {
			switch {
			case s.IsMultiplexer:
			case s.Multiplexer == "":
				xm.Signals = append(xm.Signals, signal(s, ref))
			default:
				mx := muxes[s.Multiplexer]
				if mx == nil || len(s.MuxValues) != 1 || s.MuxValues[0].Low != s.MuxValues[0].High {
					return diags, fmt.Errorf("kcd: %s.%s: only single multiplexer values can be written as KCD", m.Name, s.Name)
				}
				count := strconv.FormatUint(s.MuxValues[0].Low, 10)
				gi := -1
				for i, g := range mx.Groups {
					if g.Count == count {
						gi = i
					}
				}
				if gi < 0 {
					mx.Groups = append(mx.Groups, xMuxGroup{Count: count})
					gi = len(mx.Groups) - 1
				}
				mx.Groups[gi].Signals = append(mx.Groups[gi].Signals, signal(s, ref))
			}
		}
		for _, name := range order {
			xm.Multiplex = append(xm.Multiplex, *muxes[name])
		}
		bus.Messages = append(bus.Messages, xm)
	}
	doc.Buses = []xBus{bus}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return diags, err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	out := outNetwork{xNetwork: doc}
	out.XMLName = xml.Name{Space: "http://kayak.2codeornot2code.org/1.0", Local: "NetworkDefinition"}
	if err := enc.Encode(out); err != nil {
		return diags, err
	}
	_, err := io.WriteString(w, "\n")
	return diags, err
}

// outNetwork carries the namespace in its XMLName when writing.
type outNetwork struct {
	XMLName xml.Name
	xNetwork
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func signal(s *candb.Signal, ref func([]string) []xNodeRef) xSignal {
	xs := xSignal{Name: s.Name, Length: strconv.Itoa(s.Length), Notes: s.Comment, Consumer: ref(s.Receivers)}
	if s.ByteOrder == candb.BigEndian {
		xs.Endianess = "big"
		xs.Offset = strconv.Itoa(bits.MotorolaStart(s.StartBit))
	} else {
		xs.Offset = strconv.Itoa(s.StartBit)
	}
	v := &xValue{Unit: s.Unit}
	switch {
	case s.ValueType == candb.Float32:
		v.Type = "single"
	case s.ValueType == candb.Float64:
		v.Type = "double"
	case s.Signed:
		v.Type = "signed"
	}
	if s.Factor != 1 {
		v.Slope = num(s.Factor)
	}
	if s.Offset != 0 {
		v.Intercept = num(s.Offset)
	}
	if s.Minimum != 0 || s.Maximum != 0 {
		v.Min, v.Max = num(s.Minimum), num(s.Maximum)
	}
	if *v != (xValue{}) {
		xs.Value = v
	}
	if len(s.Values) > 0 {
		ls := &xLabelSet{}
		for _, d := range s.Values {
			ls.Labels = append(ls.Labels, struct {
				Name  string `xml:"name,attr,omitempty"`
				Value string `xml:"value,attr,omitempty"`
			}{d.Description, strconv.FormatInt(d.Value, 10)})
		}
		xs.Labels = ls
	}
	return xs
}
