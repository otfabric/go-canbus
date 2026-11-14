// SPDX-License-Identifier: MIT

// Package arxml reads the CAN part of AUTOSAR 4.x system descriptions
// (ARXML) into the format-independent model of package candb.
//
// Supported: CAN-CLUSTER physical channels with CAN-FRAME-TRIGGERINGs
// (IDENTIFIER, CAN-ADDRESSING-MODE, CAN FD frame behaviour, frame ports
// for senders and receivers), CAN-FRAMEs with PDU-TO-FRAME-MAPPINGs at
// byte offsets, I-SIGNAL-I-PDUs with I-SIGNAL-TO-I-PDU-MAPPINGs
// (PACKING-BYTE-ORDER, START-POSITION), MULTIPLEXED-I-PDUs, I-SIGNALs (LENGTH, INIT-VALUE,
// base type encoding for signed and IEEE 754 signals), SYSTEM-SIGNALs and
// COMPU-METHODs of the categories IDENTICAL, LINEAR, TEXTTABLE and
// SCALE_LINEAR_AND_TEXTTABLE, UNITs, and physical limits from DATA-CONSTRs.
//
// START-POSITION is taken as the DBC start bit in both byte orders, as
// cantools and canmatrix read it (checked in the interop suite).
//
// A MULTIPLEXED-I-PDU becomes multiplexed signals, as cantools reads it: a
// multiplexer switch "<frame>_selector<n>" at the SELECTOR-FIELD (n counts
// the frame's multiplexed PDUs from 1), the signals of each
// DYNAMIC-PART-ALTERNATIVE multiplexed by its SELECTOR-FIELD-CODE, and the
// signals of the STATIC-PARTs always present. An alternative's own mapping
// of the selector field is dropped (its value descriptions go to the
// switch); the INITIAL-DYNAMIC-PART gives the switch's initial value. A
// signal mapped identically in several alternatives is present for each of
// their codes. SEGMENT-POSITIONS are not applied: an alternative's signals
// keep their positions, which lie in the dynamic segment.
//
// Everything else (AUTOSAR 3.x, nested multiplexing, container and secured
// PDUs, signal groups, end-to-end protection, other bus types) is reported
// as a diagnostic and skipped: no ARXML compatibility beyond these
// elements is claimed.
//
// Bounds on untrusted input: elements nested deeper than 256 levels end
// Parse with an error, as does a database of more than 262144 (2^18)
// signals or 2097152 (2^21) value descriptions (each triggering of a frame
// counts its signals again). A PDU mapped more than once into one frame is
// an error and is read once. TEXTTABLE scales are expanded only for
// finite limits of at most 2^53 in magnitude and at most 256 values. At
// most 1000 diagnostics are returned; a final diagnostic, as severe as the
// worst one left out, counts the rest.
package arxml

import (
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
)

// Bounds on untrusted input (see the package documentation).
const (
	maxDepth       = 256     // XML element nesting
	maxSignals     = 1 << 18 // signals in the database
	maxValues      = 1 << 21 // value descriptions in the database
	maxDiagnostics = 1000    // diagnostics returned before the summary
	maxExact       = 1 << 53 // largest TEXTTABLE limit magnitude expanded
	maxBitPosition = 1 << 24 // largest START-POSITION, as in package candb
)

// node is a generic XML element.
type node struct {
	name     string
	attrs    map[string]string
	text     string
	children []*node
	parent   *node
	line     int
	short    string // trimmed SHORT-NAME of an indexed element
	scope    *node  // nearest indexed ancestor (or the AUTOSAR element)
	named    bool   // the element has a SHORT-NAME
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

func (n *node) textOf(names ...string) string {
	if c := n.path(names...); c != nil {
		return strings.TrimSpace(c.text)
	}
	return ""
}

// parseTree reads the document into a tree. Character data is collected
// per open element (linear in the input) and nesting is bounded by
// maxDepth.
func parseTree(r io.Reader) (*node, string, error) {
	dec := xml.NewDecoder(r)
	root := &node{name: "#root"}
	cur := root
	ns := ""
	texts := [][]byte{nil} // character data of the open elements, root first
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
			if len(texts) > maxDepth {
				return nil, "", fmt.Errorf("line %d: elements nested deeper than %d levels", line, maxDepth)
			}
			cur.children = append(cur.children, n)
			cur = n
			texts = append(texts, nil)
		case xml.EndElement:
			cur.text = string(texts[len(texts)-1])
			texts = texts[:len(texts)-1]
			cur = cur.parent
		case xml.CharData:
			texts[len(texts)-1] = append(texts[len(texts)-1], t...)
		}
	}
	root.text = string(texts[0])
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
	f, err := os.Open(filepath.Clean(path))
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
	p := &parser{db: &candb.Database{}, scopes: map[scopeKey]*node{}, msgs: map[msgKey]bool{}, compus: map[*node]*compuResult{}}
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
		p.top = ar
		p.index(ar, ar)
		p.run()
	}
	for _, d := range p.db.Validate() {
		p.keep(d)
	}
	sort.SliceStable(p.diags, func(i, j int) bool { return p.diags[i].Line < p.diags[j].Line })
	if p.dropped > 0 {
		p.diags = append(p.diags, candb.Diagnostic{Severity: p.droppedSev,
			Message: fmt.Sprintf("%d more diagnostics not reported", p.dropped)})
	}
	if candb.HasErrors(p.diags) {
		return p.db, p.diags, &ParseError{Diagnostics: p.diags}
	}
	return p.db, p.diags, nil
}

type parser struct {
	db         *candb.Database
	diags      []candb.Diagnostic
	dropped    int            // diagnostics beyond maxDiagnostics
	droppedSev candb.Severity // worst severity among them
	top        *node          // the AUTOSAR element
	scopes     map[scopeKey]*node
	indexed    []*node        // elements with a SHORT-NAME, in document order
	ports      map[*node]port // frame port → ECU and direction
	nodes      map[string]bool
	msgs       map[msgKey]bool // identifiers of the messages added
	sigs       map[string]*candb.Signal
	muxes      int // multiplexer switches of the current message
	compus     map[*node]*compuResult
	signals    int  // signals built
	values     int  // value descriptions built
	over       bool // a database bound was reached: stop reading
}

// scopeKey names an element by its nearest indexed ancestor and its
// SHORT-NAME: the reference path /A/B/C resolves one segment at a time.
type scopeKey struct {
	scope *node
	name  string
}

type msgKey struct {
	id       canbus.ID
	extended bool
}

type port struct {
	ecu string
	out bool
}

func (p *parser) diag(sev candb.Severity, line int, obj, format string, args ...any) {
	if len(p.diags) >= maxDiagnostics {
		p.drop(sev)
		return
	}
	p.diags = append(p.diags, candb.Diagnostic{Severity: sev, Line: line, Object: obj, Message: fmt.Sprintf(format, args...)})
}

// keep adds d unless maxDiagnostics are kept already.
func (p *parser) keep(d candb.Diagnostic) {
	if len(p.diags) >= maxDiagnostics {
		p.drop(d.Severity)
		return
	}
	p.diags = append(p.diags, d)
}

func (p *parser) drop(sev candb.Severity) {
	p.dropped++
	p.droppedSev = max(p.droppedSev, sev)
}

// overflow reports a database bound once and stops reading.
func (p *parser) overflow(what string, limit int) {
	if !p.over {
		p.over = true
		p.diag(candb.Error, 0, "", "more than %d %s in the database: the rest of the file is not read", limit, what)
	}
}

// index records every element with a SHORT-NAME under its nearest indexed
// ancestor. A later element of the same reference path replaces an
// earlier one.
func (p *parser) index(n, scope *node) {
	for _, c := range n.children {
		sc := scope
		if sn := c.child("SHORT-NAME"); sn != nil {
			c.short, c.scope, c.named = strings.TrimSpace(sn.text), scope, true
			p.scopes[scopeKey{scope, c.short}] = c
			p.indexed = append(p.indexed, c)
			sc = c
		}
		p.index(c, sc)
	}
}

// live reports whether n is the element its reference path resolves to.
func (p *parser) live(n *node) bool {
	return n.named && p.scopes[scopeKey{n.scope, n.short}] == n
}

// resolve finds the element of an absolute reference path.
func (p *parser) resolve(path string) *node {
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return nil
	}
	cur := p.top
	for cur != nil {
		seg, tail, more := strings.Cut(rest, "/")
		cur = p.scopes[scopeKey{cur, seg}]
		if !more {
			return cur
		}
		rest = tail
	}
	return nil
}

// pathOf returns the reference path of an indexed element.
func (p *parser) pathOf(n *node) string {
	var segs []string
	for ; n != nil && n != p.top; n = n.scope {
		segs = append(segs, n.short)
	}
	var b strings.Builder
	for i := len(segs) - 1; i >= 0; i-- {
		b.WriteString("/" + segs[i])
	}
	return b.String()
}

func (p *parser) ref(n *node, name string) *node {
	r := n.child(name)
	if r == nil {
		return nil
	}
	target := p.resolve(strings.TrimSpace(r.text))
	if target == nil {
		p.diag(candb.Warning, r.line, "", "reference %s does not resolve", strings.TrimSpace(r.text))
	}
	return target
}

func shortName(n *node) string { return n.textOf("SHORT-NAME") }

var unsupportedElements = map[string]string{
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
	for _, n := range p.indexed {
		if what, ok := unsupportedElements[n.name]; ok && !seen[n.name] && p.live(n) {
			seen[n.name] = true
			p.diag(candb.Warning, n.line, "", "%s (%s, e.g. %s) are not supported: ignored", what, n.name, p.pathOf(n))
		}
	}
	p.collectPorts()
	for _, n := range p.indexed { // document order
		if p.over {
			break
		}
		if n.name == "CAN-FRAME-TRIGGERING" && p.live(n) {
			p.triggering(n)
		}
	}
	sort.SliceStable(p.db.Messages, func(i, j int) bool {
		a, b := p.db.Messages[i], p.db.Messages[j]
		if a.Extended != b.Extended {
			return !a.Extended
		}
		return a.ID < b.ID
	})
}

// collectPorts maps frame ports to their ECU and direction, walking each
// ECU-INSTANCE's subtree once.
func (p *parser) collectPorts() {
	p.ports = map[*node]port{}
	p.nodes = map[string]bool{}
	for _, n := range p.indexed {
		if n.name == "ECU-INSTANCE" && p.live(n) {
			p.portsUnder(n, shortName(n))
		}
	}
}

// portsUnder records the frame ports below n for ecu; a nested
// ECU-INSTANCE records its own.
func (p *parser) portsUnder(n *node, ecu string) {
	for _, c := range n.children {
		switch {
		case c.name == "ECU-INSTANCE" && p.live(c):
			continue
		case c.name == "FRAME-PORT" && p.live(c):
			p.ports[c] = port{ecu: ecu, out: c.textOf("COMMUNICATION-DIRECTION") == "OUT"}
		}
		p.portsUnder(c, ecu)
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
	p.sigs, p.muxes = map[string]*candb.Signal{}, 0
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
			pt, ok := p.ports[p.resolve(strings.TrimSpace(r.text))]
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
		mappedPDUs := map[*node]bool{}
		for _, pm := range maps.children {
			if p.over {
				return
			}
			pdu := p.ref(pm, "PDU-REF")
			if pdu == nil {
				continue
			}
			if mappedPDUs[pdu] {
				p.diag(candb.Error, pm.line, "message "+m.Name, "PDU %s mapped more than once: read once", shortName(pdu))
				continue
			}
			mappedPDUs[pdu] = true
			p.pdu(m, pm, pdu, receivers)
		}
	}
	if p.over {
		return
	}
	key := msgKey{m.ID, m.Extended}
	if p.msgs[key] {
		p.diag(candb.Error, t.line, obj, "identifier 0x%X triggered twice", id)
		return
	}
	p.msgs[key] = true
	p.db.Messages = append(p.db.Messages, m)
}

func (p *parser) pdu(m *candb.Message, pm, pdu *node, receivers []string) {
	offset, _ := strconv.Atoi(pm.textOf("START-POSITION"))
	if offset < 0 || offset > maxBitPosition {
		p.diag(candb.Error, pm.line, "message "+m.Name, "PDU at bit %d: beyond %d", offset, maxBitPosition)
		return
	}
	if offset%8 != 0 {
		p.diag(candb.Error, pm.line, "message "+m.Name, "PDU at bit %d: only byte offsets are supported", offset)
		return
	}
	switch pdu.name {
	case "I-SIGNAL-I-PDU":
		for _, s := range p.pduSignals(m, pdu, offset, receivers) {
			p.add(m, s)
		}
	case "MULTIPLEXED-I-PDU":
		p.multiplexed(m, pdu, offset, receivers)
	default:
		p.diag(candb.Warning, pm.line, "message "+m.Name, "%s %s is not supported: ignored", pdu.name, shortName(pdu))
	}
}

// mapped is a signal and the line of its mapping.
type mapped struct {
	*candb.Signal
	line int
}

// pduSignals reads the signals of an I-SIGNAL-I-PDU at offset bits of the
// frame.
func (p *parser) pduSignals(m *candb.Message, pdu *node, offset int, receivers []string) []mapped {
	mappings := pdu.child("I-SIGNAL-TO-PDU-MAPPINGS")
	if mappings == nil {
		return nil
	}
	var out []mapped
	for _, sm := range mappings.children {
		if p.over {
			return out
		}
		if s := p.signal(m, sm, offset, receivers); s != nil {
			out = append(out, mapped{s, sm.line})
		}
	}
	return out
}

// add appends a signal; a second signal of the same name is an error,
// unless both are multiplexed by the same switch with the same layout:
// then the first is present for the second's switch values too.
func (p *parser) add(m *candb.Message, s mapped) {
	prev := p.sigs[s.Name]
	if prev == nil {
		p.sigs[s.Name] = s.Signal
		m.Signals = append(m.Signals, s.Signal)
		return
	}
	if prev.Multiplexer != "" && prev.Multiplexer == s.Multiplexer && prev.StartBit == s.StartBit &&
		prev.Length == s.Length && prev.ByteOrder == s.ByteOrder {
		prev.MuxValues = append(prev.MuxValues, s.MuxValues...)
		return
	}
	p.diag(candb.Error, s.line, "message "+m.Name, "duplicate signal %s", s.Name)
}

// multiplexed reads a MULTIPLEXED-I-PDU at offset bits of the frame.
func (p *parser) multiplexed(m *candb.Message, pdu *node, offset int, receivers []string) {
	obj := "multiplexed PDU " + shortName(pdu)
	start, err1 := strconv.Atoi(pdu.textOf("SELECTOR-FIELD-START-POSITION"))
	length, err2 := strconv.Atoi(pdu.textOf("SELECTOR-FIELD-LENGTH"))
	if err1 != nil || err2 != nil || length < 1 || length > 64 {
		p.diag(candb.Error, pdu.line, obj, "bad selector field (SELECTOR-FIELD-START-POSITION %q, SELECTOR-FIELD-LENGTH %q)",
			pdu.textOf("SELECTOR-FIELD-START-POSITION"), pdu.textOf("SELECTOR-FIELD-LENGTH"))
		return
	}
	n := p.muxes + 1
	sel := &candb.Signal{Name: fmt.Sprintf("%s_selector%d", m.Name, n), StartBit: start + offset, Length: length,
		Factor: 1, IsMultiplexer: true, Receivers: append([]string(nil), receivers...)}
	switch o := pdu.textOf("SELECTOR-FIELD-BYTE-ORDER"); o {
	case "MOST-SIGNIFICANT-BYTE-LAST", "":
	case "MOST-SIGNIFICANT-BYTE-FIRST":
		sel.ByteOrder = candb.BigEndian
	default:
		p.diag(candb.Error, pdu.line, obj, "unknown SELECTOR-FIELD-BYTE-ORDER %q", o)
		return
	}
	if p.sigs[sel.Name] != nil {
		p.diag(candb.Error, pdu.line, obj, "duplicate signal %s", sel.Name)
		return
	}
	p.sigs[sel.Name] = sel
	p.muxes++
	m.Signals = append(m.Signals, sel)
	var alts []*node
	if parts := pdu.child("DYNAMIC-PARTS"); parts != nil { // AUTOSAR 4.1 and later
		for _, part := range parts.children {
			if a := part.child("DYNAMIC-PART-ALTERNATIVES"); a != nil {
				alts = append(alts, a.children...)
			}
		}
	} else if a := pdu.path("DYNAMIC-PART", "DYNAMIC-PART-ALTERNATIVES"); a != nil {
		alts = a.children
	}
	if len(alts) == 0 {
		p.diag(candb.Warning, pdu.line, obj, "no dynamic part alternatives")
	}
	seen := map[string]bool{}
	for _, alt := range alts {
		if p.over {
			return
		}
		code, err := strconv.ParseUint(alt.textOf("SELECTOR-FIELD-CODE"), 0, 64)
		if err != nil || length < 64 && code >= 1<<uint(length) {
			p.diag(candb.Error, alt.line, obj, "bad SELECTOR-FIELD-CODE %q", alt.textOf("SELECTOR-FIELD-CODE"))
			continue
		}
		ipdu := p.ref(alt, "I-PDU-REF")
		if ipdu == nil {
			continue
		}
		if ipdu.name != "I-SIGNAL-I-PDU" {
			p.diag(candb.Warning, alt.line, obj, "alternative %s is a %s: ignored", shortName(ipdu), ipdu.name)
			continue
		}
		if alt.textOf("INITIAL-DYNAMIC-PART") == "true" {
			if code <= math.MaxInt64 {
				iv := int64(code)
				sel.Initial = &iv
			} else {
				p.diag(candb.Warning, alt.line, obj, "initial SELECTOR-FIELD-CODE %d beyond an initial value: ignored", code)
			}
		}
		for _, s := range p.pduSignals(m, ipdu, offset, receivers) {
			if s.StartBit == sel.StartBit && s.Length == sel.Length {
				// The alternative's mapping of the selector field.
				for _, v := range s.Values {
					if !seen[v.Description] {
						seen[v.Description] = true
						sel.Values = append(sel.Values, v)
					}
				}
				continue
			}
			s.Multiplexer, s.MuxValues = sel.Name, []candb.MuxRange{{Low: code, High: code}}
			p.add(m, s)
		}
	}
	var statics []*node
	if parts := pdu.child("STATIC-PARTS"); parts != nil {
		statics = parts.children
	} else if part := pdu.child("STATIC-PART"); part != nil {
		statics = []*node{part}
	}
	for _, part := range statics {
		if p.over {
			return
		}
		ipdu := p.ref(part, "I-PDU-REF")
		if ipdu == nil {
			continue
		}
		if ipdu.name != "I-SIGNAL-I-PDU" {
			p.diag(candb.Warning, part.line, obj, "static part %s is a %s: ignored", shortName(ipdu), ipdu.name)
			continue
		}
		for _, s := range p.pduSignals(m, ipdu, offset, receivers) {
			p.add(m, s)
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
	if p.signals >= maxSignals {
		p.overflow("signals", maxSignals)
		return nil
	}
	p.signals++
	s := &candb.Signal{Name: shortName(is), Factor: 1, Receivers: append([]string(nil), receivers...)}
	obj := "signal " + m.Name + "." + s.Name
	start, err := strconv.Atoi(sm.textOf("START-POSITION"))
	if err != nil || start < 0 || start > maxBitPosition {
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
		if f, err := strconv.ParseFloat(v, 64); err == nil && f == math.Trunc(f) && f >= math.MinInt64 && f < math.MaxInt64 {
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
				p.applyCompu(s, cm, obj)
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

// compuResult is what a COMPU-METHOD contributes to a signal; it is
// computed once per COMPU-METHOD and copied to every signal using it.
type compuResult struct {
	unit           *string
	linear         bool
	offset, factor float64
	values         []candb.ValueDescription
}

// applyCompu applies the COMPU-METHOD cm to s.
func (p *parser) applyCompu(s *candb.Signal, cm *node, obj string) {
	c := p.compus[cm]
	if c == nil {
		c = p.compu(cm, obj)
		p.compus[cm] = c
	}
	if c.unit != nil {
		s.Unit = *c.unit
	}
	if c.linear {
		s.Offset, s.Factor = c.offset, c.factor
	}
	if len(c.values) == 0 {
		return
	}
	if p.values+len(c.values) > maxValues {
		p.overflow("value descriptions", maxValues)
		return
	}
	p.values += len(c.values)
	s.Values = append([]candb.ValueDescription(nil), c.values...)
}

func (p *parser) compu(cm *node, obj string) *compuResult {
	c := &compuResult{}
	if u := p.ref(cm, "UNIT-REF"); u != nil {
		name := unitName(u)
		c.unit = &name
	}
	scales := cm.path("COMPU-INTERNAL-TO-PHYS", "COMPU-SCALES")
	cat := cm.textOf("CATEGORY")
	switch cat {
	case "IDENTICAL", "":
		return c
	case "LINEAR", "TEXTTABLE", "SCALE_LINEAR_AND_TEXTTABLE":
	default:
		p.diag(candb.Warning, cm.line, obj, "COMPU-METHOD category %s not supported: identity used", cat)
		return c
	}
	if scales == nil {
		p.diag(candb.Warning, cm.line, obj, "COMPU-METHOD %s without scales", shortName(cm))
		return c
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
			c.linear, c.offset, c.factor = true, num[0]/d, num[1]/d
			continue
		}
		if vt := sc.textOf("COMPU-CONST", "VT"); vt != "" {
			lo, err1 := strconv.ParseFloat(sc.textOf("LOWER-LIMIT"), 64)
			hi, err2 := strconv.ParseFloat(sc.textOf("UPPER-LIMIT"), 64)
			// NaN, infinities and limits beyond 2^53 have no exact int64
			// conversion; the comparisons below are false for NaN.
			if err1 != nil || err2 != nil || !(math.Abs(lo) <= maxExact) || !(math.Abs(hi) <= maxExact) || hi < lo || hi-lo > 255 {
				p.diag(candb.Warning, sc.line, obj, "text scale %q with limits %q..%q not supported", vt, sc.textOf("LOWER-LIMIT"), sc.textOf("UPPER-LIMIT"))
				continue
			}
			first, last := int64(lo), int64(hi)
			for i := int64(0); i <= last-first; i++ { // at most 256 values
				c.values = append(c.values, candb.ValueDescription{Value: first + i, Description: vt})
			}
		}
	}
	candb.SortValues(c.values)
	return c
}
