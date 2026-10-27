// SPDX-License-Identifier: MIT

package dbc

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
)

func load(t *testing.T) *candb.Database {
	t.Helper()
	db, diags, err := ParseFile("testdata/full.dbc")
	if err != nil {
		t.Fatalf("%v\n%v", err, diags)
	}
	return db
}

func TestParseModel(t *testing.T) {
	db, diags, err := ParseFile("testdata/full.dbc")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the expected diagnostics: the pseudo message (info) and the
	// environment variable (warning).
	var got []string
	for _, d := range diags {
		got = append(got, d.String())
	}
	want := []string{"info: message VECTOR__INDEPENDENT_SIG_MSG: pseudo message of unassigned signals ignored",
		"warning: environment variables (EV_) are not supported: ignored"}
	if len(got) != len(want) || !strings.Contains(got[0], want[0]) || !strings.Contains(got[1], want[1]) {
		t.Fatalf("diagnostics: %q", got)
	}
	if db.Version != "1.0 test" || db.Comment != "Network comment" || len(db.Nodes) != 3 || db.Nodes[0].Comment != "Engine control unit" {
		t.Fatalf("db %+v", db)
	}
	if len(db.Messages) != 7 || db.MessageByName("VECTOR__INDEPENDENT_SIG_MSG") != nil {
		t.Fatalf("%d messages", len(db.Messages))
	}
	if len(db.ValueTables) != 1 || db.ValueTables[0].Values[0].Description != "Off" {
		t.Fatal(db.ValueTables)
	}
	eng := db.Message(100, false)
	if eng.Name != "Engine" || eng.Length != 8 || eng.CycleTime != 10 || eng.Comment != "Engine data" || eng.FD ||
		!reflect.DeepEqual(eng.Senders, []string{"ECU1", "Gateway"}) || eng.SignalGroups[0].Signals[1] != "Temp" {
		t.Fatalf("Engine %+v", eng)
	}
	sp := eng.Signal("Speed")
	if sp.Factor != 0.125 || sp.Maximum != 8191.875 || sp.Unit != "rpm" || !reflect.DeepEqual(sp.Receivers, []string{"ECU2", "Gateway"}) ||
		sp.Comment != "Engine speed\non two lines with a \"quote\"" || sp.Attributes[0].Float != 0.5 {
		t.Fatalf("Speed %+v", sp)
	}
	if tmp := eng.Signal("Temp"); !tmp.Signed || tmp.Offset != -40 || *tmp.Initial != 40 {
		t.Fatalf("Temp %+v", tmp)
	}
	if *sp.Initial != 0 { // the default
		t.Fatal("GenSigStartValue default not applied")
	}
	if th := eng.Signal("Throttle"); th.ByteOrder != candb.BigEndian || len(th.Receivers) != 0 {
		t.Fatalf("Throttle %+v", th)
	}
	if eng.Signal("Mode").Label(7) != "Fault" {
		t.Fatal("value descriptions")
	}
	wide := db.Message(0x18FEF1FE, true)
	if wide == nil || !wide.Extended {
		t.Fatal("extended identifier")
	}
	fl := db.MessageByName("Floats")
	if fl.Signal("F32").ValueType != candb.Float32 || fl.Signal("F64").ValueType != candb.Float64 || !fl.FD {
		t.Fatalf("Floats %+v", fl)
	}
	fd := db.MessageByName("FdMsg")
	if !fd.FD || !fd.Extended || fd.ID != 0x200 || fd.Length != 64 || !fd.BRS {
		t.Fatalf("FdMsg %+v", fd)
	}
	mux := db.MessageByName("Muxed")
	if !mux.Multiplexed() || !mux.Signal("Sel").IsMultiplexer || mux.Signal("B").Multiplexer != "Sel" || mux.Signal("B").MuxValues[0].Low != 1 {
		t.Fatal("simple mux")
	}
	ext := db.MessageByName("ExtMux")
	in := ext.Signal("Inner")
	if !in.IsMultiplexer || in.Multiplexer != "Outer" || ext.Signal("Deep").Multiplexer != "Inner" ||
		!reflect.DeepEqual(ext.Signal("Deep").MuxValues, []candb.MuxRange{{Low: 2, High: 2}, {Low: 5, High: 6}}) {
		t.Fatalf("extended mux %+v", ext.Signal("Deep"))
	}
	if db.AttributeDefinition("NodeLayer", candb.ObjectNode).Type != candb.AttrHex || db.Node("ECU1").Attributes[0].Int != 16 || db.Attributes[0].String != "CAN FD" {
		t.Fatal("attributes")
	}
}

// TestDecodeKnownBytes checks layouts computed by hand.
func TestDecodeKnownBytes(t *testing.T) {
	db := load(t)
	eng := db.Message(100, false)
	// Speed = 0x1234 LE at 0 (×0.125 = 582.5), Temp = 0xF6 = -10 (-50 °C),
	// Throttle Motorola start 31, 12 bits: byte 3 bits 7..0 then byte 4
	// bits 7..4 → 0xABC; Mode LE bits 32..34 (byte 4 bits 0..2) = 5;
	// Flag Motorola bit 63 = byte 7 bit 7.
	data := []byte{0x34, 0x12, 0xF6, 0xAB, 0xC0 | 5, 0, 0, 0x80}
	sigs, err := eng.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]candb.Value{}
	for _, s := range sigs {
		got[s.Signal.Name] = s.Value
	}
	if got["Speed"].Raw != 0x1234 || got["Speed"].Physical != 582.5 {
		t.Errorf("Speed %+v", got["Speed"])
	}
	if got["Temp"].Signed != -10 || got["Temp"].Physical != -50 {
		t.Errorf("Temp %+v", got["Temp"])
	}
	if got["Throttle"].Raw != 0xABC || math.Abs(got["Throttle"].Physical-274.8) > 1e-9 {
		t.Errorf("Throttle %+v", got["Throttle"])
	}
	if got["Mode"].Raw != 5 || got["Mode"].Label != "" {
		t.Errorf("Mode %+v", got["Mode"])
	}
	if got["Flag"].Raw != 1 {
		t.Errorf("Flag %+v", got["Flag"])
	}
	// Re-encoding the decoded raw values gives the same bytes.
	in := map[string]candb.Input{}
	for _, s := range sigs {
		in[s.Signal.Name] = candb.Raw(s.Raw)
	}
	out, err := eng.Encode(in)
	if err != nil || !bytes.Equal(out, data) {
		t.Fatalf("% x, %v", out, err)
	}
	if _, err := eng.Decode(data[:7]); !errors.Is(err, candb.ErrShortPayload) {
		t.Fatal(err)
	}
}

func TestSixtyFourBitExact(t *testing.T) {
	db := load(t)
	wide := db.MessageByName("Wide")
	data, err := wide.Encode(map[string]candb.Input{"U64": candb.Raw(math.MaxUint64 - 1)})
	if err != nil || hex.EncodeToString(data) != "feffffffffffffff" {
		t.Fatalf("% x %v", data, err)
	}
	sigs, _ := wide.Decode(data)
	if sigs[0].Raw != math.MaxUint64-1 || wide.Signal("U64").RawString(sigs[0].Value) != "18446744073709551614" {
		t.Fatal(sigs[0])
	}
	sbe := db.MessageByName("SignedBE")
	data, err = sbe.Encode(map[string]candb.Input{"S64": candb.RawInt(math.MinInt64 + 1)})
	if err != nil || hex.EncodeToString(data) != "8000000000000001" {
		t.Fatalf("% x %v", data, err)
	}
	sigs, _ = sbe.Decode(data)
	if sigs[0].Signed != math.MinInt64+1 || sbe.Signal("S64").RawString(sigs[0].Value) != "-9223372036854775807" {
		t.Fatal(sigs[0])
	}
}

func TestFloatSignals(t *testing.T) {
	db := load(t)
	fl := db.MessageByName("Floats")
	data, err := fl.Encode(map[string]candb.Input{"F32": candb.Phys(-1.5), "F64": candb.Phys(7)})
	if err != nil || len(data) != 12 {
		t.Fatal(data, err)
	}
	// F32 = -1.5f little-endian; F64 Motorola from byte 4: (7-1)/2 = 3.0.
	if hex.EncodeToString(data) != "0000c0bf4008000000000000" {
		t.Fatalf("% x", data)
	}
	sigs, _ := fl.Decode(data)
	if sigs[0].Physical != -1.5 || sigs[1].Physical != 7 {
		t.Fatal(sigs)
	}
}

func TestMultiplexing(t *testing.T) {
	db := load(t)
	mux := db.MessageByName("Muxed")
	data, err := mux.Encode(map[string]candb.Input{"Sel": candb.Label("PageB"), "B": candb.Phys(-2), "C": candb.Raw(9)})
	if err != nil || hex.EncodeToString(data) != "01fe000000000009" {
		t.Fatalf("% x %v", data, err)
	}
	sigs, _ := mux.Decode(data)
	if len(sigs) != 3 || sigs[1].Signal.Name != "B" || sigs[1].Signed != -2 || sigs[0].Label != "PageB" {
		t.Fatal(sigs)
	}
	if _, err := mux.Encode(map[string]candb.Input{"Sel": candb.Raw(1), "A": candb.Raw(1), "B": candb.Raw(1), "C": candb.Raw(0)}); !errors.Is(err, candb.ErrInactiveSignal) {
		t.Fatal(err)
	}
	if _, err := mux.Encode(map[string]candb.Input{"Sel": candb.Raw(0), "C": candb.Raw(0)}); !errors.Is(err, candb.ErrMissingSignal) {
		t.Fatal(err)
	}
	ext := db.MessageByName("ExtMux")
	cases := []struct {
		in   map[string]candb.Input
		hex  string
		want []string
	}{
		{map[string]candb.Input{"Outer": candb.Raw(1), "Inner": candb.Raw(5), "Deep": candb.Raw(0xAA)}, "51aa000000000000", []string{"Outer", "Inner", "Deep"}},
		{map[string]candb.Input{"Outer": candb.Raw(1), "Inner": candb.Raw(4), "Other": candb.Raw(0xBEEF)}, "41efbe0000000000", []string{"Outer", "Inner", "Other"}},
		{map[string]candb.Input{"Outer": candb.Raw(2)}, "0200000000000000", []string{"Outer"}},
	}
	for _, c := range cases {
		data, err := ext.Encode(c.in)
		if err != nil || hex.EncodeToString(data) != c.hex {
			t.Fatalf("% x %v want %s", data, err, c.hex)
		}
		sigs, _ := ext.Decode(data)
		var names []string
		for _, s := range sigs {
			names = append(names, s.Signal.Name)
		}
		if !reflect.DeepEqual(names, c.want) {
			t.Fatalf("decoded %v, want %v", names, c.want)
		}
	}
}

func TestEncodeRanges(t *testing.T) {
	db := load(t)
	eng := db.Message(100, false)
	base := map[string]candb.Input{"Speed": candb.Phys(100), "Temp": candb.Phys(20), "Throttle": candb.Phys(50), "Mode": candb.Label("Run"), "Flag": candb.Raw(1)}
	if _, err := eng.Encode(base); err != nil {
		t.Fatal(err)
	}
	try := func(name string, in candb.Input, lax bool) error {
		m := map[string]candb.Input{}
		for k, v := range base {
			m[k] = v
		}
		m[name] = in
		_, err := eng.EncodeWith(m, candb.EncodeOptions{Lax: lax})
		return err
	}
	if err := try("Temp", candb.Phys(-100), false); !errors.Is(err, candb.ErrRange) {
		t.Fatal("below minimum:", err)
	}
	if err := try("Temp", candb.Phys(-100), true); err != nil {
		t.Fatal("lax:", err)
	}
	if err := try("Temp", candb.Phys(300), true); !errors.Is(err, candb.ErrRange) {
		t.Fatal("beyond the field even when lax:", err)
	}
	if err := try("Flag", candb.Raw(2), false); !errors.Is(err, candb.ErrRange) {
		t.Fatal(err)
	}
	if err := try("Mode", candb.Label("Nope"), false); !errors.Is(err, candb.ErrRange) {
		t.Fatal(err)
	}
	if err := try("Nope", candb.Raw(0), false); !errors.Is(err, candb.ErrUnknownSignal) {
		t.Fatal(err)
	}
	// Half-to-even rounding, as cantools and canmatrix: 0.0625 rpm is half
	// a step of 0.125: rounds to 0; 0.1875 rounds to 2 (1.5 → 2).
	for p, want := range map[float64]uint64{0.0625: 0, 0.1875: 2, 0.3125: 2} {
		r, err := eng.Signal("Speed").RawFromPhysical(p)
		if err != nil || r != want {
			t.Errorf("RawFromPhysical(%v) = %d, want %d", p, r, want)
		}
	}
	f, err := eng.Frame(base, candb.EncodeOptions{})
	if err != nil || f.ID != 100 || f.Len != 8 {
		t.Fatal(f, err)
	}
	fill, err := eng.EncodeWith(map[string]candb.Input{}, candb.EncodeOptions{FillMissing: true})
	if err != nil || fill[2] != 40 { // Temp initial raw 40
		t.Fatalf("% x %v", fill, err)
	}
}

func TestDecodeFrame(t *testing.T) {
	db := load(t)
	m := db.MessageByName("FdMsg")
	f, err := m.Frame(map[string]candb.Input{"First": candb.Raw(1), "Last": candb.Raw(2)}, candb.EncodeOptions{})
	if err != nil || !f.IsFD() || !f.IsExtended() || f.Len != 64 || f.Data[63] != 2 {
		t.Fatal(f, err)
	}
	got, sigs, err := db.DecodeFrame(&f)
	if err != nil || got != m || sigs[1].Raw != 2 {
		t.Fatal(err)
	}
	f.ID = 0x7
	if _, _, err := db.DecodeFrame(&f); !errors.Is(err, candb.ErrUnknownMessage) {
		t.Fatal(err)
	}
}

func TestWriteRoundTrip(t *testing.T) {
	db := load(t)
	var buf bytes.Buffer
	if err := Write(&buf, db); err != nil {
		t.Fatal(err)
	}
	again, diags, err := ParseBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("%v\n%v\n%s", err, diags, buf.String())
	}
	if !reflect.DeepEqual(again, db) {
		for i := range db.Messages {
			if !reflect.DeepEqual(again.Messages[i], db.Messages[i]) {
				t.Errorf("message %s differs:\n%+v\n%+v", db.Messages[i].Name, again.Messages[i], db.Messages[i])
			}
		}
		if !reflect.DeepEqual(again.AttributeDefinitions, db.AttributeDefinitions) {
			t.Errorf("definitions differ:\n%+v\n%+v", again.AttributeDefinitions, db.AttributeDefinitions)
		}
		t.Fatalf("round trip differs\n%s", buf.String())
	}
	// Writing again is byte-identical: the output is deterministic.
	var buf2 bytes.Buffer
	_ = Write(&buf2, again)
	if buf.String() != buf2.String() {
		t.Fatal("non-deterministic output")
	}
}

func TestWriteSynthesizesAttributes(t *testing.T) {
	ini := int64(3)
	db := &candb.Database{Messages: []*candb.Message{{Name: "M", ID: 5, Extended: true, Length: 12, FD: true, BRS: true, CycleTime: 50,
		Signals: []*candb.Signal{{Name: "S", Length: 8, Factor: 1, Initial: &ini}}}}}
	var buf bytes.Buffer
	if err := Write(&buf, db); err != nil {
		t.Fatal(err)
	}
	again, _, err := ParseBytes(buf.Bytes())
	if err != nil {
		t.Fatal(err, buf.String())
	}
	m := again.Messages[0]
	if !m.FD || !m.BRS || m.CycleTime != 50 || *m.Signals[0].Initial != 3 || !m.Extended {
		t.Fatalf("%+v\n%s", m, buf.String())
	}
}

func TestDiagnosticsAndRecovery(t *testing.T) {
	src := `VERSION ""
BU_: A
BO_ 1 M1: 8 A
 SG_ S : 0|8@1+ (1,0) [0|0] "" A
 SG_ Bad : 0|8@2+ (1,0) [0|0] "" A
BO_ 2 M2: 8 A
 SG_ Wide : 60|8@1+ (1,0) [0|0] "" A
 SG_ Over : 4|8@1+ (1,0) [0|0] "" A
 SG_ Ov2 : 6|4@1+ (1,0) [0|0] "" A
 SG_ X m1 : 0|4@1+ (1,0) [0|0] "" A
BO_ 2 Dup: 8 A
BO_ 3 Long: 9 A
CM_ BO_ 99 "unknown";
BA_ "Undefined" 1;
BA_DEF_ BO_ "I" INT 0 10;
BA_ "I" BO_ 1 "text";
SIG_VALTYPE_ 1 S : 7;
FOO_ 1 2 3;
BU_SG_REL_ BU_ A SG_ 1 S;
VAL_ 1 Nope 1 "x" ;
`
	db, diags, err := ParseBytes([]byte(src))
	var pe *ParseError
	if !errors.As(err, &pe) || !strings.Contains(pe.Error(), "more errors") {
		t.Fatal(err)
	}
	all := ""
	for _, d := range diags {
		all += d.String() + "\n"
	}
	for _, want := range []string{
		"line 5: error: expected byte order 0 or 1",
		"error: signal M2.Wide: bits exceed the message length",
		"warning: signal M2.Ov2: overlaps signal Over",
		"multiplexed signal m1 without SG_MUL_VAL_",
		"line 11: error: message Dup: duplicate message identifier 2",
		"warning: message Long: length 9 is not a CAN FD length; frames carry 12 bytes",
		"line 13: warning: CM_ refers to unknown message 99",
		"line 14: warning: value of undefined network attribute Undefined ignored",
		"line 16: error: value \"text\" does not match the int attribute I",
		"line 17: error: signal S: SIG_VALTYPE_ 7 unknown",
		"line 18: error: unknown keyword",
		"line 19: warning: node-signal relations (BU_SG_REL_) are not supported",
		"line 20: warning: value descriptions for unknown signal M1.Nope ignored",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing diagnostic %q in\n%s", want, all)
		}
	}
	if db.MessageByName("M1") == nil || db.MessageByName("M1").Signal("S") == nil {
		t.Fatal("recovery lost the valid parts")
	}
	if _, _, err := ParseBytes([]byte(`VERSION "unterminated`)); err == nil {
		t.Fatal("unterminated string")
	}
	if _, _, err := ParseBytes([]byte("BU_: A\n$")); err == nil {
		t.Fatal("bad character")
	}
	if _, _, err := ParseFile("testdata/missing.dbc"); err == nil {
		t.Fatal("missing file")
	}
	if _, _, err := Parse(strings.NewReader("\ufeffVERSION \"x\"\n")); err != nil {
		t.Fatal("BOM", err)
	}
}

func FuzzParse(f *testing.F) {
	src, _ := readFile("testdata/full.dbc")
	f.Add(src)
	f.Add([]byte("BO_ 1 M: 8 A\n SG_ S M : 0|8@1+ (1,0) [0|0] \"\" A\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		db, _, err := ParseBytes(b)
		if err != nil {
			return
		}
		// A database parsed without errors must survive writing and
		// parsing again, and decode a full payload of every message.
		var buf bytes.Buffer
		if err := Write(&buf, db); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ParseBytes(buf.Bytes()); err != nil {
			t.Fatalf("written database does not parse: %v\n%s", err, buf.String())
		}
		for _, m := range db.Messages {
			if _, err := m.Decode(make([]byte, 64)); err != nil {
				t.Fatalf("%s: %v", m.Name, err)
			}
		}
	})
}

// TestCRLF: files written on Windows parse to the same model.
func TestCRLF(t *testing.T) {
	src, _ := readFile("testdata/full.dbc")
	want, _, _ := ParseBytes(src)
	got, _, err := ParseBytes(bytes.ReplaceAll(src, []byte("\n"), []byte("\r\n")))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("CRLF input parses differently", err)
	}
}

func TestLexerDetails(t *testing.T) {
	toks, err := lex("A 0x1F \"s\" ; 1e-3 2abc")
	if err != nil || len(toks) != 7 {
		t.Fatal(toks, err)
	}
	if toks[1].text != "0x1F" || toks[4].text != "1e-3" || toks[5].kind != tIdent || toks[6].String() != "end of file" ||
		toks[2].String() != `string "s"` || toks[0].String() != `"A"` || tNumber.String() != "number" {
		t.Fatal(toks)
	}
	db, _, err := ParseBytes([]byte("BO_ 0x10 M: 8 A\n SG_ S : 0|8@1+ (1,0) [0|0x10] \"\" A\nBA_DEF_ BO_ \"H\" HEX 0 0xFF;\nBA_ \"H\" BO_ 16 0x1F;\n"))
	if err != nil || db.Messages[0].Attributes[0].Int != 31 || db.Messages[0].Signals[0].Maximum != 16 {
		t.Fatal(err)
	}
}
