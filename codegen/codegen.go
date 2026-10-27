// SPDX-License-Identifier: MIT

// Package codegen generates Go code from a candb.Database: one struct per
// message with typed raw fields, identifier and length constants, enum
// types for value descriptions, physical-value accessors, and
// multiplexer-aware Encode, Decode and Frame methods.
//
// The output is deterministic (no timestamps, the database's order),
// gofmt-formatted and carries its provenance: the source name, the
// SHA-256 of the source and the generator version. It imports only the
// standard library, github.com/otfabric/go-canbus and its small
// candb/bits runtime.
package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/candb/bits"
)

// Version identifies the generator in the provenance header. It changes
// when the generated code changes.
const Version = "1"

// Options control generation.
type Options struct {
	// Package is the Go package name (required).
	Package string
	// Source names the database file in the provenance header.
	Source string
	// SourceSHA256 is the hex SHA-256 of the database file, if known.
	SourceSHA256 string
}

// Generate returns the Go source for db. The database must validate
// without errors.
func Generate(db *candb.Database, opts Options) ([]byte, error) {
	if !token(opts.Package) {
		return nil, fmt.Errorf("codegen: invalid package name %q", opts.Package)
	}
	for _, d := range db.Validate() {
		if d.Severity == candb.Error {
			return nil, fmt.Errorf("codegen: database has errors: %s", d)
		}
	}
	g := &gen{db: db, opts: opts, used: map[string]bool{}}
	g.file()
	out, err := format.Source(g.buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("codegen: generated code does not compile (generator bug): %v\n%s", err, g.buf.String())
	}
	return out, nil
}

// GenerateJSON returns the database as an indented JSON descriptor for
// dynamic consumers (the candb model's JSON form, with provenance).
func GenerateJSON(db *candb.Database, opts Options) ([]byte, error) {
	doc := struct {
		Generator    string          `json:"generator"`
		Source       string          `json:"source,omitempty"`
		SourceSHA256 string          `json:"sourceSha256,omitempty"`
		Database     *candb.Database `json:"database"`
	}{"go-canbus codegen " + Version, opts.Source, opts.SourceSHA256, db}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func token(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) || r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// ident turns a database name into an exported Go identifier.
func ident(name string) string {
	var b strings.Builder
	up := true
	for _, r := range name {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if up {
				r = unicode.ToUpper(r)
				up = false
			}
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		default:
			up = true
		}
	}
	s := b.String()
	if s == "" || s == "_" {
		s = "X"
	}
	if s[0] >= '0' && s[0] <= '9' || s[0] == '_' {
		s = "X" + s
	}
	return s
}

type gen struct {
	db   *candb.Database
	opts Options
	buf  bytes.Buffer
	used map[string]bool // package-level identifiers
}

func (g *gen) p(format string, args ...any) { fmt.Fprintf(&g.buf, format, args...) }

// unique returns name, or name with a numeric suffix, not yet used at
// package level.
func (g *gen) unique(name string) string {
	n := name
	for i := 2; g.used[n]; i++ {
		n = name + strconv.Itoa(i)
	}
	g.used[n] = true
	return n
}

// msgInfo holds the generated names of a message.
type msgInfo struct {
	m      *candb.Message
	typ    string
	fields map[*candb.Signal]*fieldInfo
	order  []*candb.Signal // switches before what they select
}

type fieldInfo struct {
	s     *candb.Signal
	name  string
	gotyp string // the field's Go type
	base  string // the underlying raw type
	enum  string // enum type name, or ""
	pos   int    // bit position for the bits package
}

func comment(s string) string {
	s = strings.TrimSpace(s)
	return strings.ReplaceAll(s, "\n", "\n// ")
}

func rawType(s *candb.Signal) string {
	switch s.ValueType {
	case candb.Float32:
		return "float32"
	case candb.Float64:
		return "float64"
	default:
	}
	if s.Length == 1 && !s.Signed && len(s.Values) == 0 {
		return "bool"
	}
	size := 8
	for size < s.Length {
		size *= 2
	}
	if s.Signed {
		return "int" + strconv.Itoa(size)
	}
	return "uint" + strconv.Itoa(size)
}

// reserved are the method names of a generated message type.
var reserved = map[string]bool{"Encode": true, "Decode": true, "Frame": true, "MessageID": true, "MessageName": true, "String": true}

func (g *gen) plan() []*msgInfo {
	var out []*msgInfo
	for _, m := range g.db.Messages {
		mi := &msgInfo{m: m, typ: g.unique(ident(m.Name)), fields: map[*candb.Signal]*fieldInfo{}}
		local := map[string]bool{}
		for _, s := range m.Signals {
			name := ident(s.Name)
			for reserved[name] || local[name] || local[name+"Phys"] || strings.HasSuffix(name, "Phys") && local[strings.TrimSuffix(name, "Phys")] {
				name += "_"
			}
			local[name] = true
			pos := s.StartBit
			if s.ByteOrder == candb.BigEndian {
				pos = bits.MotorolaStart(s.StartBit)
			}
			fi := &fieldInfo{s: s, name: name, base: rawType(s), pos: pos}
			fi.gotyp = fi.base
			mi.fields[s] = fi
		}
		mi.order = muxOrder(m)
		out = append(out, mi)
	}
	// Enum types after all message types are named, so message names win.
	for _, mi := range out {
		for _, s := range mi.m.Signals {
			fi := mi.fields[s]
			if len(s.Values) > 0 && s.ValueType == candb.Integer {
				fi.enum = g.unique(mi.typ + fi.name)
				fi.gotyp = fi.enum
			}
		}
	}
	return out
}

// muxOrder orders signals so that every switch comes before the signals
// it selects (the database is validated: no cycles).
func muxOrder(m *candb.Message) []*candb.Signal {
	done := map[string]bool{}
	var out []*candb.Signal
	var visit func(s *candb.Signal)
	visit = func(s *candb.Signal) {
		if done[s.Name] {
			return
		}
		done[s.Name] = true
		if s.Multiplexer != "" {
			if sw := m.Signal(s.Multiplexer); sw != nil {
				visit(sw)
			}
		}
		out = append(out, s)
	}
	for _, s := range m.Signals {
		visit(s)
	}
	return out
}

func lit(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEn") { // keep float constants float-typed
		s += ".0"
	}
	return s
}

// cond returns the Go condition under which s is present, or "".
func (mi *msgInfo) cond(s *candb.Signal) string {
	if s.Multiplexer == "" {
		return ""
	}
	sw := mi.m.Signal(s.Multiplexer)
	fi := mi.fields[sw]
	v := "uint64(m." + fi.name + ")"
	if sw.Signed {
		v = "bits.Truncate(uint64(m." + fi.name + "), " + strconv.Itoa(sw.Length) + ")"
	}
	var parts []string
	for _, r := range s.MuxValues {
		if r.Low == r.High {
			parts = append(parts, fmt.Sprintf("%s == %d", v, r.Low))
		} else {
			parts = append(parts, fmt.Sprintf("(%s >= %d && %s <= %d)", v, r.Low, v, r.High))
		}
	}
	c := "(" + strings.Join(parts, " || ") + ")"
	if outer := mi.cond(sw); outer != "" {
		c = outer + " && " + c
	}
	return c
}

func (g *gen) file() {
	src := g.opts.Source
	if src == "" {
		src = "a CAN database"
	}
	g.p("// Code generated by go-canbus codegen %s from %s", Version, src)
	if g.opts.SourceSHA256 != "" {
		g.p(" (sha256 %s)", g.opts.SourceSHA256)
	}
	g.p(". DO NOT EDIT.\n\n")
	g.p("// Package %s encodes and decodes the messages of %s.\n", g.opts.Package, src)
	if g.db.Version != "" {
		g.p("//\n// Database version: %s\n", strings.ReplaceAll(g.db.Version, "\n", " "))
	}
	g.p("package %s\n\n", g.opts.Package)
	msgs := g.plan()
	g.p("import (\n\"errors\"\n\"fmt\"\n\"math\"\n\n\"github.com/otfabric/go-canbus\"\n\"github.com/otfabric/go-canbus/candb/bits\"\n)\n\n")
	g.p(`// ErrRange reports a value that does not fit its signal.
var ErrRange = errors.New(%q)

// ErrLength reports a payload shorter than its message.
var ErrLength = errors.New(%q)

// ErrUnknownMessage reports a frame of no message of this database.
var ErrUnknownMessage = errors.New(%q)

// Message is implemented by every message type of this package.
type Message interface {
	MessageID() canbus.ID
	MessageName() string
	Encode() ([]byte, error)
	Decode(data []byte) error
	Frame() (canbus.Frame, error)
}

var (
	_ = math.Float32bits
	_ = bits.Truncate
	_ = fmt.Errorf
)

`, g.opts.Package+": value out of range", g.opts.Package+": payload too short", g.opts.Package+": unknown message")
	for _, mi := range msgs {
		g.message(mi)
	}
	g.dispatch(msgs)
}

func (g *gen) message(mi *msgInfo) {
	m := mi.m
	t := mi.typ
	idText := fmt.Sprintf("0x%X", uint32(m.ID))
	g.p("// %sID is the identifier of %s (%s).\nconst %sID canbus.ID = %s\n\n", t, m.Name, map[bool]string{true: "29-bit", false: "11-bit"}[m.Extended], t, idText)
	g.p("// %sLength is the payload length of %s in bytes.\nconst %sLength = %d\n\n", t, m.Name, t, m.Length)
	g.used[t+"ID"], g.used[t+"Length"] = true, true
	// Enums.
	for _, s := range m.Signals {
		fi := mi.fields[s]
		if fi.enum == "" {
			continue
		}
		g.p("// %s are the described values of %s.%s.\ntype %s %s\n\n", fi.enum, m.Name, s.Name, fi.enum, fi.base)
		g.p("const (\n")
		names := map[string]bool{}
		for _, v := range s.Values {
			n := fi.enum + ident(v.Description)
			for i := 2; names[n] || g.used[n]; i++ {
				n = fi.enum + ident(v.Description) + strconv.Itoa(i)
			}
			names[n] = true
			g.used[n] = true
			if s.Signed || v.Value >= 0 {
				g.p("%s %s = %d // %s\n", n, fi.enum, v.Value, strconv.Quote(v.Description))
			}
		}
		g.p(")\n\n")
		g.p("// String returns the description of the value, or its number.\nfunc (v %s) String() string {\nswitch v {\n", fi.enum)
		seen := map[int64]bool{}
		for _, v := range s.Values {
			if seen[v.Value] || !s.Signed && v.Value < 0 {
				continue
			}
			seen[v.Value] = true
			g.p("case %d:\nreturn %s\n", v.Value, strconv.Quote(v.Description))
		}
		g.p("}\nreturn fmt.Sprint(%s(v))\n}\n\n", fi.base)
	}
	// Struct.
	g.p("// %s is the message %s", t, m.Name)
	if m.Comment != "" {
		g.p(": %s", comment(m.Comment))
	}
	g.p(".\n//\n// Identifier %s (%s), %d bytes", idText, map[bool]string{true: "extended", false: "standard"}[m.Extended], m.Length)
	if m.FD {
		g.p(", CAN FD")
		if m.BRS {
			g.p(" with bit rate switching")
		}
	}
	if len(m.Senders) > 0 {
		g.p(", sent by %s", strings.Join(m.Senders, ", "))
	}
	if m.CycleTime > 0 {
		g.p(", every %d ms", m.CycleTime)
	}
	g.p(".\n//\n// Fields hold raw values; the Phys methods scale them.\ntype %s struct {\n", t)
	for _, s := range m.Signals {
		fi := mi.fields[s]
		g.p("// %s: %d bit %s %s", s.Name, s.Length, s.ByteOrder, map[bool]string{true: "signed", false: "unsigned"}[s.Signed])
		if s.ValueType != candb.Integer {
			g.p(" %s", s.ValueType)
		}
		g.p(" at start bit %d; physical = raw × %s + %s", s.StartBit, lit(s.Factor), lit(s.Offset))
		if s.Unit != "" {
			g.p(" [%s]", s.Unit)
		}
		if s.Minimum != 0 || s.Maximum != 0 {
			g.p(", range %s..%s", lit(s.Minimum), lit(s.Maximum))
		}
		if s.IsMultiplexer {
			g.p("; multiplexer")
		}
		if s.Multiplexer != "" {
			g.p("; present when %s", muxText(s))
		}
		g.p(".")
		if s.Comment != "" {
			g.p("\n// %s", comment(s.Comment))
		}
		g.p("\n%s %s\n", fi.name, fi.gotyp)
	}
	g.p("}\n\n")
	g.p("// MessageID returns %sID.\nfunc (*%s) MessageID() canbus.ID { return %sID }\n\n", t, t, t)
	g.p("// MessageName returns %q.\nfunc (*%s) MessageName() string { return %q }\n\n", m.Name, t, m.Name)
	// Phys accessors.
	for _, s := range m.Signals {
		g.phys(mi, s)
	}
	g.encode(mi)
	g.decode(mi)
	// Frame.
	g.p("// Frame encodes the message into a frame.\nfunc (m *%s) Frame() (canbus.Frame, error) {\ndata, err := m.Encode()\nif err != nil {\nreturn canbus.Frame{}, err\n}\n", t)
	switch {
	case m.FD:
		g.p("return canbus.NewFD(%sID, %v, %v, data)\n}\n\n", t, m.Extended, m.BRS)
	case m.Extended:
		g.p("return canbus.NewExtended(%sID, data)\n}\n\n", t)
	default:
		g.p("return canbus.NewStandard(%sID, data)\n}\n\n", t)
	}
}

func muxText(s *candb.Signal) string {
	var parts []string
	for _, r := range s.MuxValues {
		if r.Low == r.High {
			parts = append(parts, strconv.FormatUint(r.Low, 10))
		} else {
			parts = append(parts, fmt.Sprintf("%d..%d", r.Low, r.High))
		}
	}
	return s.Multiplexer + " is " + strings.Join(parts, ", ")
}

// rawExpr converts the field to its uint64 bit pattern, for encoding.
func rawExpr(fi *fieldInfo) string {
	v := "m." + fi.name
	switch fi.base {
	case "bool":
		return "b2u(" + v + ")"
	case "float32":
		return "uint64(math.Float32bits(" + v + "))"
	case "float64":
		return "math.Float64bits(" + v + ")"
	}
	if fi.s.Signed {
		return fmt.Sprintf("bits.Truncate(uint64(%s), %d)", v, fi.s.Length)
	}
	return "uint64(" + v + ")"
}

// fromRaw converts a bit pattern expression to the field's type.
func fromRaw(fi *fieldInfo, raw string) string {
	switch fi.base {
	case "bool":
		return raw + " != 0"
	case "float32":
		return "math.Float32frombits(uint32(" + raw + "))"
	case "float64":
		return "math.Float64frombits(" + raw + ")"
	}
	if fi.s.Signed {
		return fmt.Sprintf("%s(bits.SignExtend(%s, %d))", fi.gotyp, raw, fi.s.Length)
	}
	return fi.gotyp + "(" + raw + ")"
}

func (g *gen) phys(mi *msgInfo, s *candb.Signal) {
	fi := mi.fields[s]
	t := mi.typ
	num := "float64(m." + fi.name + ")"
	if fi.base == "bool" {
		num = "float64(b2u(m." + fi.name + "))"
	}
	g.p("// %sPhys returns the physical value of %s", fi.name, s.Name)
	if s.Unit != "" {
		g.p(" in %s", s.Unit)
	}
	g.p(".\nfunc (m *%s) %sPhys() float64 { return %s*%s + %s }\n\n", t, fi.name, num, lit(s.Factor), lit(s.Offset))
	factor := s.Factor
	if factor == 0 {
		factor = 1
	}
	g.p("// Set%sPhys sets %s from a physical value: (p − offset) / factor", fi.name, s.Name)
	if s.ValueType == candb.Integer {
		g.p(", rounded half to even")
	}
	g.p(". It fails with ErrRange when the value is outside the signal's range or does not fit its %d bits.\n", s.Length)
	g.p("func (m *%s) Set%sPhys(p float64) error {\n", t, fi.name)
	if s.Minimum != 0 || s.Maximum != 0 {
		g.p("if p < %s || p > %s {\nreturn fmt.Errorf(\"%%w: %s.%s: %%v\", ErrRange, p)\n}\n", lit(s.Minimum), lit(s.Maximum), mi.m.Name, s.Name)
	}
	g.p("x := (p - %s) / %s\n", lit(s.Offset), lit(factor))
	switch fi.base {
	case "float32":
		g.p("m.%s = float32(x)\nreturn nil\n}\n\n", fi.name)
		return
	case "float64":
		g.p("m.%s = x\nreturn nil\n}\n\n", fi.name)
		return
	}
	g.p("r := math.RoundToEven(x)\n")
	if s.Signed {
		g.p("if r < %s || r > %s {\nreturn fmt.Errorf(\"%%w: %s.%s: %%v\", ErrRange, p)\n}\n",
			lit(-math2(s.Length-1)), lit(math2(s.Length-1)-1), mi.m.Name, s.Name)
		g.p("m.%s = %s(r)\nreturn nil\n}\n\n", fi.name, fi.gotyp)
		return
	}
	g.p("if r < 0 || r > %s {\nreturn fmt.Errorf(\"%%w: %s.%s: %%v\", ErrRange, p)\n}\n", lit(math2(s.Length)-1), mi.m.Name, s.Name)
	if fi.base == "bool" {
		g.p("m.%s = r != 0\nreturn nil\n}\n\n", fi.name)
		return
	}
	if s.Length == 64 {
		// 2^64−1 is not exactly representable: r ≤ 2^64 here, convert safely.
		g.p("if r >= 18446744073709551616.0 {\nreturn fmt.Errorf(\"%%w: %s.%s: %%v\", ErrRange, p)\n}\n", mi.m.Name, s.Name)
	}
	g.p("m.%s = %s(r)\nreturn nil\n}\n\n", fi.name, fi.gotyp)
}

// math2 returns 2^n as float64.
func math2(n int) float64 {
	f := 1.0
	for i := 0; i < n; i++ {
		f *= 2
	}
	return f
}

func (g *gen) encode(mi *msgInfo) {
	m := mi.m
	n := m.Length
	if m.FD {
		n = roundFD(n)
	}
	g.p("// Encode returns the payload (%d bytes). Signals the multiplexers do not select are not written; their bits stay 0.\n", n)
	g.p("func (m *%s) Encode() ([]byte, error) {\ndata := make([]byte, %d)\n", mi.typ, n)
	for _, s := range mi.order {
		fi := mi.fields[s]
		c := mi.cond(s)
		if c != "" {
			g.p("if %s {\n", c)
		}
		size := 8
		for size < s.Length {
			size *= 2
		}
		if fi.base != "bool" && s.ValueType == candb.Integer && size != s.Length {
			if s.Signed {
				g.p("if !bits.FitsSigned(int64(m.%s), %d) {\nreturn nil, fmt.Errorf(\"%%w: %s.%s: %%d\", ErrRange, m.%s)\n}\n", fi.name, s.Length, m.Name, s.Name, fi.name)
			} else {
				g.p("if !bits.FitsUnsigned(uint64(m.%s), %d) {\nreturn nil, fmt.Errorf(\"%%w: %s.%s: %%d\", ErrRange, m.%s)\n}\n", fi.name, s.Length, m.Name, s.Name, fi.name)
			}
		}
		fn := "bits.InsertLE"
		if s.ByteOrder == candb.BigEndian {
			fn = "bits.InsertBE"
		}
		g.p("%s(data, %d, %d, %s)\n", fn, fi.pos, s.Length, rawExpr(fi))
		if c != "" {
			g.p("}\n")
		}
	}
	g.p("return data, nil\n}\n\n")
}

func (g *gen) decode(mi *msgInfo) {
	m := mi.m
	g.p("// Decode sets the fields from a payload of at least %d bytes. Signals the multiplexers do not select are set to zero.\n", m.Length)
	g.p("func (m *%s) Decode(data []byte) error {\nif len(data) < %d {\nreturn fmt.Errorf(\"%%w: %s needs %d bytes, got %%d\", ErrLength, len(data))\n}\n", mi.typ, m.Length, m.Name, m.Length)
	g.p("*m = %s{}\n", mi.typ)
	for _, s := range mi.order {
		fi := mi.fields[s]
		c := mi.cond(s)
		if c != "" {
			g.p("if %s {\n", c)
		}
		fn := "bits.ExtractLE"
		if s.ByteOrder == candb.BigEndian {
			fn = "bits.ExtractBE"
		}
		g.p("m.%s = %s\n", fi.name, fromRaw(fi, fmt.Sprintf("%s(data, %d, %d)", fn, fi.pos, s.Length)))
		if c != "" {
			g.p("}\n")
		}
	}
	g.p("return nil\n}\n\n")
}

func roundFD(n int) int {
	for _, l := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 12, 16, 20, 24, 32, 48, 64} {
		if l >= n {
			return l
		}
	}
	return 64
}

func (g *gen) dispatch(msgs []*msgInfo) {
	g.p("func b2u(b bool) uint64 {\nif b {\nreturn 1\n}\nreturn 0\n}\n\n")
	sorted := append([]*msgInfo(nil), msgs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].m, sorted[j].m
		if a.Extended != b.Extended {
			return !a.Extended
		}
		return a.ID < b.ID
	})
	g.p("// New returns an empty message of the given identifier, or nil.\nfunc New(id canbus.ID, extended bool) Message {\nswitch {\n")
	for _, mi := range sorted {
		g.p("case id == %sID && extended == %v:\nreturn &%s{}\n", mi.typ, mi.m.Extended, mi.typ)
	}
	g.p("}\nreturn nil\n}\n\n")
	g.p("// Decode decodes a frame into the message of its identifier.\nfunc Decode(f *canbus.Frame) (Message, error) {\nm := New(f.ID, f.IsExtended())\nif m == nil || f.IsRemote() || f.IsError() {\nreturn nil, fmt.Errorf(\"%%w: %%s\", ErrUnknownMessage, f.String())\n}\nif err := m.Decode(f.Payload()); err != nil {\nreturn nil, err\n}\nreturn m, nil\n}\n")
}
