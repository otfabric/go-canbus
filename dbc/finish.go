// SPDX-License-Identifier: MIT

package dbc

import (
	"math"
	"strconv"
	"strings"

	"github.com/otfabric/go-canbus/candb"
)

// finish resolves what DBC states apart from the definitions: value
// types, multiplexing, attributes and the attributes mapped onto the
// model; then validates the result.
func (p *parser) finish() {
	for k, vt := range p.valTypes {
		m := p.msgs[k.msg]
		if m == nil || m.Signal(k.sig) == nil {
			p.diag(candb.Warning, 0, "", "SIG_VALTYPE_ for unknown signal %d %s ignored", uint32(k.msg), k.sig)
			continue
		}
		m.Signal(k.sig).ValueType = candb.ValueType(vt)
	}
	for _, m := range p.db.Messages {
		p.resolveMux(m)
	}
	for k := range p.muxVals {
		if m := p.msgs[k.msg]; m == nil || m.Signal(k.sig) == nil {
			p.diag(candb.Warning, p.muxVals[k][0].line, "", "SG_MUL_VAL_ for unknown signal %d %s ignored", uint32(k.msg), k.sig)
		}
	}
	p.applyDefaults()
	for _, pa := range p.attrs {
		p.applyAttr(pa)
	}
	p.applyMappedDefaults()
	for _, m := range p.db.Messages {
		if m.Length > 8 {
			m.FD = true
		}
		if !m.FD {
			m.BRS = false // CANFD_BRS (often a default) means nothing for classical frames
		}
	}
	p.diags = append(p.diags, p.db.Validate()...)
}

func (p *parser) resolveMux(m *candb.Message) {
	var key msgKey
	for k, v := range p.msgs {
		if v == m {
			key = k
		}
	}
	var switches []*candb.Signal
	for _, s := range m.Signals {
		if ind := p.muxInd[sigKey{key, s.Name}]; strings.HasSuffix(ind, "M") {
			s.IsMultiplexer = true
			switches = append(switches, s)
		}
	}
	var root *candb.Signal
	for _, s := range switches {
		if p.muxInd[sigKey{key, s.Name}] == "M" {
			if root != nil {
				root = nil // more than one top-level switch: SG_MUL_VAL_ must say
				break
			}
			root = s
		}
	}
	for _, s := range m.Signals {
		k := sigKey{key, s.Name}
		ind := p.muxInd[k]
		mv := p.muxVals[k]
		if len(mv) > 1 {
			p.diag(candb.Error, mv[1].line, "signal "+m.Name+"."+s.Name, "more than one SG_MUL_VAL_ (a signal has one multiplexer)")
		}
		switch {
		case len(mv) > 0:
			if ind == "" || ind == "M" {
				p.diag(candb.Error, mv[0].line, "signal "+m.Name+"."+s.Name, "SG_MUL_VAL_ for a signal without an mN indicator")
				continue
			}
			s.Multiplexer, s.MuxValues = mv[0].sw, mv[0].ranges
		case strings.HasPrefix(ind, "m"):
			v, _ := strconv.ParseUint(strings.TrimSuffix(ind[1:], "M"), 10, 64)
			if root == nil {
				p.diag(candb.Error, 0, "signal "+m.Name+"."+s.Name, "multiplexed signal %s without SG_MUL_VAL_ and no single multiplexer switch", ind)
				continue
			}
			s.Multiplexer, s.MuxValues = root.Name, []candb.MuxRange{{Low: v, High: v}}
		}
	}
}

func (p *parser) applyDefaults() {
	for name, def := range p.defaults {
		found := false
		for i := range p.db.AttributeDefinitions {
			d := &p.db.AttributeDefinitions[i]
			if d.Name != name {
				continue
			}
			found = true
			a, ok := p.convert(d, token{kind: kindOf(def.String), text: def.String})
			if !ok {
				p.diag(candb.Warning, 0, "", "default %q of attribute %s does not match its type: ignored", def.String, name)
				continue
			}
			d.Default = &a
		}
		if !found {
			p.diag(candb.Warning, 0, "", "default for undefined attribute %s ignored", name)
		}
	}
}

func kindOf(s string) tokKind {
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return tNumber
	}
	return tString
}

// convert types an attribute value by its definition. Enum values may be
// given as index or as label.
func (p *parser) convert(d *candb.AttributeDefinition, v token) (candb.Attribute, bool) {
	a := candb.Attribute{Name: d.Name}
	switch d.Type {
	case candb.AttrString:
		a.String = v.text
		return a, true
	case candb.AttrFloat:
		f, err := strconv.ParseFloat(v.text, 64)
		a.Float = f
		return a, err == nil
	case candb.AttrEnum:
		if v.kind == tString {
			for i, e := range d.Enum {
				if e == v.text {
					a.Int = int64(i)
					return a, true
				}
			}
			if i, err := strconv.ParseInt(v.text, 10, 64); err == nil && i >= 0 && int(i) < len(d.Enum) {
				a.Int = i
				return a, true
			}
			return a, false
		}
		i, err := strconv.ParseInt(v.text, 10, 64)
		a.Int = i
		return a, err == nil && i >= 0 && (len(d.Enum) == 0 || int(i) < len(d.Enum))
	default: // int, hex
		i, err := strconv.ParseInt(v.text, 0, 64)
		if err != nil {
			f, ferr := strconv.ParseFloat(v.text, 64)
			if ferr != nil || f != math.Trunc(f) {
				return a, false
			}
			i = int64(f)
		}
		a.Int = i
		return a, true
	}
}

func (p *parser) applyAttr(pa pendingAttr) {
	d := p.db.AttributeDefinition(pa.attr.Name, pa.obj)
	if d == nil {
		p.diag(candb.Warning, pa.line, "", "value of undefined %s attribute %s ignored", pa.obj, pa.attr.Name)
		return
	}
	a, ok := p.convert(d, pa.raw)
	if !ok {
		p.diag(candb.Error, pa.line, "", "value %q does not match the %s attribute %s", pa.raw.text, d.Type, d.Name)
		return
	}
	switch pa.obj {
	case candb.ObjectNetwork:
		p.db.Attributes = append(p.db.Attributes, a)
	case candb.ObjectNode:
		n := p.db.Node(pa.node)
		if n == nil {
			p.diag(candb.Warning, pa.line, "", "attribute %s of unknown node %s ignored", a.Name, pa.node)
			return
		}
		n.Attributes = append(n.Attributes, a)
	case candb.ObjectMessage:
		m := p.msg(pa.msg, token{text: "BA_", line: pa.line})
		if m == nil {
			return
		}
		m.Attributes = append(m.Attributes, a)
		switch a.Name {
		case "GenMsgCycleTime":
			m.CycleTime = int(a.Int)
		case "VFrameFormat":
			if int(a.Int) < len(d.Enum) {
				if strings.HasSuffix(d.Enum[a.Int], "CAN_FD") {
					m.FD = true
				}
			}
		case "CANFD_BRS":
			label := strconv.FormatInt(a.Int, 10)
			if int(a.Int) < len(d.Enum) {
				label = d.Enum[a.Int]
			}
			m.BRS = label == "1"
		}
	case candb.ObjectSignal:
		m := p.msg(pa.msg, token{text: "BA_", line: pa.line})
		if m == nil {
			return
		}
		s := m.Signal(pa.sig)
		if s == nil {
			p.diag(candb.Warning, pa.line, "", "attribute %s of unknown signal %s.%s ignored", a.Name, m.Name, pa.sig)
			return
		}
		s.Attributes = append(s.Attributes, a)
		if a.Name == "GenSigStartValue" {
			v := a.Int
			if d.Type == candb.AttrFloat {
				v = int64(math.Round(a.Float))
			}
			s.Initial = &v
		}
	default:
	}
}

// applyMappedDefaults applies the defaults of the attributes mapped onto
// the model to objects without an explicit value.
func (p *parser) applyMappedDefaults() {
	has := func(as []candb.Attribute, name string) bool {
		for _, a := range as {
			if a.Name == name {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"GenMsgCycleTime", "VFrameFormat", "CANFD_BRS"} {
		d := p.db.AttributeDefinition(name, candb.ObjectMessage)
		if d == nil || d.Default == nil {
			continue
		}
		for key, m := range p.msgs {
			if !has(m.Attributes, name) {
				p.applyAttr(pendingAttr{obj: candb.ObjectMessage, msg: key, attr: candb.Attribute{Name: name},
					raw: defaultToken(d), line: 0})
				m.Attributes = m.Attributes[:len(m.Attributes)-1] // a default is not an explicit value
			}
		}
	}
	if d := p.db.AttributeDefinition("GenSigStartValue", candb.ObjectSignal); d != nil && d.Default != nil {
		for _, m := range p.db.Messages {
			for _, s := range m.Signals {
				if s.Initial == nil {
					v := d.Default.Int
					if d.Type == candb.AttrFloat {
						v = int64(math.Round(d.Default.Float))
					}
					s.Initial = &v
				}
			}
		}
	}
}

func defaultToken(d *candb.AttributeDefinition) token {
	switch d.Type {
	case candb.AttrString:
		return token{kind: tString, text: d.Default.String}
	case candb.AttrFloat:
		return token{kind: tNumber, text: strconv.FormatFloat(d.Default.Float, 'g', -1, 64)}
	default:
	}
	return token{kind: tNumber, text: strconv.FormatInt(d.Default.Int, 10)}
}
