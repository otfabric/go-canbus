// SPDX-License-Identifier: MIT

package dbc

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/otfabric/go-canbus/candb"
)

// newSymbols is the NS_ list Vector tools write.
var newSymbols = []string{"NS_DESC_", "CM_", "BA_DEF_", "BA_", "VAL_", "CAT_DEF_", "CAT_", "FILTER", "BA_DEF_DEF_",
	"EV_DATA_", "ENVVAR_DATA_", "SGTYPE_", "SGTYPE_VAL_", "BA_DEF_SGTYPE_", "BA_SGTYPE_", "SIG_TYPE_REF_",
	"VAL_TABLE_", "SIG_GROUP_", "SIG_VALTYPE_", "SIGTYPE_VALTYPE_", "BO_TX_BU_", "BA_DEF_REL_", "BA_REL_",
	"BA_DEF_DEF_REL_", "BU_SG_REL_", "BU_EV_REL_", "BU_BO_REL_", "SG_MUL_VAL_"}

// Write writes db as a DBC file. The model's cycle times, initial values
// and CAN FD flags are written as the attributes GenMsgCycleTime,
// GenSigStartValue, VFrameFormat and CANFD_BRS when the database does not
// carry them already. Multiplexing is written with mN indicators and, when
// it is not simple (more than one switch or ranges), SG_MUL_VAL_.
func Write(w io.Writer, db *candb.Database) error {
	bw := bufio.NewWriter(w)
	wr := &writer{w: bw, db: augment(db)}
	wr.all()
	if wr.err != nil {
		return wr.err
	}
	return bw.Flush()
}

type writer struct {
	w   *bufio.Writer
	db  *candb.Database
	err error
}

func (w *writer) p(format string, args ...any) {
	if w.err == nil {
		_, w.err = fmt.Fprintf(w.w, format, args...)
	}
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func dbcID(m *candb.Message) uint32 {
	if m.Extended {
		return uint32(m.ID) | 0x80000000
	}
	return uint32(m.ID)
}

func or(list []string, def string) string {
	if len(list) == 0 {
		return def
	}
	return strings.Join(list, ",")
}

// simpleMux reports whether the message's multiplexing can be written
// with mN indicators alone.
func simpleMux(m *candb.Message) bool {
	switches := 0
	for _, s := range m.Signals {
		if s.IsMultiplexer {
			switches++
		}
		if s.Multiplexer != "" && (len(s.MuxValues) != 1 || s.MuxValues[0].Low != s.MuxValues[0].High) {
			return false
		}
	}
	return switches <= 1
}

func (w *writer) all() {
	db := w.db
	w.p("VERSION %s\n\n\nNS_ :\n", quote(db.Version))
	for _, s := range newSymbols {
		w.p("\t%s\n", s)
	}
	w.p("\nBS_:\n\nBU_:")
	for _, n := range db.Nodes {
		w.p(" %s", n.Name)
	}
	w.p("\n")
	for _, vt := range db.ValueTables {
		w.p("VAL_TABLE_ %s", vt.Name)
		w.values(vt.Values)
		w.p(" ;\n")
	}
	w.p("\n")
	for _, m := range db.Messages {
		sender := "Vector__XXX"
		if len(m.Senders) > 0 {
			sender = m.Senders[0]
		}
		w.p("\nBO_ %d %s: %d %s\n", dbcID(m), m.Name, m.Length, sender)
		for _, s := range m.Signals {
			ind := ""
			switch {
			case s.Multiplexer != "" && s.IsMultiplexer:
				ind = fmt.Sprintf(" m%dM", firstMux(s))
			case s.Multiplexer != "":
				ind = fmt.Sprintf(" m%d", firstMux(s))
			case s.IsMultiplexer:
				ind = " M"
			}
			order, sign := 1, "+"
			if s.ByteOrder == candb.BigEndian {
				order = 0
			}
			if s.Signed {
				sign = "-"
			}
			w.p(" SG_ %s%s : %d|%d@%d%s (%s,%s) [%s|%s] %s %s\n", s.Name, ind, s.StartBit, s.Length, order, sign,
				num(s.Factor), num(s.Offset), num(s.Minimum), num(s.Maximum), quote(s.Unit), or(s.Receivers, "Vector__XXX"))
		}
	}
	w.p("\n")
	for _, m := range db.Messages {
		if len(m.Senders) > 1 {
			w.p("BO_TX_BU_ %d : %s;\n", dbcID(m), strings.Join(m.Senders, ","))
		}
	}
	w.p("\n")
	if db.Comment != "" {
		w.p("CM_ %s;\n", quote(db.Comment))
	}
	for _, n := range db.Nodes {
		if n.Comment != "" {
			w.p("CM_ BU_ %s %s;\n", n.Name, quote(n.Comment))
		}
	}
	for _, m := range db.Messages {
		if m.Comment != "" {
			w.p("CM_ BO_ %d %s;\n", dbcID(m), quote(m.Comment))
		}
		for _, s := range m.Signals {
			if s.Comment != "" {
				w.p("CM_ SG_ %d %s %s;\n", dbcID(m), s.Name, quote(s.Comment))
			}
		}
	}
	objKw := map[candb.ObjectType]string{candb.ObjectNetwork: "", candb.ObjectNode: "BU_ ", candb.ObjectMessage: "BO_ ", candb.ObjectSignal: "SG_ ", candb.ObjectEnvVar: "EV_ "}
	for _, d := range db.AttributeDefinitions {
		w.p("BA_DEF_ %s%s ", objKw[d.Object], quote(d.Name))
		switch d.Type {
		case candb.AttrInt:
			w.p("INT %s %s;\n", num(d.Min), num(d.Max))
		case candb.AttrHex:
			w.p("HEX %s %s;\n", num(d.Min), num(d.Max))
		case candb.AttrFloat:
			w.p("FLOAT %s %s;\n", num(d.Min), num(d.Max))
		case candb.AttrString:
			w.p("STRING ;\n")
		case candb.AttrEnum:
			q := make([]string, len(d.Enum))
			for i, e := range d.Enum {
				q[i] = quote(e)
			}
			w.p("ENUM %s;\n", strings.Join(q, ","))
		}
	}
	for _, d := range db.AttributeDefinitions {
		if d.Default != nil {
			w.p("BA_DEF_DEF_ %s %s;\n", quote(d.Name), w.attrText(&d, *d.Default))
		}
	}
	for _, a := range db.Attributes {
		w.p("BA_ %s %s;\n", quote(a.Name), w.attrText(db.AttributeDefinition(a.Name, candb.ObjectNetwork), a))
	}
	for _, n := range db.Nodes {
		for _, a := range n.Attributes {
			w.p("BA_ %s BU_ %s %s;\n", quote(a.Name), n.Name, w.attrText(db.AttributeDefinition(a.Name, candb.ObjectNode), a))
		}
	}
	for _, m := range db.Messages {
		for _, a := range m.Attributes {
			w.p("BA_ %s BO_ %d %s;\n", quote(a.Name), dbcID(m), w.attrText(db.AttributeDefinition(a.Name, candb.ObjectMessage), a))
		}
		for _, s := range m.Signals {
			for _, a := range s.Attributes {
				w.p("BA_ %s SG_ %d %s %s;\n", quote(a.Name), dbcID(m), s.Name, w.attrText(db.AttributeDefinition(a.Name, candb.ObjectSignal), a))
			}
		}
	}
	for _, m := range db.Messages {
		for _, s := range m.Signals {
			if len(s.Values) > 0 {
				w.p("VAL_ %d %s", dbcID(m), s.Name)
				w.values(s.Values)
				w.p(" ;\n")
			}
		}
	}
	for _, m := range db.Messages {
		for _, s := range m.Signals {
			switch s.ValueType {
			case candb.Float32:
				w.p("SIG_VALTYPE_ %d %s : 1;\n", dbcID(m), s.Name)
			case candb.Float64:
				w.p("SIG_VALTYPE_ %d %s : 2;\n", dbcID(m), s.Name)
			default:
			}
		}
	}
	for _, m := range db.Messages {
		for _, g := range m.SignalGroups {
			w.p("SIG_GROUP_ %d %s %d : %s;\n", dbcID(m), g.Name, g.Repetitions, strings.Join(g.Signals, " "))
		}
	}
	for _, m := range db.Messages {
		if simpleMux(m) {
			continue
		}
		for _, s := range m.Signals {
			if s.Multiplexer == "" {
				continue
			}
			rs := make([]string, len(s.MuxValues))
			for i, r := range s.MuxValues {
				rs[i] = fmt.Sprintf("%d-%d", r.Low, r.High)
			}
			w.p("SG_MUL_VAL_ %d %s %s %s;\n", dbcID(m), s.Name, s.Multiplexer, strings.Join(rs, ", "))
		}
	}
}

func firstMux(s *candb.Signal) uint64 {
	if len(s.MuxValues) == 0 {
		return 0
	}
	return s.MuxValues[0].Low
}

func (w *writer) values(vs []candb.ValueDescription) {
	for _, v := range vs {
		w.p(" %d %s", v.Value, quote(v.Description))
	}
}

func (w *writer) attrText(d *candb.AttributeDefinition, a candb.Attribute) string {
	if d == nil {
		return quote(a.String)
	}
	switch d.Type {
	case candb.AttrString:
		return quote(a.String)
	case candb.AttrFloat:
		return num(a.Float)
	default:
	}
	return strconv.FormatInt(a.Int, 10)
}

// augment returns a copy of db whose attributes also carry the model's
// mapped fields (cycle time, initial value, CAN FD), so a writer that
// only writes attributes loses nothing.
func augment(src *candb.Database) *candb.Database {
	db := *src
	db.AttributeDefinitions = append([]candb.AttributeDefinition(nil), src.AttributeDefinitions...)
	db.Messages = make([]*candb.Message, len(src.Messages))
	// implied reports that the attribute's default already yields the
	// value, so no explicit attribute is needed.
	implied := func(name string, obj candb.ObjectType, ok func(d *candb.AttributeDefinition) bool) bool {
		d := db.AttributeDefinition(name, obj)
		return d != nil && d.Default != nil && ok(d)
	}
	label := func(d *candb.AttributeDefinition) string {
		if int(d.Default.Int) < len(d.Enum) && d.Default.Int >= 0 {
			return d.Enum[d.Default.Int]
		}
		return ""
	}
	need := func(name string, obj candb.ObjectType, def candb.AttributeDefinition) {
		if db.AttributeDefinition(name, obj) == nil {
			def.Name, def.Object = name, obj
			db.AttributeDefinitions = append(db.AttributeDefinitions, def)
		}
	}
	has := func(as []candb.Attribute, name string) bool {
		for _, a := range as {
			if a.Name == name {
				return true
			}
		}
		return false
	}
	for i, sm := range src.Messages {
		m := *sm
		m.Attributes = append([]candb.Attribute(nil), sm.Attributes...)
		m.Signals = make([]*candb.Signal, len(sm.Signals))
		if m.CycleTime != 0 && !has(m.Attributes, "GenMsgCycleTime") &&
			!implied("GenMsgCycleTime", candb.ObjectMessage, func(d *candb.AttributeDefinition) bool { return d.Default.Int == int64(m.CycleTime) }) {
			need("GenMsgCycleTime", candb.ObjectMessage, candb.AttributeDefinition{Type: candb.AttrInt, Min: 0, Max: 65535})
			m.Attributes = append(m.Attributes, candb.Attribute{Name: "GenMsgCycleTime", Int: int64(m.CycleTime)})
		}
		if m.FD && m.Length <= 8 && !has(m.Attributes, "VFrameFormat") &&
			!implied("VFrameFormat", candb.ObjectMessage, func(d *candb.AttributeDefinition) bool { return strings.HasSuffix(label(d), "CAN_FD") }) {
			need("VFrameFormat", candb.ObjectMessage, candb.AttributeDefinition{Type: candb.AttrEnum,
				Enum: []string{"StandardCAN", "ExtendedCAN", "reserved", "reserved", "reserved", "reserved", "reserved", "reserved",
					"reserved", "reserved", "reserved", "reserved", "reserved", "reserved", "StandardCAN_FD", "ExtendedCAN_FD"}})
			d := db.AttributeDefinition("VFrameFormat", candb.ObjectMessage)
			want := "StandardCAN_FD"
			if m.Extended {
				want = "ExtendedCAN_FD"
			}
			for j, e := range d.Enum {
				if e == want {
					m.Attributes = append(m.Attributes, candb.Attribute{Name: "VFrameFormat", Int: int64(j)})
					break
				}
			}
		}
		if m.BRS && !has(m.Attributes, "CANFD_BRS") &&
			!implied("CANFD_BRS", candb.ObjectMessage, func(d *candb.AttributeDefinition) bool { return label(d) == "1" }) {
			need("CANFD_BRS", candb.ObjectMessage, candb.AttributeDefinition{Type: candb.AttrEnum, Enum: []string{"0", "1"}})
			m.Attributes = append(m.Attributes, candb.Attribute{Name: "CANFD_BRS", Int: 1})
		}
		for j, ss := range sm.Signals {
			s := *ss
			s.Attributes = append([]candb.Attribute(nil), ss.Attributes...)
			if s.Initial != nil && !has(s.Attributes, "GenSigStartValue") &&
				!implied("GenSigStartValue", candb.ObjectSignal, func(d *candb.AttributeDefinition) bool {
					return d.Default.Int == *s.Initial && d.Type != candb.AttrFloat || d.Type == candb.AttrFloat && d.Default.Float == float64(*s.Initial)
				}) {
				need("GenSigStartValue", candb.ObjectSignal, candb.AttributeDefinition{Type: candb.AttrInt, Min: -9223372036854775808, Max: 9223372036854775807})
				if d := db.AttributeDefinition("GenSigStartValue", candb.ObjectSignal); d.Type == candb.AttrFloat {
					s.Attributes = append(s.Attributes, candb.Attribute{Name: "GenSigStartValue", Float: float64(*s.Initial)})
				} else {
					s.Attributes = append(s.Attributes, candb.Attribute{Name: "GenSigStartValue", Int: *s.Initial})
				}
			}
			m.Signals[j] = &s
		}
		db.Messages[i] = &m
	}
	return &db
}
