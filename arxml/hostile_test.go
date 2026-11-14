// SPDX-License-Identifier: MIT

package arxml

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus/candb"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func diagText(src string) (string, error) {
	_, diags, err := Parse(strings.NewReader(src))
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.String() + "\n")
	}
	return b.String(), err
}

// TestSelectorInitialBeyondInt64: a 64-bit selector whose initial
// SELECTOR-FIELD-CODE is 2^63 or more has no int64 initial value. It
// used to wrap to a negative Initial that an unsigned selector cannot
// encode; it is now dropped with a warning.
func TestSelectorInitialBeyondInt64(t *testing.T) {
	src := readFixture(t, "multiplexed.arxml")
	src = strings.Replace(src, "<SELECTOR-FIELD-LENGTH>4</SELECTOR-FIELD-LENGTH>", "<SELECTOR-FIELD-LENGTH>64</SELECTOR-FIELD-LENGTH>", 1)
	src = strings.Replace(src, "<SELECTOR-FIELD-CODE>0</SELECTOR-FIELD-CODE>", "<SELECTOR-FIELD-CODE>9223372036854775808</SELECTOR-FIELD-CODE>", 1)
	db, _, _ := Parse(strings.NewReader(src))
	all, _ := diagText(src)
	if !strings.Contains(all, "initial SELECTOR-FIELD-CODE 9223372036854775808 beyond an initial value") {
		t.Fatalf("diagnostics:\n%s", all)
	}
	if m := db.MessageByName("MuxFrame"); m != nil {
		if sel := m.Signal("MuxFrame_selector1"); sel != nil && sel.Initial != nil {
			t.Fatalf("wrapped initial %d", *sel.Initial)
		}
	}
	// The largest int64 code is still an initial value.
	src = strings.Replace(src, "9223372036854775808", "9223372036854775807", 1)
	db, _, _ = Parse(strings.NewReader(src))
	if sel := db.MessageByName("MuxFrame").Signal("MuxFrame_selector1"); sel == nil || sel.Initial == nil || *sel.Initial != 1<<63-1 {
		t.Fatalf("selector %+v", sel)
	}
}

// TestInitValueBeyondInt64: an integral INIT-VALUE outside the int64
// range is not converted (the float to int conversion is undefined there).
func TestInitValueBeyondInt64(t *testing.T) {
	src := readFixture(t, "system.arxml")
	for _, v := range []string{"1e30", "-1e30", "9223372036854775808", "-1e19"} {
		s := strings.Replace(src, "<VALUE>2</VALUE>", "<VALUE>"+v+"</VALUE>", 1)
		db, _, _ := Parse(strings.NewReader(s))
		if g := db.MessageByName("Engine").Signal("Gear"); g.Initial != nil {
			t.Errorf("INIT-VALUE %s: initial %d", v, *g.Initial)
		}
	}
	s := strings.Replace(src, "<VALUE>2</VALUE>", "<VALUE>-9223372036854775808</VALUE>", 1)
	db, _, _ := Parse(strings.NewReader(s))
	if g := db.MessageByName("Engine").Signal("Gear"); g.Initial == nil || *g.Initial != -1<<63 {
		t.Errorf("INIT-VALUE MinInt64: %v", g.Initial)
	}
}

// TestHostileXML: oversized, deeply nested and malformed documents end
// in an error or diagnostics, never a panic.
func TestHostileXML(t *testing.T) {
	sys := readFixture(t, "system.arxml")
	cases := map[string]string{
		"deep nesting":       "<AUTOSAR>" + strings.Repeat("<X>", 100000) + strings.Repeat("</X>", 100000) + "</AUTOSAR>",
		"unclosed nesting":   "<AUTOSAR>" + strings.Repeat("<X>", 100000),
		"huge text":          "<AUTOSAR><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>" + strings.Repeat("a", 1<<20) + "</SHORT-NAME></AR-PACKAGE></AR-PACKAGES></AUTOSAR>",
		"identifier too big": strings.Replace(sys, "<IDENTIFIER>256</IDENTIFIER>", "<IDENTIFIER>4294967296</IDENTIFIER>", 1),
		"identifier minus":   strings.Replace(sys, "<IDENTIFIER>256</IDENTIFIER>", "<IDENTIFIER>-1</IDENTIFIER>", 1),
		"frame length huge":  strings.Replace(sys, "<FRAME-LENGTH>8</FRAME-LENGTH>", "<FRAME-LENGTH>9223372036854775807</FRAME-LENGTH>", 1),
		"frame length minus": strings.Replace(sys, "<FRAME-LENGTH>8</FRAME-LENGTH>", "<FRAME-LENGTH>-8</FRAME-LENGTH>", 1),
		"signal length huge": strings.Replace(sys, "<LENGTH>16</LENGTH>", "<LENGTH>9223372036854775807</LENGTH>", 1),
		"signal length 0":    strings.Replace(sys, "<LENGTH>16</LENGTH>", "<LENGTH>0</LENGTH>", 1),
		"signal length 65":   strings.Replace(sys, "<LENGTH>16</LENGTH>", "<LENGTH>65</LENGTH>", 1),
		"start minus":        strings.Replace(sys, "<START-POSITION>23</START-POSITION>", "<START-POSITION>-23</START-POSITION>", 1),
		"start huge":         strings.Replace(sys, "<START-POSITION>23</START-POSITION>", "<START-POSITION>9223372036854775807</START-POSITION>", 1),
		"pdu offset huge":    strings.Replace(sys, "<START-POSITION>8</START-POSITION></PDU-TO-FRAME-MAPPING>", "<START-POSITION>9223372036854775807</START-POSITION></PDU-TO-FRAME-MAPPING>", 1),
		"entity expansion":   `<!DOCTYPE a [<!ENTITY x "xxxxxxxxxx">]><AUTOSAR>&x;&x;&x;</AUTOSAR>`,
		"truncated":          sys[:len(sys)/2],
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			db, _, err := Parse(strings.NewReader(src))
			if err != nil {
				return
			}
			for _, m := range db.Messages {
				if m.Length <= 64 {
					if _, err := m.Decode(make([]byte, 64)); err != nil {
						t.Fatalf("%s: %v", m.Name, err)
					}
				}
			}
		})
	}
}

// fanDoc builds T triggerings (identifiers 1..T) of frame /F/f, which maps
// PDU /F/p K times; the PDU maps S signals sharing one COMPU-METHOD of
// the given category and scales.
func fanDoc(T, K, S int, category, scales string) string {
	var b strings.Builder
	b.WriteString(`<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>C</SHORT-NAME><ELEMENTS>`)
	for i := 0; i < T; i++ {
		fmt.Fprintf(&b, `<CAN-FRAME-TRIGGERING><SHORT-NAME>t%d</SHORT-NAME><FRAME-REF>/F/f</FRAME-REF><IDENTIFIER>%d</IDENTIFIER></CAN-FRAME-TRIGGERING>`, i, i+1)
	}
	b.WriteString(`</ELEMENTS></AR-PACKAGE><AR-PACKAGE><SHORT-NAME>F</SHORT-NAME><ELEMENTS><CAN-FRAME><SHORT-NAME>f</SHORT-NAME><FRAME-LENGTH>64</FRAME-LENGTH><PDU-TO-FRAME-MAPPINGS>`)
	for i := 0; i < K; i++ {
		b.WriteString(`<PDU-TO-FRAME-MAPPING><PDU-REF>/F/p</PDU-REF><START-POSITION>0</START-POSITION></PDU-TO-FRAME-MAPPING>`)
	}
	b.WriteString(`</PDU-TO-FRAME-MAPPINGS></CAN-FRAME><I-SIGNAL-I-PDU><SHORT-NAME>p</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>`)
	for i := 0; i < S; i++ {
		fmt.Fprintf(&b, `<I-SIGNAL-TO-I-PDU-MAPPING><I-SIGNAL-REF>/F/s%d</I-SIGNAL-REF><START-POSITION>%d</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>`, i, i%512)
	}
	b.WriteString(`</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU>`)
	for i := 0; i < S; i++ {
		fmt.Fprintf(&b, `<I-SIGNAL><SHORT-NAME>s%d</SHORT-NAME><LENGTH>1</LENGTH><SYSTEM-SIGNAL-REF>/F/ss</SYSTEM-SIGNAL-REF></I-SIGNAL>`, i)
	}
	b.WriteString(`<SYSTEM-SIGNAL><SHORT-NAME>ss</SHORT-NAME><PHYSICAL-PROPS><SW-DATA-DEF-PROPS-VARIANTS><SW-DATA-DEF-PROPS-CONDITIONAL><COMPU-METHOD-REF>/F/cm</COMPU-METHOD-REF></SW-DATA-DEF-PROPS-CONDITIONAL></SW-DATA-DEF-PROPS-VARIANTS></PHYSICAL-PROPS></SYSTEM-SIGNAL>`)
	b.WriteString(`<COMPU-METHOD><SHORT-NAME>cm</SHORT-NAME><CATEGORY>` + category + `</CATEGORY><COMPU-INTERNAL-TO-PHYS><COMPU-SCALES>` + scales + `</COMPU-SCALES></COMPU-INTERNAL-TO-PHYS></COMPU-METHOD>`)
	b.WriteString(`</ELEMENTS></AR-PACKAGE></AR-PACKAGES></AUTOSAR>`)
	return b.String()
}

func textScale(lo, hi string) string {
	return `<COMPU-SCALE><LOWER-LIMIT>` + lo + `</LOWER-LIMIT><UPPER-LIMIT>` + hi + `</UPPER-LIMIT><COMPU-CONST><VT>v</VT></COMPU-CONST></COMPU-SCALE>`
}

// parseWithin parses src and fails the test if Parse does not return
// within d.
func parseWithin(t *testing.T, src string, d time.Duration) (*candb.Database, []candb.Diagnostic, error) {
	t.Helper()
	type result struct {
		db    *candb.Database
		diags []candb.Diagnostic
		err   error
	}
	done := make(chan result, 1)
	go func() {
		db, diags, err := Parse(strings.NewReader(src))
		done <- result{db, diags, err}
	}()
	select {
	case r := <-done:
		return r.db, r.diags, r.err
	case <-time.After(d):
		t.Fatalf("Parse did not return within %v", d)
		return nil, nil, nil
	}
}

// allocBytes reports the bytes allocated by Parse(src).
func allocBytes(src string) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, _ = Parse(strings.NewReader(src))
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func countValues(db *candb.Database) (signals, values int) {
	for _, m := range db.Messages {
		for _, s := range m.Signals {
			signals++
			values += len(s.Values)
		}
	}
	return
}

// TestTextTableLimits: TEXTTABLE scales with non-finite or out-of-range
// limits are not expanded; in-range scales still are.
func TestTextTableLimits(t *testing.T) {
	cases := []struct {
		lo, hi string
		values int
	}{
		{"NaN", "1e18", 0},
		{"0", "NaN", 0},
		{"Inf", "Inf", 0},
		{"-Inf", "-Inf", 0},
		{"1e300", "1e300", 0},
		{"-1e300", "-1e300", 0},
		{"9223372036854775807", "9223372036854775807", 0},
		{"9007199254740992", "9007199254740992", 1},
		{"-9007199254740992", "-9007199254740737", 256},
		{"0", "255", 256},
		{"0", "256", 0},
	}
	for _, c := range cases {
		t.Run(c.lo+".."+c.hi, func(t *testing.T) {
			db, diags, _ := parseWithin(t, fanDoc(1, 1, 1, "TEXTTABLE", textScale(c.lo, c.hi)), 10*time.Second)
			if _, n := countValues(db); n != c.values {
				t.Fatalf("%d values, want %d", n, c.values)
			}
			if c.values == 0 && !strings.Contains(fmt.Sprint(diags), "not supported") {
				t.Fatalf("diagnostics %v", diags)
			}
		})
	}
}

// TestPDUMappedTwice: a PDU mapped more than once into one frame is an
// error and its signals are read once.
func TestPDUMappedTwice(t *testing.T) {
	db, diags, err := Parse(strings.NewReader(fanDoc(1, 3, 4, "TEXTTABLE", textScale("0", "3"))))
	if err == nil || !strings.Contains(fmt.Sprint(diags), "PDU p mapped more than once") {
		t.Fatalf("err %v, diagnostics %v", err, diags)
	}
	if strings.Contains(fmt.Sprint(diags), "duplicate signal") {
		t.Fatalf("signals read twice: %v", diags)
	}
	if n, v := countValues(db); n != 4 || v != 16 {
		t.Fatalf("%d signals, %d values", n, v)
	}
}

// TestFanOutGrowth: repeated PDU mappings add nothing and a shared
// COMPU-METHOD is computed once, so allocation grows with the signals
// built, not with the number of references.
func TestFanOutGrowth(t *testing.T) {
	scale := textScale("0", "255")
	base := allocBytes(fanDoc(16, 1, 16, "TEXTTABLE", scale))
	repeated := allocBytes(fanDoc(16, 16, 16, "TEXTTABLE", scale))
	if repeated > 2*base {
		t.Errorf("K=16: %d bytes, K=1: %d bytes", repeated, base)
	}
	// Twice the triggerings and signals build four times the signals.
	double := allocBytes(fanDoc(32, 32, 32, "TEXTTABLE", scale))
	if double > 8*base {
		t.Errorf("T=K=S=32: %d bytes, T=S=16 K=1: %d bytes", double, base)
	}
}

// TestDatabaseBounds: a database beyond maxValues value descriptions or
// maxSignals signals ends Parse with an error.
func TestDatabaseBounds(t *testing.T) {
	_, diags, err := parseWithin(t, fanDoc(40, 1, 256, "TEXTTABLE", textScale("0", "255")), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "more than 2097152 value descriptions") {
		t.Fatalf("err %v, %d diagnostics", err, len(diags))
	}
	_, diags, err = parseWithin(t, fanDoc(1100, 1, 256, "IDENTICAL", ""), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "more than 262144 signals") {
		t.Fatalf("err %v, %d diagnostics", err, len(diags))
	}
}

// TestDiagnosticsBound: at most maxDiagnostics diagnostics are returned,
// followed by one that counts the rest with the worst severity left out.
func TestDiagnosticsBound(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>F</SHORT-NAME><ELEMENTS>
<CAN-FRAME-TRIGGERING><SHORT-NAME>t</SHORT-NAME><FRAME-REF>/F/f</FRAME-REF><IDENTIFIER>1</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME><SHORT-NAME>f</SHORT-NAME><FRAME-LENGTH>8</FRAME-LENGTH><PDU-TO-FRAME-MAPPINGS><PDU-TO-FRAME-MAPPING><PDU-REF>/F/p</PDU-REF><START-POSITION>0</START-POSITION></PDU-TO-FRAME-MAPPING></PDU-TO-FRAME-MAPPINGS></CAN-FRAME>
<I-SIGNAL-I-PDU><SHORT-NAME>p</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>`)
	for i := 0; i < 3000; i++ {
		b.WriteString("<I-SIGNAL-TO-I-PDU-MAPPING><I-SIGNAL-REF>/F/s</I-SIGNAL-REF><START-POSITION>0</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>\n")
	}
	b.WriteString(`</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU><I-SIGNAL><SHORT-NAME>s</SHORT-NAME><LENGTH>8</LENGTH></I-SIGNAL></ELEMENTS></AR-PACKAGE></AR-PACKAGES></AUTOSAR>`)
	_, diags, err := Parse(strings.NewReader(b.String()))
	if err == nil || len(diags) != maxDiagnostics+1 {
		t.Fatalf("err %v, %d diagnostics", err, len(diags))
	}
	last := diags[len(diags)-1]
	if last.Severity != candb.Error || last.Message != "1999 more diagnostics not reported" {
		t.Fatalf("last diagnostic %+v", last)
	}
}

// TestCharDataLinear: character data split into many pieces (here by
// comments) is collected in linear time and memory.
func TestCharDataLinear(t *testing.T) {
	doc := func(n int) string {
		return `<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><X>` + strings.Repeat("aaaaaaaa<!---->", n) + `</X></AUTOSAR>`
	}
	small, large := allocBytes(doc(10000)), allocBytes(doc(40000))
	if large > 8*small {
		t.Fatalf("40000 pieces: %d bytes, 10000 pieces: %d bytes", large, small)
	}
}

// TestNestingBound: elements nested deeper than maxDepth end Parse with
// an error; maxDepth levels are read.
func TestNestingBound(t *testing.T) {
	doc := func(depth int) string { // AUTOSAR, depth-2 named levels, SHORT-NAME
		return `<AUTOSAR xmlns="http://autosar.org/schema/r4.0">` + strings.Repeat("<X><SHORT-NAME>a</SHORT-NAME>", depth-2) + strings.Repeat("</X>", depth-2) + `</AUTOSAR>`
	}
	if _, diags, err := Parse(strings.NewReader(doc(maxDepth))); err != nil {
		t.Fatalf("depth %d: %v %v", maxDepth, err, diags)
	}
	_, _, err := Parse(strings.NewReader(doc(maxDepth + 1)))
	if err == nil || !strings.Contains(err.Error(), "nested deeper than 256 levels") {
		t.Fatalf("depth %d: %v", maxDepth+1, err)
	}
}

// TestManyECUInstances: frame ports are collected in one walk of each
// ECU-INSTANCE, not by scanning every element per ECU.
func TestManyECUInstances(t *testing.T) {
	const n = 64000
	var b strings.Builder
	b.WriteString(`<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>E</SHORT-NAME><ELEMENTS>`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<ECU-INSTANCE><SHORT-NAME>e%d</SHORT-NAME><CONNECTORS><CAN-COMMUNICATION-CONNECTOR><SHORT-NAME>c</SHORT-NAME><ECU-COMM-PORT-INSTANCES><FRAME-PORT><SHORT-NAME>fp</SHORT-NAME><COMMUNICATION-DIRECTION>OUT</COMMUNICATION-DIRECTION></FRAME-PORT></ECU-COMM-PORT-INSTANCES></CAN-COMMUNICATION-CONNECTOR></CONNECTORS></ECU-INSTANCE>`, i)
	}
	b.WriteString(`<CAN-FRAME-TRIGGERING><SHORT-NAME>t</SHORT-NAME><FRAME-REF>/E/f</FRAME-REF><IDENTIFIER>1</IDENTIFIER><FRAME-PORT-REFS><FRAME-PORT-REF>/E/e63999/c/fp</FRAME-PORT-REF></FRAME-PORT-REFS></CAN-FRAME-TRIGGERING>`)
	b.WriteString(`<CAN-FRAME><SHORT-NAME>f</SHORT-NAME><FRAME-LENGTH>8</FRAME-LENGTH></CAN-FRAME>`)
	b.WriteString(`</ELEMENTS></AR-PACKAGE></AR-PACKAGES></AUTOSAR>`)
	db, diags, err := parseWithin(t, b.String(), 20*time.Second)
	if err != nil {
		t.Fatalf("%v %v", err, diags)
	}
	if m := db.Message(1, false); m == nil || len(m.Senders) != 1 || m.Senders[0] != "e63999" {
		t.Fatalf("message %+v", m)
	}
}

// TestReferenceAndPositionBounds: relative references do not resolve,
// positions beyond the model's bit range are rejected, and a nested
// ECU-INSTANCE owns its frame ports.
func TestReferenceAndPositionBounds(t *testing.T) {
	doc := fanDoc(1, 1, 1, "IDENTICAL", "")
	for name, c := range map[string]struct{ old, new, want string }{
		"relative reference": {"<FRAME-REF>/F/f", "<FRAME-REF>F/f", "reference F/f does not resolve"},
		"PDU offset beyond":  {"<START-POSITION>0</START-POSITION></PDU-TO-FRAME-MAPPING>", "<START-POSITION>33554432</START-POSITION></PDU-TO-FRAME-MAPPING>", "PDU at bit 33554432: beyond 16777216"},
		"start beyond":       {"<START-POSITION>0</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>", "<START-POSITION>16777217</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>", `bad START-POSITION "16777217"`},
		"start minus":        {"<START-POSITION>0</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>", "<START-POSITION>-1</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>", `bad START-POSITION "-1"`},
	} {
		t.Run(name, func(t *testing.T) {
			all, _ := diagText(strings.Replace(doc, c.old, c.new, 1))
			if !strings.Contains(all, c.want) {
				t.Fatalf("missing %q in\n%s", c.want, all)
			}
		})
	}
	nested := `<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>E</SHORT-NAME><ELEMENTS>
<ECU-INSTANCE><SHORT-NAME>outer</SHORT-NAME><FRAME-PORT><SHORT-NAME>p1</SHORT-NAME><COMMUNICATION-DIRECTION>IN</COMMUNICATION-DIRECTION></FRAME-PORT>
 <ECU-INSTANCE><SHORT-NAME>inner</SHORT-NAME><FRAME-PORT><SHORT-NAME>p2</SHORT-NAME><COMMUNICATION-DIRECTION>OUT</COMMUNICATION-DIRECTION></FRAME-PORT></ECU-INSTANCE></ECU-INSTANCE>
<CAN-FRAME-TRIGGERING><SHORT-NAME>t</SHORT-NAME><FRAME-REF>/E/f</FRAME-REF><IDENTIFIER>1</IDENTIFIER><FRAME-PORT-REFS><FRAME-PORT-REF>/E/outer/inner/p2</FRAME-PORT-REF><FRAME-PORT-REF>/E/outer/p1</FRAME-PORT-REF></FRAME-PORT-REFS></CAN-FRAME-TRIGGERING>
<CAN-FRAME><SHORT-NAME>f</SHORT-NAME><FRAME-LENGTH>8</FRAME-LENGTH></CAN-FRAME>
</ELEMENTS></AR-PACKAGE></AR-PACKAGES></AUTOSAR>`
	db, _, err := Parse(strings.NewReader(nested))
	if err != nil {
		t.Fatal(err)
	}
	if m := db.Message(1, false); m == nil || len(m.Senders) != 1 || m.Senders[0] != "inner" || len(db.Nodes) != 2 {
		t.Fatalf("message %+v, nodes %d", m, len(db.Nodes))
	}
}
