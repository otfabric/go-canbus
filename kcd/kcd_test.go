// SPDX-License-Identifier: MIT

package kcd

import (
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
)

// TestSample checks the layout canmatrix 1.3.3 derives from the same file
// (canconvert sample.kcd out.dbc):
//
//	BO_ 16 M: 8 A
//	 SG_ BE12 : 15|12@0+ (0.5,-1) [0|10] "V" B
//	 SG_ LE8 : 40|8@1- (1,0) [-128|127] "" Vector__XXX
//	BO_ 2147488308 X: 8 Vector__XXX
//	 SG_ Mux M : 0|4@1+ (1,0) [0|15] "" Vector__XXX
//	 SG_ P m0 : 8|8@1+ (1,0) [0|255] "" Vector__XXX
//	 SG_ Q m1 : 15|16@0+ (1,0) [0|65535] "" Vector__XXX
func TestSample(t *testing.T) {
	db, diags, err := ParseFile("testdata/sample.kcd")
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	m := db.MessageByName("M")
	be := m.Signal("BE12")
	if m.ID != 16 || m.CycleTime != 100 || m.Senders[0] != "A" || be.StartBit != 15 || be.ByteOrder != candb.BigEndian ||
		be.Factor != 0.5 || be.Offset != -1 || be.Unit != "V" || be.Receivers[0] != "B" || be.Maximum != 10 {
		t.Fatalf("%+v %+v", m, be)
	}
	le := m.Signal("LE8")
	if le.StartBit != 40 || !le.Signed || le.Label(0) != "Zero" {
		t.Fatalf("%+v", le)
	}
	x := db.MessageByName("X")
	if !x.Extended || x.ID != 0x1234 || !x.Signal("Mux").IsMultiplexer || x.Signal("Q").StartBit != 15 || x.Signal("Q").MuxValues[0].Low != 1 ||
		x.Signal("P").Multiplexer != "Mux" || x.Length != 8 {
		t.Fatalf("%+v", x)
	}
	if db.Version != "1" || len(db.Nodes) != 2 {
		t.Fatal(db.Version)
	}
}

func TestDiagnostics(t *testing.T) {
	src := `<NetworkDefinition xmlns="http://kayak.2codeornot2code.org/1.0">
<Document version="2">notes</Document>
<Node id="1" name="A"/>
<Foo/>
<Bus name="a"><Message id="0x1" name="R" remote="true" interval="x" length="auto">
  <Signal name="S" offset="0" length="16"><Value type="single"/><Bar/></Signal>
  <Signal name="L" offset="16" length="4"><LabelSet><Label name="a" value="x"/><LabelGroup name="g" from="1" to="3"/><LabelGroup name="big" from="0" to="1000"/><LabelGroup name="bad" from="3" to="1"/></LabelSet></Signal>
  <Producer><NodeRef id="9"/></Producer>
</Message>
<Message id="zz" name="Bad"/>
<Message id="2" name="F" format="odd" length="x"/>
<Message id="3" name="E"><Signal name="E1" offset="-1"/><Signal name="E2" offset="0" length="z"/><Signal name="E3" offset="0" endianess="mid"/>
  <Signal name="E4" offset="0"><Value type="complex" slope="q"/></Signal>
  <Multiplex name="MX" offset="8" length="2"><MuxGroup count="w"><Signal name="Z" offset="16"/></MuxGroup></Multiplex>
</Message></Bus>
<Bus name="b"/>
</NetworkDefinition>`
	db, diags, err := Parse(strings.NewReader(src))
	if err == nil {
		t.Fatal("errors not reported")
	}
	var all []string
	for _, d := range diags {
		all = append(all, d.String())
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{"element <Foo> is not supported", "2 buses merged", "remote frame definition", "bad interval",
		"element <Bar>", "bad label value", "LabelGroup big (1001 values) not expanded", "bad LabelGroup", "unknown node \"9\"",
		"bad id \"zz\"", "unknown format \"odd\"", "bad length \"x\"", "bad offset \"-1\"", "bad length \"z\"", "unknown endianess",
		"unknown value type", "bad number in <Value>", "bad MuxGroup count", "float32 signal of 16 bits"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	r := db.MessageByName("R")
	if r == nil || r.Length != 3 || r.Signal("L").Label(2) != "g" || db.Comment != "notes" {
		t.Fatalf("%+v", r)
	}
	if _, _, err := Parse(strings.NewReader("<x")); err == nil || !strings.Contains(err.Error(), "XML") {
		t.Fatal(err)
	}
	if _, _, err := ParseFile("testdata/missing.kcd"); err == nil {
		t.Fatal("missing file")
	}
	if (&ParseError{}).Error() != "kcd: invalid file" {
		t.Fatal("empty ParseError")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`<NetworkDefinition><Bus><Message id="1" name="M"><Signal name="S" offset="0" length="8"/></Message></Bus></NetworkDefinition>`))
	f.Fuzz(func(t *testing.T, b []byte) {
		db, _, err := Parse(strings.NewReader(string(b)))
		if err != nil {
			return
		}
		for _, m := range db.Messages {
			if _, err := m.Decode(make([]byte, 64)); err != nil && m.Length <= 64 {
				t.Fatalf("%s: %v", m.Name, err)
			}
		}
	})
}
