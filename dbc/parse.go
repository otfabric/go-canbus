// SPDX-License-Identifier: MIT

// Package dbc reads and writes DBC files into the format-independent
// model of package candb.
//
// Supported: VERSION, BU_, VAL_TABLE_, BO_ and SG_ (Intel and Motorola,
// signed and unsigned, scaling, limits, units, receivers, simple and
// extended multiplexing), BO_TX_BU_, CM_ for the network, nodes, messages
// and signals, BA_DEF_ / BA_DEF_DEF_ / BA_ for the network, nodes,
// messages and signals, VAL_, SIG_VALTYPE_ (float and double signals),
// SG_MUL_VAL_ and SIG_GROUP_. The attributes GenMsgCycleTime,
// GenSigStartValue, VFrameFormat and CANFD_BRS are also mapped onto the
// model's cycle time, initial value and CAN FD flags.
//
// Everything else (environment variables, signal types, relation
// attributes, categories, filters) is skipped with a warning diagnostic;
// nothing is dropped silently. There is no public specification of the
// format: behaviour follows the de facto Vector conventions and is
// cross-checked against cantools and canmatrix (see INTEROPERABILITY.md).
package dbc

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
)

// ParseError reports a DBC file that could not be parsed without errors.
// The database and every diagnostic are still returned by [Parse].
type ParseError struct {
	Diagnostics []candb.Diagnostic
}

// Error reports the first error diagnostic and how many there are.
func (e *ParseError) Error() string {
	n := 0
	var first candb.Diagnostic
	for _, d := range e.Diagnostics {
		if d.Severity == candb.Error {
			if n == 0 {
				first = d
			}
			n++
		}
	}
	if n > 1 {
		return fmt.Sprintf("dbc: %s (and %d more errors)", first, n-1)
	}
	return "dbc: " + first.String()
}

// ParseFile parses a DBC file. See [Parse].
func ParseFile(path string) (*candb.Database, []candb.Diagnostic, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParseBytes(b)
}

// Parse parses DBC source. It returns the database, every diagnostic
// (info, warnings and errors, sorted by line) and, when any diagnostic is
// an error, a *[ParseError]: the database then describes only what could
// be parsed and validated.
func Parse(r io.Reader) (*candb.Database, []candb.Diagnostic, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	return ParseBytes(b)
}

// ParseBytes parses DBC source held in memory. See [Parse].
func ParseBytes(src []byte) (*candb.Database, []candb.Diagnostic, error) {
	s := string(src)
	s = strings.TrimPrefix(s, "\ufeff")
	toks, err := lex(s)
	if err != nil {
		d := []candb.Diagnostic{{Severity: candb.Error, Message: err.Error()}}
		return &candb.Database{}, d, &ParseError{Diagnostics: d}
	}
	p := &parser{toks: toks, db: &candb.Database{}, msgs: map[msgKey]*candb.Message{},
		valTypes: map[sigKey]int{}, muxVals: map[sigKey][]muxVal{}, muxInd: map[sigKey]string{}}
	p.run()
	p.finish()
	sortDiagnostics(p.diags)
	if candb.HasErrors(p.diags) {
		return p.db, p.diags, &ParseError{Diagnostics: p.diags}
	}
	return p.db, p.diags, nil
}

type msgKey uint32 // the DBC identifier with bit 31 for extended

type sigKey struct {
	msg msgKey
	sig string
}

type muxVal struct {
	sw     string
	ranges []candb.MuxRange
	line   int
}

type parser struct {
	toks  []token
	pos   int
	db    *candb.Database
	diags []candb.Diagnostic

	msgs     map[msgKey]*candb.Message
	skipMsg  map[msgKey]bool
	valTypes map[sigKey]int
	muxVals  map[sigKey][]muxVal
	muxInd   map[sigKey]string // the raw multiplexer indicator: "M", "m3", "m3M"
	attrs    []pendingAttr
	defaults map[string]candb.Attribute
}

type pendingAttr struct {
	obj  candb.ObjectType
	node string
	msg  msgKey
	sig  string
	attr candb.Attribute
	raw  token // the value token, for enum labels given as strings
	line int
}

// errSyntax aborts a statement; run recovers at the next keyword.
type errSyntax struct {
	line int
	msg  string
}

func (p *parser) diag(sev candb.Severity, line int, obj, format string, args ...any) {
	p.diags = append(p.diags, candb.Diagnostic{Severity: sev, Line: line, Object: obj, Message: fmt.Sprintf(format, args...)})
}

func (p *parser) peek() token { return p.toks[p.pos] }

// atStatement reports a keyword at the start of a line: where a
// statement starts.
func (p *parser) atStatement() bool {
	t := p.peek()
	return t.kind == tIdent && keywords[t.text] && t.col == 0
}

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tEOF {
		p.pos++
	}
	return t
}

func (p *parser) fail(t token, format string, args ...any) {
	panic(errSyntax{line: t.line, msg: fmt.Sprintf(format, args...) + ", got " + t.String()})
}

func (p *parser) expectPunct(s string) token {
	t := p.next()
	if t.kind != tPunct || t.text != s {
		p.fail(t, "expected %q", s)
	}
	return t
}

func (p *parser) acceptPunct(s string) bool {
	if t := p.peek(); t.kind == tPunct && t.text == s {
		p.pos++
		return true
	}
	return false
}

func (p *parser) ident() string {
	t := p.next()
	if t.kind != tIdent {
		p.fail(t, "expected an identifier")
	}
	return t.text
}

func (p *parser) str() string {
	t := p.next()
	if t.kind != tString {
		p.fail(t, "expected a string")
	}
	return t.text
}

// number reads an optionally signed number.
func (p *parser) number() float64 {
	neg := false
	if p.acceptPunct("-") {
		neg = true
	} else {
		p.acceptPunct("+")
	}
	t := p.next()
	if t.kind != tNumber {
		p.fail(t, "expected a number")
	}
	var v float64
	var err error
	if strings.HasPrefix(t.text, "0x") || strings.HasPrefix(t.text, "0X") {
		var u uint64
		u, err = strconv.ParseUint(t.text[2:], 16, 64)
		v = float64(u)
	} else {
		v, err = strconv.ParseFloat(t.text, 64)
	}
	if err != nil {
		p.fail(t, "bad number")
	}
	if neg {
		v = -v
	}
	return v
}

// integer reads an optionally signed integer exactly.
func (p *parser) integer() int64 {
	neg := p.acceptPunct("-")
	if !neg {
		p.acceptPunct("+")
	}
	t := p.next()
	if t.kind != tNumber {
		p.fail(t, "expected an integer")
	}
	var u uint64
	var err error
	if strings.HasPrefix(t.text, "0x") || strings.HasPrefix(t.text, "0X") {
		u, err = strconv.ParseUint(t.text[2:], 16, 64)
	} else {
		u, err = strconv.ParseUint(t.text, 10, 64)
		if err != nil {
			// Tools write integral values as "5.0" or "1e3".
			if f, ferr := strconv.ParseFloat(t.text, 64); ferr == nil && f == math.Trunc(f) && math.Abs(f) < 1<<63 {
				u, err = uint64(math.Abs(f)), nil
			}
		}
	}
	if err != nil || (!neg && u > math.MaxInt64) || (neg && u > 1<<63) {
		p.fail(t, "bad integer")
	}
	if neg {
		return -int64(u)
	}
	return int64(u)
}

func (p *parser) uinteger() uint64 {
	t := p.next()
	if t.kind != tNumber {
		p.fail(t, "expected an unsigned integer")
	}
	u, err := strconv.ParseUint(t.text, 0, 64)
	if err != nil {
		p.fail(t, "bad unsigned integer")
	}
	return u
}

// skipStatement skips to the next ';' (inclusive).
func (p *parser) skipStatement() {
	for {
		t := p.next()
		if t.kind == tEOF || t.kind == tPunct && t.text == ";" {
			return
		}
	}
}

var keywords = map[string]bool{
	"VERSION": true, "NS_": true, "BS_": true, "BU_": true, "VAL_TABLE_": true, "BO_": true, "BO_TX_BU_": true,
	"CM_": true, "BA_DEF_": true, "BA_DEF_DEF_": true, "BA_": true, "VAL_": true, "SIG_VALTYPE_": true,
	"SG_MUL_VAL_": true, "SIG_GROUP_": true, "EV_": true, "ENVVAR_DATA_": true, "SGTYPE_": true, "SGTYPE_VAL_": true,
	"BA_DEF_SGTYPE_": true, "BA_SGTYPE_": true, "SIG_TYPE_REF_": true, "BA_DEF_REL_": true, "BA_REL_": true,
	"BA_DEF_DEF_REL_": true, "BU_SG_REL_": true, "BU_EV_REL_": true, "BU_BO_REL_": true, "CAT_DEF_": true,
	"CAT_": true, "FILTER": true, "EV_DATA_": true, "SIGTYPE_VALTYPE_": true, "VAL_TABLE": true,
}

// unsupported are statements that are skipped with a warning.
var unsupported = map[string]string{
	"EV_":              "environment variables",
	"ENVVAR_DATA_":     "environment variable data",
	"EV_DATA_":         "environment variable data",
	"SGTYPE_":          "signal types",
	"SGTYPE_VAL_":      "signal type value descriptions",
	"BA_DEF_SGTYPE_":   "signal type attributes",
	"BA_SGTYPE_":       "signal type attributes",
	"SIG_TYPE_REF_":    "signal type references",
	"SIGTYPE_VALTYPE_": "signal type value types",
	"BA_DEF_REL_":      "relation attribute definitions",
	"BA_REL_":          "relation attributes",
	"BA_DEF_DEF_REL_":  "relation attribute defaults",
	"BU_SG_REL_":       "node-signal relations",
	"BU_EV_REL_":       "node-environment variable relations",
	"BU_BO_REL_":       "node-message relations",
	"CAT_DEF_":         "categories",
	"CAT_":             "categories",
	"FILTER":           "filters",
}

func (p *parser) run() {
	for p.peek().kind != tEOF {
		start := p.pos
		func() {
			defer func() {
				if r := recover(); r != nil {
					e, ok := r.(errSyntax)
					if !ok {
						panic(r)
					}
					p.diag(candb.Error, e.line, "", "%s", e.msg)
					// Recover at the next keyword that starts a statement.
					if p.pos == start {
						p.pos++
					}
					for p.peek().kind != tEOF && !p.atStatement() {
						p.pos++
					}
				}
			}()
			p.statement()
		}()
	}
}

func (p *parser) statement() {
	t := p.next()
	if t.kind != tIdent {
		p.fail(t, "expected a keyword")
	}
	switch t.text {
	case "VERSION":
		p.db.Version = p.str()
	case "NS_":
		p.expectPunct(":")
		// The new-symbols list: indented names up to the next line that
		// starts at column 0.
		for p.peek().kind == tIdent && p.peek().col > 0 {
			p.pos++
		}
	case "BS_":
		p.expectPunct(":")
		if p.peek().kind == tNumber {
			p.number()
			p.expectPunct(":")
			p.number()
			p.expectPunct(",")
			p.number()
			p.diag(candb.Info, t.line, "", "BS_ bit timing ignored (obsolete)")
		}
	case "BU_":
		p.expectPunct(":")
		for p.peek().kind == tIdent && !keywords[p.peek().text] {
			name := p.ident()
			if p.db.Node(name) == nil {
				p.db.Nodes = append(p.db.Nodes, &candb.Node{Name: name})
			}
		}
	case "VAL_TABLE_":
		vt := candb.ValueTable{Name: p.ident()}
		vt.Values = p.valueDescriptions()
		p.expectPunct(";")
		p.db.ValueTables = append(p.db.ValueTables, vt)
	case "BO_":
		p.message(t)
	case "SG_":
		p.fail(t, "SG_ outside a message")
	case "BO_TX_BU_":
		id := msgKey(p.uinteger())
		p.expectPunct(":")
		var senders []string
		for {
			senders = append(senders, p.ident())
			if !p.acceptPunct(",") {
				break
			}
		}
		p.expectPunct(";")
		if m := p.msg(id, t); m != nil {
			for _, s := range senders {
				if !contains(m.Senders, s) {
					m.Senders = append(m.Senders, s)
				}
			}
		}
	case "CM_":
		p.comment(t)
	case "BA_DEF_":
		p.attrDef(t)
	case "BA_DEF_DEF_":
		name := p.str()
		val := p.next()
		if val.kind == tPunct && val.text == "-" {
			val = p.next()
			val.text = "-" + val.text
		}
		p.expectPunct(";")
		if p.defaults == nil {
			p.defaults = map[string]candb.Attribute{}
		}
		p.defaults[name] = candb.Attribute{Name: name, String: val.text}
	case "BA_":
		p.attrValue(t)
	case "VAL_":
		p.values(t)
	case "SIG_VALTYPE_":
		id := msgKey(p.uinteger())
		sig := p.ident()
		p.acceptPunct(":")
		vt := p.integer()
		p.expectPunct(";")
		if vt < 0 || vt > 2 {
			p.diag(candb.Error, t.line, "signal "+sig, "SIG_VALTYPE_ %d unknown (0 integer, 1 float, 2 double)", vt)
			return
		}
		p.valTypes[sigKey{id, sig}] = int(vt)
	case "SG_MUL_VAL_":
		id := msgKey(p.uinteger())
		sig := p.ident()
		sw := p.ident()
		var rs []candb.MuxRange
		for {
			lo := p.uinteger()
			p.expectPunct("-")
			hi := p.uinteger()
			if hi < lo {
				p.diag(candb.Error, t.line, "signal "+sig, "multiplexer range %d-%d is empty", lo, hi)
			}
			rs = append(rs, candb.MuxRange{Low: lo, High: hi})
			if !p.acceptPunct(",") {
				break
			}
		}
		p.expectPunct(";")
		k := sigKey{id, sig}
		p.muxVals[k] = append(p.muxVals[k], muxVal{sw: sw, ranges: rs, line: t.line})
	case "SIG_GROUP_":
		id := msgKey(p.uinteger())
		g := candb.SignalGroup{Name: p.ident(), Repetitions: int(p.integer())}
		p.expectPunct(":")
		for p.peek().kind == tIdent {
			g.Signals = append(g.Signals, p.ident())
			p.acceptPunct(",")
		}
		p.expectPunct(";")
		if m := p.msg(id, t); m != nil {
			m.SignalGroups = append(m.SignalGroups, g)
		}
	default:
		if what, ok := unsupported[t.text]; ok {
			p.diag(candb.Warning, t.line, "", "%s (%s) are not supported: ignored", what, t.text)
			p.skipStatement()
			return
		}
		p.fail(t, "unknown keyword")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// msg finds a message by DBC identifier, reporting unknown ones.
func (p *parser) msg(id msgKey, at token) *candb.Message {
	if p.skipMsg[id] {
		return nil
	}
	m := p.msgs[id]
	if m == nil {
		p.diag(candb.Warning, at.line, "", "%s refers to unknown message %d: ignored", at.text, uint32(id))
	}
	return m
}

func (p *parser) valueDescriptions() []candb.ValueDescription {
	var vs []candb.ValueDescription
	for p.peek().kind == tNumber || p.peek().kind == tPunct && p.peek().text == "-" {
		v := p.integer()
		vs = append(vs, candb.ValueDescription{Value: v, Description: p.str()})
	}
	candb.SortValues(vs)
	return vs
}

// independentSignals is the identifier of Vector's pseudo message that
// holds signals not assigned to any message.
const independentSignals = 0xC0000000

func (p *parser) message(t token) {
	rawID := p.uinteger()
	if rawID > math.MaxUint32 {
		p.fail(t, "message identifier %d out of range", rawID)
	}
	name := p.ident()
	p.expectPunct(":")
	length := p.integer()
	sender := p.ident()
	id := msgKey(rawID)
	m := &candb.Message{Name: name, Length: int(length)}
	if rawID&0x80000000 != 0 {
		m.Extended = true
		m.ID = canbus.ID(rawID & 0x1FFFFFFF)
	} else {
		m.ID = canbus.ID(rawID)
	}
	if sender != "Vector__XXX" {
		m.Senders = []string{sender}
	}
	skip := rawID == independentSignals
	if skip {
		p.diag(candb.Info, t.line, "message "+name, "pseudo message of unassigned signals ignored")
	} else if rawID&0x60000000 != 0 {
		p.diag(candb.Error, t.line, "message "+name, "identifier %d has bits 29..30 set", rawID)
		skip = true
	}
	if length < 0 || length > 64 {
		p.diag(candb.Error, t.line, "message "+name, "length %d out of range", length)
		skip = true
	}
	for p.peek().kind == tIdent && p.peek().text == "SG_" {
		st := p.next()
		s := p.signalRecover(st, id)
		if s != nil && !skip {
			if m.Signal(s.Name) != nil {
				p.diag(candb.Error, st.line, "signal "+name+"."+s.Name, "duplicate signal")
				continue
			}
			m.Signals = append(m.Signals, s)
		}
	}
	if skip {
		if p.skipMsg == nil {
			p.skipMsg = map[msgKey]bool{}
		}
		p.skipMsg[id] = true
		return
	}
	if _, dup := p.msgs[id]; dup {
		p.diag(candb.Error, t.line, "message "+name, "duplicate message identifier %d", rawID)
		return
	}
	p.msgs[id] = m
	p.db.Messages = append(p.db.Messages, m)
}

// signalRecover parses a signal; on a syntax error it reports it and skips
// to the next signal or statement, so the rest of the message survives.
func (p *parser) signalRecover(t token, id msgKey) (s *candb.Signal) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(errSyntax)
			if !ok {
				panic(r)
			}
			p.diag(candb.Error, e.line, "", "%s", e.msg)
			for p.peek().kind != tEOF && !p.atStatement() && (p.peek().kind != tIdent || p.peek().text != "SG_") {
				p.pos++
			}
			s = nil
		}
	}()
	return p.signal(t, id)
}

func (p *parser) signal(t token, id msgKey) *candb.Signal {
	s := &candb.Signal{Name: p.ident()}
	if p.peek().kind == tIdent {
		ind := p.ident()
		if !validMuxIndicator(ind) {
			p.fail(t, "bad multiplexer indicator %q", ind)
		}
		p.muxInd[sigKey{id, s.Name}] = ind
	}
	p.expectPunct(":")
	start := p.integer()
	p.expectPunct("|")
	length := p.integer()
	p.expectPunct("@")
	order := p.next()
	switch {
	case order.kind == tNumber && order.text == "1":
		s.ByteOrder = candb.LittleEndian
	case order.kind == tNumber && order.text == "0":
		s.ByteOrder = candb.BigEndian
	default:
		p.fail(order, "expected byte order 0 or 1")
	}
	sign := p.next()
	switch {
	case sign.kind == tPunct && sign.text == "+":
	case sign.kind == tPunct && sign.text == "-":
		s.Signed = true
	default:
		p.fail(sign, "expected sign + or -")
	}
	p.expectPunct("(")
	s.Factor = p.number()
	p.expectPunct(",")
	s.Offset = p.number()
	p.expectPunct(")")
	p.expectPunct("[")
	s.Minimum = p.number()
	p.expectPunct("|")
	s.Maximum = p.number()
	p.expectPunct("]")
	s.Unit = p.str()
	for p.peek().kind == tIdent && !keywords[p.peek().text] && p.peek().text != "SG_" {
		r := p.ident()
		if r != "Vector__XXX" {
			s.Receivers = append(s.Receivers, r)
		}
		if !p.acceptPunct(",") {
			break
		}
	}
	if start < 0 || start > 511 || length < 1 || length > 64 {
		p.diag(candb.Error, t.line, "signal "+s.Name, "start bit %d, length %d out of range", start, length)
		return nil
	}
	s.StartBit, s.Length = int(start), int(length)
	return s
}

func validMuxIndicator(s string) bool {
	if s == "M" {
		return true
	}
	if len(s) < 2 || s[0] != 'm' {
		return false
	}
	digits := strings.TrimSuffix(s[1:], "M")
	if digits == "" {
		return false
	}
	_, err := strconv.ParseUint(digits, 10, 64)
	return err == nil
}

func (p *parser) comment(t token) {
	if p.peek().kind == tString {
		p.db.Comment = p.str()
		p.expectPunct(";")
		return
	}
	kind := p.ident()
	switch kind {
	case "BU_":
		name := p.ident()
		text := p.str()
		p.expectPunct(";")
		if n := p.db.Node(name); n != nil {
			n.Comment = text
		} else {
			p.diag(candb.Warning, t.line, "", "comment for unknown node %s ignored", name)
		}
	case "BO_":
		id := msgKey(p.uinteger())
		text := p.str()
		p.expectPunct(";")
		if m := p.msg(id, t); m != nil {
			m.Comment = text
		}
	case "SG_":
		id := msgKey(p.uinteger())
		sig := p.ident()
		text := p.str()
		p.expectPunct(";")
		if m := p.msg(id, t); m != nil {
			if s := m.Signal(sig); s != nil {
				s.Comment = text
			} else {
				p.diag(candb.Warning, t.line, "", "comment for unknown signal %s.%s ignored", m.Name, sig)
			}
		}
	case "EV_":
		p.skipStatement()
		p.diag(candb.Warning, t.line, "", "comments on environment variables are not supported: ignored")
	default:
		p.fail(t, "unknown comment target %q", kind)
	}
}

var objOf = map[string]candb.ObjectType{"BU_": candb.ObjectNode, "BO_": candb.ObjectMessage, "SG_": candb.ObjectSignal, "EV_": candb.ObjectEnvVar}

func (p *parser) attrDef(t token) {
	def := candb.AttributeDefinition{Object: candb.ObjectNetwork}
	if p.peek().kind == tIdent {
		o := p.ident()
		obj, ok := objOf[o]
		if !ok {
			p.fail(t, "unknown attribute object %q", o)
		}
		def.Object = obj
	}
	def.Name = p.str()
	typ := p.ident()
	switch typ {
	case "INT", "HEX", "FLOAT":
		def.Type = map[string]candb.AttributeType{"INT": candb.AttrInt, "HEX": candb.AttrHex, "FLOAT": candb.AttrFloat}[typ]
		if p.peek().kind != tPunct || p.peek().text != ";" {
			def.Min = p.number()
			def.Max = p.number()
		}
	case "STRING":
		def.Type = candb.AttrString
	case "ENUM":
		def.Type = candb.AttrEnum
		for p.peek().kind == tString {
			def.Enum = append(def.Enum, p.str())
			if !p.acceptPunct(",") {
				break
			}
		}
	default:
		p.fail(t, "unknown attribute type %q", typ)
	}
	p.expectPunct(";")
	if def.Object == candb.ObjectEnvVar {
		p.diag(candb.Warning, t.line, "", "attribute %q of environment variables ignored", def.Name)
		return
	}
	p.db.AttributeDefinitions = append(p.db.AttributeDefinitions, def)
}

func (p *parser) attrValue(t token) {
	pa := pendingAttr{obj: candb.ObjectNetwork, line: t.line}
	pa.attr.Name = p.str()
	if p.peek().kind == tIdent {
		o := p.ident()
		switch o {
		case "BU_":
			pa.obj, pa.node = candb.ObjectNode, p.ident()
		case "BO_":
			pa.obj, pa.msg = candb.ObjectMessage, msgKey(p.uinteger())
		case "SG_":
			pa.obj, pa.msg = candb.ObjectSignal, msgKey(p.uinteger())
			pa.sig = p.ident()
		case "EV_":
			p.skipStatement()
			p.diag(candb.Warning, t.line, "", "attribute values of environment variables ignored")
			return
		default:
			p.fail(t, "unknown attribute object %q", o)
		}
	}
	neg := p.acceptPunct("-")
	v := p.next()
	if v.kind != tNumber && v.kind != tString {
		p.fail(v, "expected an attribute value")
	}
	if neg {
		if v.kind != tNumber {
			p.fail(v, "expected a number after '-'")
		}
		v.text = "-" + v.text
	}
	pa.raw = v
	p.expectPunct(";")
	p.attrs = append(p.attrs, pa)
}

func (p *parser) values(t token) {
	if p.peek().kind == tIdent {
		name := p.ident()
		p.valueDescriptions()
		p.expectPunct(";")
		p.diag(candb.Warning, t.line, "", "value descriptions of environment variable %s ignored", name)
		return
	}
	id := msgKey(p.uinteger())
	sig := p.ident()
	vs := p.valueDescriptions()
	p.expectPunct(";")
	if m := p.msg(id, t); m != nil {
		if s := m.Signal(sig); s != nil {
			s.Values = vs
		} else {
			p.diag(candb.Warning, t.line, "", "value descriptions for unknown signal %s.%s ignored", m.Name, sig)
		}
	}
}

func sortDiagnostics(ds []candb.Diagnostic) {
	// Stable insertion sort by line; the lists are short.
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j].Line < ds[j-1].Line; j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
}
