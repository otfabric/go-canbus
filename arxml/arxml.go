// SPDX-License-Identifier: MIT

// Package arxml reads the CAN part of AUTOSAR 4.x system descriptions
// (ARXML) into the format-independent model of package candb.
//
// Supported: CAN-CLUSTER physical channels with CAN-FRAME-TRIGGERINGs
// (IDENTIFIER, CAN-ADDRESSING-MODE, CAN FD frame behaviour, frame ports
// for senders and receivers), CAN-FRAMEs with PDU-TO-FRAME-MAPPINGs at
// byte offsets, I-SIGNAL-I-PDUs with I-SIGNAL-TO-I-PDU-MAPPINGs
// (PACKING-BYTE-ORDER, START-POSITION), I-SIGNALs (LENGTH, INIT-VALUE,
// base type encoding for signed and IEEE 754 signals), SYSTEM-SIGNALs and
// COMPU-METHODs of the categories IDENTICAL, LINEAR, TEXTTABLE and
// SCALE_LINEAR_AND_TEXTTABLE, UNITs, and physical limits from DATA-CONSTRs.
//
// START-POSITION is taken as the DBC start bit in both byte orders, as
// cantools and canmatrix read it (checked in the interop suite). Everything
// else (AUTOSAR 3.x, MULTIPLEXED-I-PDUs, container and secured PDUs, signal
// groups, end-to-end protection, other bus types) is reported as a
// diagnostic and skipped: no ARXML compatibility beyond these elements is
// claimed.
package arxml

import (
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
)

// node is a generic XML element.
type node struct {
	name     string
	attrs    map[string]string
	text     string
	children []*node
	parent   *node
	line     int
}

func (n *node) child(name string) *node {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// path follows a chain of child names.
func (n *node) path(names ...string) *node {
	cur := n
	for _, name := range names {
		if cur == nil {
			return nil
		}
		cur = cur.child(name)
	}
	return cur
}

// find returns every descendant with the name.
func (n *node) find(name string) []*node {
	var out []*node
	var walk func(*node)
	walk = func(m *node) {
		for _, c := range m.children {
			if c.name == name {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

func (n *node) textOf(names ...string) string {
	if c := n.path(names...); c != nil {
		return strings.TrimSpace(c.text)
	}
	return ""
}

func parseTree(r io.Reader) (*node, string, error) {
	dec := xml.NewDecoder(r)
	root := &node{name: "#root"}
	cur := root
	ns := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			line, _ := dec.InputPos()
			n := &node{name: t.Name.Local, parent: cur, line: line}
			if t.Name.Local == "AUTOSAR" {
				ns = t.Name.Space
			}
			for _, a := range t.Attr {
				if n.attrs == nil {
					n.attrs = map[string]string{}
				}
				n.attrs[a.Name.Local] = a.Value
			}
			cur.children = append(cur.children, n)
			cur = n
		case xml.EndElement:
			cur = cur.parent
		case xml.CharData:
			cur.text += string(t)
		}
	}
	return root, ns, nil
}

// ParseError reports an ARXML file that could not be read without errors.
type ParseError struct {
	Diagnostics []candb.Diagnostic
}

// Error reports the first error diagnostic.
func (e *ParseError) Error() string {
	for _, d := range e.Diagnostics {
		if d.Severity == candb.Error {
			return "arxml: " + d.String()
		}
	}
	return "arxml: invalid file"
}

// ParseFile reads an ARXML file. See [Parse].
func ParseFile(path string) (*candb.Database, []candb.Diagnostic, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse reads ARXML. It returns the database, every diagnostic and, when
// any is an error, a *[ParseError].
func Parse(r io.Reader) (*candb.Database, []candb.Diagnostic, error) {
	root, ns, err := parseTree(r)
	p := &parser{db: &candb.Database{}, paths: map[string]*node{}}
	if err != nil {
		p.diag(candb.Error, 0, "", "XML: %v", err)
		return p.db, p.diags, &ParseError{Diagnostics: p.diags}
	}
	ar := root.child("AUTOSAR")
	switch {
	case ar == nil:
		p.diag(candb.Error, 0, "", "not an AUTOSAR document")
	case !strings.Contains(ns, "/r4.") && ns != "":
		p.diag(candb.Error, ar.line, "", "AUTOSAR schema %q is not supported (AUTOSAR 4.x only)", ns)
	default:
		p.index(ar, "")
		p.run()
	}
	p.diags = append(p.diags, p.db.Validate()...)
	sort.SliceStable(p.diags, func(i, j int) bool { return p.diags[i].Line < p.diags[j].Line })
	if candb.HasErrors(p.diags) {
		return p.db, p.diags, &ParseError{Diagnostics: p.diags}
	}
	return p.db, p.diags, nil
}

type parser struct {
	db    *candb.Database
	diags []candb.Diagnostic
	paths map[string]*node // absolute reference path → element
	ports map[string]port  // frame port path → ECU and direction
	nodes map[string]bool
}

type port struct {
	ecu string
	out bool
}

func (p *parser) diag(sev candb.Severity, line int, obj, format string, args ...any) {
	p.diags = append(p.diags, candb.Diagnostic{Severity: sev, Line: line, Object: obj, Message: fmt.Sprintf(format, args...)})
}

// index records every element with a SHORT-NAME under its reference path.
func (p *parser) index(n *node, prefix string) {
	for _, c := range n.children {
		pre := prefix
		if sn := c.child("SHORT-NAME"); sn != nil {
			pre = prefix + "/" + strings.TrimSpace(sn.text)
			p.paths[pre] = c
		}
		p.index(c, pre)
	}
}

func (p *parser) ref(n *node, name string) *node {
	r := n.child(name)
	if r == nil {
		return nil
	}
	target := p.paths[strings.TrimSpace(r.text)]
	if target == nil {
		p.diag(candb.Warning, r.line, "", "reference %s does not resolve", strings.TrimSpace(r.text))
	}
	return target
}

func shortName(n *node) string { return n.textOf("SHORT-NAME") }

var unsupportedElements = map[string]string{
	"MULTIPLEXED-I-PDU":         "multiplexed I-PDUs",
	"CONTAINER-I-PDU":           "container I-PDUs",
	"SECURED-I-PDU":             "secured I-PDUs",
	"NM-PDU":                    "network management PDUs",
	"DCM-I-PDU":                 "diagnostic PDUs",
	"I-SIGNAL-GROUP":            "signal groups",
	"END-TO-END-PROTECTION-SET": "end-to-end protection",
	"LIN-CLUSTER":               "LIN clusters",
	"FLEXRAY-CLUSTER":           "FlexRay clusters",
	"ETHERNET-CLUSTER":          "Ethernet clusters",
}

func (p *parser) run() {
	seen := map[string]bool{}
	for path, n := range p.paths {
		if what, ok := unsupportedElements[n.name]; ok && !seen[n.name] {
			seen[n.name] = true
			p.diag(candb.Warning, n.line, "", "%s (%s, e.g. %s) are not supported: ignored", what, n.name, path)
		}
	}
	p.collectPorts()
	var paths []string
	for path, n := range p.paths {
		if n.name == "CAN-FRAME-TRIGGERING" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths) // deterministic order
	for _, path := range paths {
		p.triggering(p.paths[path])
	}
	sort.SliceStable(p.db.Messages, func(i, j int) bool {
		a, b := p.db.Messages[i], p.db.Messages[j]
		if a.Extended != b.Extended {
			return !a.Extended
		}
		return a.ID < b.ID
	})
}

// collectPorts maps frame ports to their ECU and direction.
func (p *parser) collectPorts() {
	p.ports = map[string]port{}
	p.nodes = map[string]bool{}
	for path, n := range p.paths {
		if n.name != "ECU-INSTANCE" {
			continue
		}
		ecu := shortName(n)
		for portPath, pn := range p.paths {
			if pn.name == "FRAME-PORT" && strings.HasPrefix(portPath, path+"/") {
				p.ports[portPath] = port{ecu: ecu, out: pn.textOf("COMMUNICATION-DIRECTION") == "OUT"}
			}
		}
	}
}

func (p *parser) node(name string) {
	if !p.nodes[name] {
		p.nodes[name] = true
		p.db.Nodes = append(p.db.Nodes, &candb.Node{Name: name})
	}
}

func (p *parser) triggering(t *node) {
	obj := "frame triggering " + shortName(t)
	id, err := strconv.ParseUint(t.textOf("IDENTIFIER"), 0, 32)
	if err != nil {
		p.diag(candb.Error, t.line, obj, "bad IDENTIFIER %q", t.textOf("IDENTIFIER"))
		return
	}
	frame := p.ref(t, "FRAME-REF")
	if frame == nil {
		return
	}
	if frame.name != "CAN-FRAME" {
		p.diag(candb.Warning, t.line, obj, "frame %s is a %s: ignored", shortName(frame), frame.name)
		return
	}
	m := &candb.Message{ID: canbus.ID(id), Name: shortName(frame)}
	switch mode := t.textOf("CAN-ADDRESSING-MODE"); mode {
	case "", "STANDARD":
	case "EXTENDED":
		m.Extended = true
	default:
		p.diag(candb.Error, t.line, obj, "unknown CAN-ADDRESSING-MODE %q", mode)
		return
	}
	for _, b := range []string{"CAN-FRAME-TX-BEHAVIOR", "CAN-FRAME-RX-BEHAVIOR"} {
		if t.textOf(b) == "CAN-FD" {
			m.FD = true
		}
	}
	if n, err := strconv.Atoi(frame.textOf("FRAME-LENGTH")); err == nil {
		m.Length = n
	} else {
		p.diag(candb.Error, frame.line, "frame "+m.Name, "bad FRAME-LENGTH %q", frame.textOf("FRAME-LENGTH"))
		return
	}
	if m.Length > 8 {
		m.FD = true
	}
	if desc := frame.textOf("DESC", "L-2"); desc != "" {
		m.Comment = desc
	}
	var receivers []string
	if refs := t.child("FRAME-PORT-REFS"); refs != nil {
		for _, r := range refs.children {
			pt, ok := p.ports[strings.TrimSpace(r.text)]
			if !ok {
				continue
			}
			p.node(pt.ecu)
			if pt.out {
				m.Senders = append(m.Senders, pt.ecu)
			} else {
				receivers = append(receivers, pt.ecu)
			}
		}
	}
	maps := frame.child("PDU-TO-FRAME-MAPPINGS")
	if maps == nil {
		p.diag(candb.Warning, frame.line, "frame "+m.Name, "frame without PDUs")
	} else {
		for _, pm := range maps.children {
			p.pdu(m, pm, receivers)
		}
	}
	if p.db.Message(m.ID, m.Extended) != nil {
		p.diag(candb.Error, t.line, obj, "identifier 0x%X triggered twice", id)
		return
	}
	p.db.Messages = append(p.db.Messages, m)
}

func (p *parser) pdu(m *candb.Message, pm *node, receivers []string) {
	pdu := p.ref(pm, "PDU-REF")
	if pdu == nil {
		return
	}
	if pdu.name != "I-SIGNAL-I-PDU" {
		p.diag(candb.Warning, pm.line, "message "+m.Name, "%s %s is not supported: ignored", pdu.name, shortName(pdu))
		return
	}
	offset, _ := strconv.Atoi(pm.textOf("START-POSITION"))
	if offset%8 != 0 {
		p.diag(candb.Error, pm.line, "message "+m.Name, "PDU at bit %d: only byte offsets are supported", offset)
		return
	}
	mappings := pdu.child("I-SIGNAL-TO-PDU-MAPPINGS")
	if mappings == nil {
		return
	}
	for _, sm := range mappings.children {
		if s := p.signal(m, sm, offset, receivers); s != nil {
			if m.Signal(s.Name) != nil {
				p.diag(candb.Error, sm.line, "message "+m.Name, "duplicate signal %s", s.Name)
				continue
			}
			m.Signals = append(m.Signals, s)
		}
	}
}

func (p *parser) signal(m *candb.Message, sm *node, offset int, receivers []string) *candb.Signal {
	if sm.child("I-SIGNAL-REF") == nil {
		if sm.child("I-SIGNAL-GROUP-REF") == nil {
			p.diag(candb.Warning, sm.line, "message "+m.Name, "signal mapping %s without I-SIGNAL-REF ignored", shortName(sm))
		}
		return nil
	}
	is := p.ref(sm, "I-SIGNAL-REF")
	if is == nil {
		return nil
	}
	s := &candb.Signal{Name: shortName(is), Factor: 1, Receivers: append([]string(nil), receivers...)}
	obj := "signal " + m.Name + "." + s.Name
	start, err := strconv.Atoi(sm.textOf("START-POSITION"))
	if err != nil {
		p.diag(candb.Error, sm.line, obj, "bad START-POSITION %q", sm.textOf("START-POSITION"))
		return nil
	}
	s.StartBit = start + offset
	switch o := sm.textOf("PACKING-BYTE-ORDER"); o {
	case "MOST-SIGNIFICANT-BYTE-LAST", "":
	case "MOST-SIGNIFICANT-BYTE-FIRST":
		s.ByteOrder = candb.BigEndian
	case "OPAQUE":
	default:
		p.diag(candb.Error, sm.line, obj, "unknown PACKING-BYTE-ORDER %q", o)
		return nil
	}
	if s.Length, err = strconv.Atoi(is.textOf("LENGTH")); err != nil {
		p.diag(candb.Error, is.line, obj, "bad LENGTH %q", is.textOf("LENGTH"))
		return nil
	}
	if v := is.textOf("INIT-VALUE", "NUMERICAL-VALUE-SPECIFICATION", "VALUE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f == math.Trunc(f) {
			iv := int64(f)
			s.Initial = &iv
		}
	}
	// Base type: signed integers and IEEE 754.
	if bt := p.baseType(is); bt != nil {
		enc := bt.textOf("BASE-TYPE-ENCODING")
		if enc == "" {
			enc = bt.textOf("BASE-TYPE-DEFINITION", "BASE-TYPE-ENCODING")
		}
		switch enc {
		case "2C":
			s.Signed = true
		case "IEEE754":
			switch s.Length {
			case 32:
				s.ValueType = candb.Float32
			case 64:
				s.ValueType = candb.Float64
			}
		}
	}
	if ss := p.ref(is, "SYSTEM-SIGNAL-REF"); ss != nil {
		if desc := ss.textOf("DESC", "L-2"); desc != "" {
			s.Comment = desc
		}
		props := ss.path("PHYSICAL-PROPS", "SW-DATA-DEF-PROPS-VARIANTS", "SW-DATA-DEF-PROPS-CONDITIONAL")
		if props != nil {
			if cm := p.ref(props, "COMPU-METHOD-REF"); cm != nil {
				p.compu(s, cm, obj)
			}
			if u := p.ref(props, "UNIT-REF"); u != nil && s.Unit == "" {
				s.Unit = unitName(u)
			}
			if dc := p.ref(props, "DATA-CONSTR-REF"); dc != nil {
				if rule := dc.path("DATA-CONSTR-RULES", "DATA-CONSTR-RULE", "PHYS-CONSTRS"); rule != nil {
					s.Minimum, _ = strconv.ParseFloat(rule.textOf("LOWER-LIMIT"), 64)
					s.Maximum, _ = strconv.ParseFloat(rule.textOf("UPPER-LIMIT"), 64)
				}
			}
		}
	}
	return s
}

func unitName(u *node) string {
	if d := u.textOf("DISPLAY-NAME"); d != "" {
		return d
	}
	return shortName(u)
}

func (p *parser) baseType(is *node) *node {
	props := is.path("NETWORK-REPRESENTATION-PROPS", "SW-DATA-DEF-PROPS-VARIANTS", "SW-DATA-DEF-PROPS-CONDITIONAL")
	if props == nil {
		return nil
	}
	return p.ref(props, "BASE-TYPE-REF")
}

func coeffs(n *node, name string) []float64 {
	var out []float64
	if c := n.child(name); c != nil {
		for _, v := range c.children {
			if v.name == "V" {
				f, _ := strconv.ParseFloat(strings.TrimSpace(v.text), 64)
				out = append(out, f)
			}
		}
	}
	return out
}

func (p *parser) compu(s *candb.Signal, cm *node, obj string) {
	if u := p.ref(cm, "UNIT-REF"); u != nil {
		s.Unit = unitName(u)
	}
	scales := cm.path("COMPU-INTERNAL-TO-PHYS", "COMPU-SCALES")
	cat := cm.textOf("CATEGORY")
	switch cat {
	case "IDENTICAL", "":
		return
	case "LINEAR", "TEXTTABLE", "SCALE_LINEAR_AND_TEXTTABLE":
	default:
		p.diag(candb.Warning, cm.line, obj, "COMPU-METHOD category %s not supported: identity used", cat)
		return
	}
	if scales == nil {
		p.diag(candb.Warning, cm.line, obj, "COMPU-METHOD %s without scales", shortName(cm))
		return
	}
	linear := 0
	for _, sc := range scales.children {
		if rc := sc.child("COMPU-RATIONAL-COEFFS"); rc != nil {
			num, den := coeffs(rc, "COMPU-NUMERATOR"), coeffs(rc, "COMPU-DENOMINATOR")
			if len(num) < 2 || len(num) > 2 || len(den) > 1 {
				p.diag(candb.Warning, sc.line, obj, "only linear rational coefficients are supported: identity used")
				continue
			}
			d := 1.0
			if len(den) == 1 && den[0] != 0 {
				d = den[0]
			}
			linear++
			if linear > 1 {
				p.diag(candb.Warning, sc.line, obj, "more than one linear scale: the first is used")
				continue
			}
			s.Offset, s.Factor = num[0]/d, num[1]/d
			continue
		}
		if vt := sc.textOf("COMPU-CONST", "VT"); vt != "" {
			lo, err1 := strconv.ParseFloat(sc.textOf("LOWER-LIMIT"), 64)
			hi, err2 := strconv.ParseFloat(sc.textOf("UPPER-LIMIT"), 64)
			if err1 != nil || err2 != nil || hi < lo || hi-lo > 255 {
				p.diag(candb.Warning, sc.line, obj, "text scale %q with limits %q..%q not supported", vt, sc.textOf("LOWER-LIMIT"), sc.textOf("UPPER-LIMIT"))
				continue
			}
			for v := int64(lo); v <= int64(hi); v++ {
				s.Values = append(s.Values, candb.ValueDescription{Value: v, Description: vt})
			}
		}
	}
	candb.SortValues(s.Values)
}
