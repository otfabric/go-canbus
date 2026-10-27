// SPDX-License-Identifier: MIT

// Package kcd reads KCD files, the XML CAN database format of the Kayak
// project (namespace http://kayak.2codeornot2code.org/1.0), into the
// format-independent model of package candb.
//
// Supported: Document (version, notes), Node, Bus (all buses are merged
// into one database), Message (id, name, length or "auto", interval,
// format, remote with a warning, Notes, Producer), Signal (offset,
// length, endianess, Notes, Consumer, Value with type
// unsigned/signed/single/double, slope, intercept, unit, min, max,
// LabelSet with Label and LabelGroup) and Multiplex with MuxGroup.
//
// The offset of a big-endian signal is taken as the index of its most
// significant bit counted from the most significant bit of byte 0, the
// interpretation of canmatrix, which serves as the reference (see
// INTEROPERABILITY.md). Unknown elements and attributes produce warnings.
package kcd

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/candb/bits"
)

type xNetwork struct {
	XMLName  xml.Name   `xml:"NetworkDefinition"`
	Document *xDocument `xml:"Document"`
	Nodes    []xNode    `xml:"Node"`
	Buses    []xBus     `xml:"Bus"`
	Any      []xAny     `xml:",any"`
	Attrs    []xml.Attr `xml:",any,attr"`
}

type xAny struct {
	XMLName xml.Name
}

type xDocument struct {
	Version string `xml:"version,attr"`
	Text    string `xml:",chardata"`
}

type xNode struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

type xBus struct {
	Name     string     `xml:"name,attr"`
	Messages []xMessage `xml:"Message"`
	Any      []xAny     `xml:",any"`
}

type xNodeRef struct {
	ID string `xml:"id,attr"`
}

type xMessage struct {
	ID        string       `xml:"id,attr"`
	Name      string       `xml:"name,attr"`
	Length    string       `xml:"length,attr"`
	Interval  string       `xml:"interval,attr"`
	Format    string       `xml:"format,attr"`
	Remote    string       `xml:"remote,attr"`
	Notes     string       `xml:"Notes"`
	Producer  []xNodeRef   `xml:"Producer>NodeRef"`
	Signals   []xSignal    `xml:"Signal"`
	Multiplex []xMultiplex `xml:"Multiplex"`
	Any       []xAny       `xml:",any"`
}

type xSignal struct {
	Name      string     `xml:"name,attr"`
	Offset    string     `xml:"offset,attr"`
	Length    string     `xml:"length,attr"`
	Endianess string     `xml:"endianess,attr"`
	Notes     string     `xml:"Notes"`
	Consumer  []xNodeRef `xml:"Consumer>NodeRef"`
	Value     *xValue    `xml:"Value"`
	Labels    *xLabelSet `xml:"LabelSet"`
	Any       []xAny     `xml:",any"`
}

type xMultiplex struct {
	xSignal
	Groups []xMuxGroup `xml:"MuxGroup"`
}

type xMuxGroup struct {
	Count   string    `xml:"count,attr"`
	Signals []xSignal `xml:"Signal"`
	Any     []xAny    `xml:",any"`
}

type xValue struct {
	Type      string `xml:"type,attr"`
	Slope     string `xml:"slope,attr"`
	Intercept string `xml:"intercept,attr"`
	Unit      string `xml:"unit,attr"`
	Min       string `xml:"min,attr"`
	Max       string `xml:"max,attr"`
}

type xLabelSet struct {
	Labels []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"Label"`
	Groups []struct {
		Name string `xml:"name,attr"`
		From string `xml:"from,attr"`
		To   string `xml:"to,attr"`
	} `xml:"LabelGroup"`
}

// ParseError reports a KCD file that could not be parsed without errors.
type ParseError struct {
	Diagnostics []candb.Diagnostic
}

// Error reports the first error diagnostic.
func (e *ParseError) Error() string {
	for _, d := range e.Diagnostics {
		if d.Severity == candb.Error {
			return "kcd: " + d.String()
		}
	}
	return "kcd: invalid file"
}

// ParseFile parses a KCD file. See [Parse].
func ParseFile(path string) (*candb.Database, []candb.Diagnostic, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse parses KCD XML. Like dbc.Parse it returns the database, every
// diagnostic and, when any is an error, a *[ParseError].
func Parse(r io.Reader) (*candb.Database, []candb.Diagnostic, error) {
	var x xNetwork
	dec := xml.NewDecoder(r)
	if err := dec.Decode(&x); err != nil {
		d := []candb.Diagnostic{{Severity: candb.Error, Message: "XML: " + err.Error()}}
		return &candb.Database{}, d, &ParseError{Diagnostics: d}
	}
	p := &parser{db: &candb.Database{}, nodes: map[string]string{}}
	p.network(&x)
	p.diags = append(p.diags, p.db.Validate()...)
	if candb.HasErrors(p.diags) {
		return p.db, p.diags, &ParseError{Diagnostics: p.diags}
	}
	return p.db, p.diags, nil
}

type parser struct {
	db    *candb.Database
	diags []candb.Diagnostic
	nodes map[string]string // id → name
}

func (p *parser) diag(sev candb.Severity, obj, format string, args ...any) {
	p.diags = append(p.diags, candb.Diagnostic{Severity: sev, Object: obj, Message: fmt.Sprintf(format, args...)})
}

func (p *parser) unknown(obj string, any []xAny) {
	for _, a := range any {
		p.diag(candb.Warning, obj, "element <%s> is not supported: ignored", a.XMLName.Local)
	}
}

func (p *parser) network(x *xNetwork) {
	if x.Document != nil {
		p.db.Version = x.Document.Version
		p.db.Comment = strings.TrimSpace(x.Document.Text)
	}
	p.unknown("network", x.Any)
	for _, n := range x.Nodes {
		p.nodes[n.ID] = n.Name
		p.db.Nodes = append(p.db.Nodes, &candb.Node{Name: n.Name})
	}
	if len(x.Buses) > 1 {
		p.diag(candb.Warning, "network", "%d buses merged into one database", len(x.Buses))
	}
	for _, b := range x.Buses {
		p.unknown("bus "+b.Name, b.Any)
		for i := range b.Messages {
			p.message(&b.Messages[i])
		}
	}
}

func (p *parser) refs(obj string, refs []xNodeRef) []string {
	var out []string
	for _, r := range refs {
		name, ok := p.nodes[r.ID]
		if !ok {
			p.diag(candb.Warning, obj, "reference to unknown node %q ignored", r.ID)
			continue
		}
		out = append(out, name)
	}
	return out
}

func (p *parser) message(x *xMessage) {
	obj := "message " + x.Name
	p.unknown(obj, x.Any)
	id, err := strconv.ParseUint(x.ID, 0, 32)
	if err != nil {
		p.diag(candb.Error, obj, "bad id %q", x.ID)
		return
	}
	m := &candb.Message{ID: canbus.ID(id), Name: x.Name, Comment: strings.TrimSpace(x.Notes)}
	switch x.Format {
	case "", "standard":
	case "extended":
		m.Extended = true
	default:
		p.diag(candb.Error, obj, "unknown format %q", x.Format)
	}
	if x.Remote == "true" {
		p.diag(candb.Warning, obj, "remote frame definition: the model describes data frames only")
	}
	if x.Interval != "" {
		if v, err := strconv.Atoi(x.Interval); err == nil {
			m.CycleTime = v
		} else {
			p.diag(candb.Warning, obj, "bad interval %q ignored", x.Interval)
		}
	}
	m.Senders = p.refs(obj, x.Producer)
	for i := range x.Signals {
		if s := p.signal(m, &x.Signals[i]); s != nil {
			m.Signals = append(m.Signals, s)
		}
	}
	for i := range x.Multiplex {
		mx := &x.Multiplex[i]
		sw := p.signal(m, &mx.xSignal)
		if sw == nil {
			continue
		}
		sw.IsMultiplexer = true
		m.Signals = append(m.Signals, sw)
		for _, g := range mx.Groups {
			p.unknown("multiplexer "+sw.Name, g.Any)
			count, err := strconv.ParseUint(g.Count, 0, 64)
			if err != nil {
				p.diag(candb.Error, "multiplexer "+sw.Name, "bad MuxGroup count %q", g.Count)
				continue
			}
			for j := range g.Signals {
				if s := p.signal(m, &g.Signals[j]); s != nil {
					s.Multiplexer = sw.Name
					s.MuxValues = []candb.MuxRange{{Low: count, High: count}}
					m.Signals = append(m.Signals, s)
				}
			}
		}
	}
	switch x.Length {
	case "", "auto":
		for _, s := range m.Signals {
			m.Length = max(m.Length, s.Span())
		}
	default:
		n, err := strconv.Atoi(x.Length)
		if err != nil {
			p.diag(candb.Error, obj, "bad length %q", x.Length)
			return
		}
		m.Length = n
	}
	if m.Length > 8 {
		m.FD = true
	}
	p.db.Messages = append(p.db.Messages, m)
}

func atof(s string, def float64) (float64, error) {
	if s == "" {
		return def, nil
	}
	return strconv.ParseFloat(s, 64)
}

func (p *parser) signal(m *candb.Message, x *xSignal) *candb.Signal {
	obj := "signal " + m.Name + "." + x.Name
	p.unknown(obj, x.Any)
	s := &candb.Signal{Name: x.Name, Factor: 1, Comment: strings.TrimSpace(x.Notes)}
	off, err := strconv.Atoi(x.Offset)
	if err != nil || off < 0 {
		p.diag(candb.Error, obj, "bad offset %q", x.Offset)
		return nil
	}
	s.Length = 1
	if x.Length != "" {
		if s.Length, err = strconv.Atoi(x.Length); err != nil {
			p.diag(candb.Error, obj, "bad length %q", x.Length)
			return nil
		}
	}
	switch x.Endianess {
	case "", "little":
		s.StartBit = off
	case "big":
		s.ByteOrder = candb.BigEndian
		s.StartBit = bits.MotorolaDBCStart(off)
	default:
		p.diag(candb.Error, obj, "unknown endianess %q", x.Endianess)
		return nil
	}
	s.Receivers = p.refs(obj, x.Consumer)
	if v := x.Value; v != nil {
		switch v.Type {
		case "", "unsigned":
		case "signed":
			s.Signed = true
		case "single":
			s.ValueType = candb.Float32
		case "double":
			s.ValueType = candb.Float64
		default:
			p.diag(candb.Error, obj, "unknown value type %q", v.Type)
		}
		var e1, e2, e3, e4 error
		s.Factor, e1 = atof(v.Slope, 1)
		s.Offset, e2 = atof(v.Intercept, 0)
		s.Minimum, e3 = atof(v.Min, 0)
		s.Maximum, e4 = atof(v.Max, 0)
		for _, e := range []error{e1, e2, e3, e4} {
			if e != nil {
				p.diag(candb.Error, obj, "bad number in <Value>: %v", e)
				break
			}
		}
		s.Unit = v.Unit
	}
	if ls := x.Labels; ls != nil {
		for _, l := range ls.Labels {
			v, err := strconv.ParseInt(l.Value, 0, 64)
			if err != nil {
				p.diag(candb.Error, obj, "bad label value %q", l.Value)
				continue
			}
			s.Values = append(s.Values, candb.ValueDescription{Value: v, Description: l.Name})
		}
		for _, g := range ls.Groups {
			from, e1 := strconv.ParseInt(g.From, 0, 64)
			to, e2 := strconv.ParseInt(g.To, 0, 64)
			switch {
			case e1 != nil || e2 != nil || to < from:
				p.diag(candb.Error, obj, "bad LabelGroup %q..%q", g.From, g.To)
			case to-from >= 256:
				p.diag(candb.Warning, obj, "LabelGroup %s (%d values) not expanded: ignored", g.Name, to-from+1)
			default:
				for v := from; v <= to; v++ {
					s.Values = append(s.Values, candb.ValueDescription{Value: v, Description: g.Name})
				}
			}
		}
		sort.SliceStable(s.Values, func(i, j int) bool { return s.Values[i].Value < s.Values[j].Value })
	}
	return s
}
