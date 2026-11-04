// SPDX-License-Identifier: MIT

package arxml

import (
	"os"
	"strings"
	"testing"
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
