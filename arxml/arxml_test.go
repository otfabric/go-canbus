// SPDX-License-Identifier: MIT

package arxml

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
)

func TestSystem(t *testing.T) {
	db, diags, err := ParseFile("testdata/system.arxml")
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics %v", diags)
	}
	if len(db.Messages) != 3 || len(db.Nodes) != 2 {
		t.Fatalf("%d messages, %d nodes", len(db.Messages), len(db.Nodes))
	}
	eng := db.MessageByName("Engine")
	if eng.ID != 0x100 || eng.Extended || eng.FD || !reflect.DeepEqual(eng.Senders, []string{"EMS"}) {
		t.Fatalf("%+v", eng)
	}
	sp := eng.Signal("Speed")
	if sp.Factor != 0.01 || sp.Unit != "km/h" || *sp.Initial != 0 || sp.Comment != "Vehicle speed" || !reflect.DeepEqual(sp.Receivers, []string{"Dash"}) {
		t.Fatalf("Speed %+v", sp)
	}
	tmp := eng.Signal("Temp")
	if !tmp.Signed || tmp.ByteOrder != candb.BigEndian || tmp.StartBit != 23 || tmp.Offset != -40 {
		t.Fatalf("Temp %+v", tmp)
	}
	if g := eng.Signal("Gear"); g.Label(3) != "Drive" || *g.Initial != 2 {
		t.Fatalf("Gear %+v", g)
	}
	lv := eng.Signal("Level")
	if lv.Factor != 0.4 || lv.Label(255) != "NotAvailable" || lv.Label(254) != "Error" {
		t.Fatalf("Level %+v", lv)
	}
	cl := db.Message(0x18FF1200, true)
	if cl == nil || cl.Signal("Fan").StartBit != 8 || cl.Signal("Ratio").ValueType != candb.Float32 || cl.Signal("Ratio").StartBit != 16 {
		t.Fatalf("Climate (PDU at byte 1) %+v", cl)
	}
	fd := db.MessageByName("FdFrame")
	if !fd.FD || fd.Length != 64 || fd.Signal("Tail").StartBit != 504 {
		t.Fatalf("FD %+v", fd)
	}
	// Decode a payload computed by hand: Speed 0x1234 → 46.6 km/h, Temp
	// (Motorola, start 23, 10 bits): byte 2 bits 7..0 + byte 3 bits 7..6.
	data, _ := hex.DecodeString("34120003" + "00000000")
	f, _ := canbus.NewStandard(0x100, data)
	_, sigs, err := db.DecodeFrame(&f)
	if err != nil {
		t.Fatal(err)
	}
	if sigs[0].Raw != 0x1234 || math.Abs(sigs[0].Physical-46.6) > 1e-9 || sigs[1].Raw != 0 || sigs[2].Label != "Drive" {
		t.Fatalf("%+v", sigs)
	}
}

func TestDiagnostics(t *testing.T) {
	if _, _, err := Parse(strings.NewReader("<x")); err == nil {
		t.Fatal("bad XML")
	}
	if _, _, err := Parse(strings.NewReader("<NOTAUTOSAR/>")); err == nil {
		t.Fatal("not AUTOSAR")
	}
	if _, _, err := Parse(strings.NewReader(`<AUTOSAR xmlns="http://autosar.org/3.2.3"/>`)); err == nil || !strings.Contains(err.Error(), "AUTOSAR 4.x only") {
		t.Fatal(err)
	}
	if _, _, err := ParseFile("testdata/missing.arxml"); err == nil {
		t.Fatal("missing file")
	}
	if (&ParseError{}).Error() != "arxml: invalid file" {
		t.Fatal("empty ParseError")
	}
	src := `<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>P</SHORT-NAME><ELEMENTS>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T1</SHORT-NAME><FRAME-REF>/P/F</FRAME-REF><IDENTIFIER>x</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T2</SHORT-NAME><FRAME-REF>/P/Nope</FRAME-REF><IDENTIFIER>1</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T3</SHORT-NAME><FRAME-REF>/P/F</FRAME-REF><CAN-ADDRESSING-MODE>ODD</CAN-ADDRESSING-MODE><IDENTIFIER>2</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T4</SHORT-NAME><FRAME-REF>/P/Bad</FRAME-REF><IDENTIFIER>3</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T5</SHORT-NAME><FRAME-REF>/P/F</FRAME-REF><IDENTIFIER>4</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T6</SHORT-NAME><FRAME-REF>/P/F</FRAME-REF><IDENTIFIER>4</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T7</SHORT-NAME><FRAME-REF>/P/Lin</FRAME-REF><IDENTIFIER>5</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME-TRIGGERING><SHORT-NAME>T8</SHORT-NAME><FRAME-REF>/P/Empty</FRAME-REF><IDENTIFIER>6</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME><SHORT-NAME>Bad</SHORT-NAME><FRAME-LENGTH>z</FRAME-LENGTH></CAN-FRAME>
<CAN-FRAME><SHORT-NAME>Empty</SHORT-NAME><FRAME-LENGTH>8</FRAME-LENGTH></CAN-FRAME>
<LIN-UNCONDITIONAL-FRAME><SHORT-NAME>Lin</SHORT-NAME></LIN-UNCONDITIONAL-FRAME>
<CAN-FRAME><SHORT-NAME>F</SHORT-NAME><FRAME-LENGTH>8</FRAME-LENGTH><PDU-TO-FRAME-MAPPINGS>
 <PDU-TO-FRAME-MAPPING><PDU-REF>/P/Pdu</PDU-REF><START-POSITION>3</START-POSITION></PDU-TO-FRAME-MAPPING>
 <PDU-TO-FRAME-MAPPING><PDU-REF>/P/Nm</PDU-REF><START-POSITION>0</START-POSITION></PDU-TO-FRAME-MAPPING>
 <PDU-TO-FRAME-MAPPING><PDU-REF>/P/Pdu2</PDU-REF><START-POSITION>0</START-POSITION></PDU-TO-FRAME-MAPPING>
</PDU-TO-FRAME-MAPPINGS></CAN-FRAME>
<NM-PDU><SHORT-NAME>Nm</SHORT-NAME></NM-PDU>
<I-SIGNAL-I-PDU><SHORT-NAME>Pdu</SHORT-NAME></I-SIGNAL-I-PDU>
<I-SIGNAL-I-PDU><SHORT-NAME>Pdu2</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>a</SHORT-NAME></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>b</SHORT-NAME><I-SIGNAL-REF>/P/S</I-SIGNAL-REF><START-POSITION>q</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>c</SHORT-NAME><I-SIGNAL-REF>/P/S</I-SIGNAL-REF><START-POSITION>0</START-POSITION><PACKING-BYTE-ORDER>SIDEWAYS</PACKING-BYTE-ORDER></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>d</SHORT-NAME><I-SIGNAL-REF>/P/BadLen</I-SIGNAL-REF><START-POSITION>0</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>e</SHORT-NAME><I-SIGNAL-REF>/P/S</I-SIGNAL-REF><START-POSITION>0</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>f</SHORT-NAME><I-SIGNAL-REF>/P/S</I-SIGNAL-REF><START-POSITION>8</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>g</SHORT-NAME><I-SIGNAL-REF>/P/S2</I-SIGNAL-REF><START-POSITION>16</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU>
<I-SIGNAL><SHORT-NAME>S</SHORT-NAME><LENGTH>8</LENGTH></I-SIGNAL>
<I-SIGNAL><SHORT-NAME>BadLen</SHORT-NAME><LENGTH>?</LENGTH></I-SIGNAL>
<I-SIGNAL><SHORT-NAME>S2</SHORT-NAME><LENGTH>8</LENGTH><SYSTEM-SIGNAL-REF>/P/SS2</SYSTEM-SIGNAL-REF></I-SIGNAL>
<SYSTEM-SIGNAL><SHORT-NAME>SS2</SHORT-NAME><PHYSICAL-PROPS><SW-DATA-DEF-PROPS-VARIANTS><SW-DATA-DEF-PROPS-CONDITIONAL>
 <COMPU-METHOD-REF>/P/CM</COMPU-METHOD-REF><DATA-CONSTR-REF>/P/DC</DATA-CONSTR-REF></SW-DATA-DEF-PROPS-CONDITIONAL></SW-DATA-DEF-PROPS-VARIANTS></PHYSICAL-PROPS></SYSTEM-SIGNAL>
<COMPU-METHOD><SHORT-NAME>CM</SHORT-NAME><CATEGORY>SCALE_LINEAR_AND_TEXTTABLE</CATEGORY><COMPU-INTERNAL-TO-PHYS><COMPU-SCALES>
 <COMPU-SCALE><COMPU-RATIONAL-COEFFS><COMPU-NUMERATOR><V>1</V><V>2</V></COMPU-NUMERATOR><COMPU-DENOMINATOR><V>2</V></COMPU-DENOMINATOR></COMPU-RATIONAL-COEFFS></COMPU-SCALE>
 <COMPU-SCALE><COMPU-RATIONAL-COEFFS><COMPU-NUMERATOR><V>5</V><V>5</V></COMPU-NUMERATOR></COMPU-RATIONAL-COEFFS></COMPU-SCALE>
 <COMPU-SCALE><COMPU-RATIONAL-COEFFS><COMPU-NUMERATOR><V>5</V><V>5</V><V>1</V></COMPU-NUMERATOR></COMPU-RATIONAL-COEFFS></COMPU-SCALE>
 <COMPU-SCALE><LOWER-LIMIT>0</LOWER-LIMIT><UPPER-LIMIT>1000</UPPER-LIMIT><COMPU-CONST><VT>Wide</VT></COMPU-CONST></COMPU-SCALE>
</COMPU-SCALES></COMPU-INTERNAL-TO-PHYS></COMPU-METHOD>
<DATA-CONSTR><SHORT-NAME>DC</SHORT-NAME><DATA-CONSTR-RULES><DATA-CONSTR-RULE><PHYS-CONSTRS><LOWER-LIMIT>-5</LOWER-LIMIT><UPPER-LIMIT>200</UPPER-LIMIT></PHYS-CONSTRS></DATA-CONSTR-RULE></DATA-CONSTR-RULES></DATA-CONSTR>
<COMPU-METHOD><SHORT-NAME>Odd</SHORT-NAME><CATEGORY>BITFIELD_TEXTTABLE</CATEGORY></COMPU-METHOD>
</ELEMENTS></AR-PACKAGE></AR-PACKAGES></AUTOSAR>`
	db, diags, err := Parse(strings.NewReader(src))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatal(err)
	}
	all := ""
	for _, d := range diags {
		all += d.String() + "\n"
	}
	for _, want := range []string{"bad IDENTIFIER", "/P/Nope does not resolve", "unknown CAN-ADDRESSING-MODE", "bad FRAME-LENGTH",
		"identifier 0x4 triggered twice", "LIN-UNCONDITIONAL-FRAME: ignored", "frame without PDUs", "only byte offsets", "NM-PDU Nm is not supported",
		"without I-SIGNAL-REF", "bad START-POSITION", "unknown PACKING-BYTE-ORDER", "bad LENGTH", "duplicate signal S",
		"more than one linear scale", "only linear rational", "text scale \"Wide\"", "network management PDUs"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	s2 := db.Message(4, false).Signal("S2")
	if s2.Factor != 1 || s2.Offset != 0.5 || s2.Minimum != -5 || s2.Maximum != 200 {
		t.Fatalf("%+v", s2)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES/></AUTOSAR>`))
	f.Fuzz(func(t *testing.T, b []byte) {
		db, _, err := Parse(strings.NewReader(string(b)))
		if err != nil {
			return
		}
		for _, m := range db.Messages {
			if _, err := m.Decode(make([]byte, 64)); err != nil && m.Length <= 64 {
				t.Fatal(err)
			}
		}
	})
}

// TestMultiplexed reads canbus-interop's multiplexed.arxml fixture as
// cantools does and decodes the self-test's probe frames.
func TestMultiplexed(t *testing.T) {
	db, diags, err := ParseFile("testdata/multiplexed.arxml")
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	m := db.MessageByName("MuxFrame")
	sel := m.Signal("MuxFrame_selector1")
	if sel == nil || !sel.IsMultiplexer || sel.StartBit != 0 || sel.Length != 4 || sel.ByteOrder != candb.LittleEndian || *sel.Initial != 0 {
		t.Fatalf("selector %+v", sel)
	}
	if m.Signal("Sel") != nil {
		t.Fatal("the alternatives' selector mapping was kept")
	}
	if c := m.Signal("Counter"); c.Multiplexer != "" {
		t.Fatalf("static Counter %+v", c)
	}
	if p := m.Signal("Pressure"); p.Multiplexer != sel.Name || !reflect.DeepEqual(p.MuxValues, []candb.MuxRange{{Low: 1, High: 1}}) {
		t.Fatalf("Pressure %+v", p)
	}
	if b := db.MessageByName("BeMuxFrame").Signal("BeMuxFrame_selector1"); b.ByteOrder != candb.BigEndian || b.StartBit != 7 || *b.Initial != 10 {
		t.Fatalf("BE selector %+v", b)
	}
	for _, c := range []struct {
		id   uint32
		data string
		want map[string]string // raw values, "raw/label"
	}{
		{0x310, "01279403000000c8", map[string]string{"MuxFrame_selector1": "1", "Pressure": "10132", "Mode": "3/Sport", "Counter": "200"}},
		{0x310, "f05a010000000007", map[string]string{"MuxFrame_selector1": "0", "Temp": "90", "Flag": "1", "Counter": "7"}},
		{0x311, "0a00fff000000000", map[string]string{"BeMuxFrame_selector1": "10", "Y": "4095"}},
	} {
		data, _ := hex.DecodeString(c.data)
		msg := db.Message(canbus.ID(c.id), false)
		sigs, err := msg.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, d := range sigs {
			v := d.Signal.RawString(d.Value)
			if d.Label != "" {
				v += "/" + d.Label
			}
			got[d.Signal.Name] = v
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.data, got, c.want)
		}
	}
}

func TestMultiplexedVariants(t *testing.T) {
	const head = `<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>P</SHORT-NAME><ELEMENTS>`
	const tail = `
<I-SIGNAL-I-PDU><SHORT-NAME>A</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>a1</SHORT-NAME><I-SIGNAL-REF>/P/Sel</I-SIGNAL-REF><START-POSITION>8</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>a2</SHORT-NAME><I-SIGNAL-REF>/P/Shared</I-SIGNAL-REF><START-POSITION>16</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU>
<I-SIGNAL-I-PDU><SHORT-NAME>B</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>b1</SHORT-NAME><I-SIGNAL-REF>/P/Shared</I-SIGNAL-REF><START-POSITION>16</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>b2</SHORT-NAME><I-SIGNAL-REF>/P/Moved</I-SIGNAL-REF><START-POSITION>24</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU>
<I-SIGNAL-I-PDU><SHORT-NAME>C</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>c1</SHORT-NAME><I-SIGNAL-REF>/P/Moved</I-SIGNAL-REF><START-POSITION>32</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU>
<I-SIGNAL-I-PDU><SHORT-NAME>St</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>
 <I-SIGNAL-TO-I-PDU-MAPPING><SHORT-NAME>s1</SHORT-NAME><I-SIGNAL-REF>/P/Static</I-SIGNAL-REF><START-POSITION>0</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>
</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU>
<NM-PDU><SHORT-NAME>Nm</SHORT-NAME></NM-PDU>
<I-SIGNAL><SHORT-NAME>Sel</SHORT-NAME><LENGTH>8</LENGTH><SYSTEM-SIGNAL-REF>/P/SelS</SYSTEM-SIGNAL-REF></I-SIGNAL>
<SYSTEM-SIGNAL><SHORT-NAME>SelS</SHORT-NAME><PHYSICAL-PROPS><SW-DATA-DEF-PROPS-VARIANTS><SW-DATA-DEF-PROPS-CONDITIONAL>
 <COMPU-METHOD-REF>/P/SelCM</COMPU-METHOD-REF></SW-DATA-DEF-PROPS-CONDITIONAL></SW-DATA-DEF-PROPS-VARIANTS></PHYSICAL-PROPS></SYSTEM-SIGNAL>
<COMPU-METHOD><SHORT-NAME>SelCM</SHORT-NAME><CATEGORY>TEXTTABLE</CATEGORY><COMPU-INTERNAL-TO-PHYS><COMPU-SCALES>
 <COMPU-SCALE><LOWER-LIMIT>1</LOWER-LIMIT><UPPER-LIMIT>1</UPPER-LIMIT><COMPU-CONST><VT>One</VT></COMPU-CONST></COMPU-SCALE>
</COMPU-SCALES></COMPU-INTERNAL-TO-PHYS></COMPU-METHOD>
<I-SIGNAL><SHORT-NAME>Shared</SHORT-NAME><LENGTH>8</LENGTH></I-SIGNAL>
<I-SIGNAL><SHORT-NAME>Moved</SHORT-NAME><LENGTH>8</LENGTH></I-SIGNAL>
<I-SIGNAL><SHORT-NAME>Static</SHORT-NAME><LENGTH>8</LENGTH></I-SIGNAL>
</ELEMENTS></AR-PACKAGE></AR-PACKAGES></AUTOSAR>`
	frame := func(id int, pdu string, pos int) string {
		return fmt.Sprintf(`<CAN-FRAME-TRIGGERING><SHORT-NAME>T%d</SHORT-NAME><FRAME-REF>/P/F%d</FRAME-REF><IDENTIFIER>%d</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME><SHORT-NAME>F%d</SHORT-NAME><FRAME-LENGTH>8</FRAME-LENGTH><PDU-TO-FRAME-MAPPINGS>
 <PDU-TO-FRAME-MAPPING><PDU-REF>/P/%s</PDU-REF><START-POSITION>%d</START-POSITION></PDU-TO-FRAME-MAPPING></PDU-TO-FRAME-MAPPINGS></CAN-FRAME>`, id, id, id, id, pdu, pos)
	}
	alt := func(pdu string, code string, initial bool) string {
		return fmt.Sprintf(`<DYNAMIC-PART-ALTERNATIVE><I-PDU-REF>/P/%s</I-PDU-REF><INITIAL-DYNAMIC-PART>%v</INITIAL-DYNAMIC-PART><SELECTOR-FIELD-CODE>%s</SELECTOR-FIELD-CODE></DYNAMIC-PART-ALTERNATIVE>`, pdu, initial, code)
	}
	src := head +
		// Legacy layout (DYNAMIC-PART, STATIC-PART), a PDU offset of one byte.
		frame(1, "Legacy", 8) + `<MULTIPLEXED-I-PDU><SHORT-NAME>Legacy</SHORT-NAME>
 <DYNAMIC-PART><DYNAMIC-PART-ALTERNATIVES>` + alt("A", "1", false) + alt("B", "0x2", true) + alt("C", "3", false) + `</DYNAMIC-PART-ALTERNATIVES></DYNAMIC-PART>
 <SELECTOR-FIELD-LENGTH>8</SELECTOR-FIELD-LENGTH><SELECTOR-FIELD-START-POSITION>8</SELECTOR-FIELD-START-POSITION>
 <STATIC-PART><I-PDU-REF>/P/St</I-PDU-REF></STATIC-PART></MULTIPLEXED-I-PDU>` +
		frame(2, "BadSel", 0) + `<MULTIPLEXED-I-PDU><SHORT-NAME>BadSel</SHORT-NAME><SELECTOR-FIELD-LENGTH>0</SELECTOR-FIELD-LENGTH><SELECTOR-FIELD-START-POSITION>0</SELECTOR-FIELD-START-POSITION></MULTIPLEXED-I-PDU>` +
		frame(3, "Odd", 0) + `<MULTIPLEXED-I-PDU><SHORT-NAME>Odd</SHORT-NAME><SELECTOR-FIELD-BYTE-ORDER>SIDEWAYS</SELECTOR-FIELD-BYTE-ORDER><SELECTOR-FIELD-LENGTH>4</SELECTOR-FIELD-LENGTH><SELECTOR-FIELD-START-POSITION>0</SELECTOR-FIELD-START-POSITION></MULTIPLEXED-I-PDU>` +
		frame(4, "Weird", 0) + `<MULTIPLEXED-I-PDU><SHORT-NAME>Weird</SHORT-NAME>
 <DYNAMIC-PARTS><DYNAMIC-PART><DYNAMIC-PART-ALTERNATIVES>` + alt("A", "16", false) + alt("A", "x", false) + alt("Nm", "1", false) + alt("Gone", "2", false) + `</DYNAMIC-PART-ALTERNATIVES></DYNAMIC-PART></DYNAMIC-PARTS>
 <SELECTOR-FIELD-LENGTH>4</SELECTOR-FIELD-LENGTH><SELECTOR-FIELD-START-POSITION>0</SELECTOR-FIELD-START-POSITION>
 <STATIC-PARTS><STATIC-PART><I-PDU-REF>/P/Nm</I-PDU-REF></STATIC-PART><STATIC-PART><I-PDU-REF>/P/Gone</I-PDU-REF></STATIC-PART></STATIC-PARTS></MULTIPLEXED-I-PDU>` +
		frame(5, "Empty", 0) + `<MULTIPLEXED-I-PDU><SHORT-NAME>Empty</SHORT-NAME><SELECTOR-FIELD-LENGTH>4</SELECTOR-FIELD-LENGTH><SELECTOR-FIELD-START-POSITION>0</SELECTOR-FIELD-START-POSITION></MULTIPLEXED-I-PDU>` +
		tail
	db, diags, err := Parse(strings.NewReader(src))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatal(err)
	}
	all := ""
	for _, d := range diags {
		all += d.String() + "\n"
	}
	for _, want := range []string{"multiplexed PDU BadSel: bad selector field", "unknown SELECTOR-FIELD-BYTE-ORDER \"SIDEWAYS\"",
		"bad SELECTOR-FIELD-CODE \"16\"", "bad SELECTOR-FIELD-CODE \"x\"", "alternative Nm is a NM-PDU", "/P/Gone does not resolve",
		"static part Nm is a NM-PDU", "multiplexed PDU Empty: no dynamic part alternatives", "duplicate signal Moved"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	m := db.Message(1, false)
	sel := m.Signal("F1_selector1")
	if sel.StartBit != 16 || *sel.Initial != 2 || !reflect.DeepEqual(sel.Values, []candb.ValueDescription{{Value: 1, Description: "One"}}) {
		t.Fatalf("selector %+v", sel)
	}
	if s := m.Signal("Shared"); s.StartBit != 24 || !reflect.DeepEqual(s.MuxValues, []candb.MuxRange{{Low: 1, High: 1}, {Low: 2, High: 2}}) {
		t.Fatalf("Shared %+v", s)
	}
	if s := m.Signal("Static"); s.StartBit != 8 || s.Multiplexer != "" {
		t.Fatalf("Static %+v", s)
	}
	if s := m.Signal("Moved"); s.StartBit != 32 || !reflect.DeepEqual(s.MuxValues, []candb.MuxRange{{Low: 2, High: 2}}) {
		t.Fatalf("Moved %+v (the second mapping, at another position, is a duplicate)", s)
	}
}
