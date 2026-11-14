// SPDX-License-Identifier: MIT

package kcd

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
)

// FuzzRoundTrip: a KCD file that parses without errors writes back to
// KCD that parses without errors, with the same messages and signal
// layouts.
func FuzzRoundTrip(f *testing.F) {
	if b, err := os.ReadFile("testdata/sample.kcd"); err == nil {
		f.Add(b)
	}
	f.Add([]byte(`<NetworkDefinition><Bus><Message id="0x1FFFFFFF" format="extended" name="M"><Signal name="S" offset="0" length="64" endianess="big"><Value type="signed"/></Signal></Message></Bus></NetworkDefinition>`))
	f.Fuzz(func(t *testing.T, b []byte) {
		db, diags, err := Parse(bytes.NewReader(b))
		if err != nil || candb.HasErrors(diags) {
			return
		}
		var buf bytes.Buffer
		if _, err := Write(&buf, db); err != nil {
			return // the database holds something KCD cannot carry
		}
		back, d2, err := Parse(&buf)
		if err != nil || candb.HasErrors(d2) {
			t.Fatalf("written KCD does not parse: %v %v\n%s", err, d2, buf.String())
		}
		if len(back.Messages) != len(db.Messages) {
			t.Fatalf("%d messages, then %d", len(db.Messages), len(back.Messages))
		}
		for _, m := range db.Messages {
			o := back.Message(m.ID, m.Extended)
			if o == nil || o.Length != m.Length || len(o.Signals) != len(m.Signals) {
				t.Fatalf("message %s: %+v", m.Name, o)
			}
			for _, s := range m.Signals {
				r := o.Signal(s.Name)
				if r == nil || r.StartBit != s.StartBit || r.Length != s.Length || r.ByteOrder != s.ByteOrder || r.ValueType != s.ValueType {
					t.Fatalf("signal %s.%s: %+v, was %+v", m.Name, s.Name, r, s)
				}
			}
		}
	})
}

// TestHostileKCD: oversized, deeply nested and out-of-range documents end
// in an error or diagnostics, never a panic.
func TestHostileKCD(t *testing.T) {
	msg := func(attrs, sig string) string {
		return `<NetworkDefinition><Bus><Message ` + attrs + `>` + sig + `</Message></Bus></NetworkDefinition>`
	}
	cases := map[string]string{
		"deep nesting":       "<NetworkDefinition>" + strings.Repeat("<X>", 100000) + strings.Repeat("</X>", 100000) + "</NetworkDefinition>",
		"unclosed":           "<NetworkDefinition>" + strings.Repeat("<Bus>", 100000),
		"huge name":          msg(`id="1" name="`+strings.Repeat("a", 1<<20)+`"`, ""),
		"id beyond 32 bits":  msg(`id="0x100000000" name="M"`, ""),
		"id negative":        msg(`id="-1" name="M"`, ""),
		"id beyond 29 bits":  msg(`id="0x20000000" format="extended" name="M"`, ""),
		"length negative":    msg(`id="1" name="M" length="-1"`, `<Signal name="S" offset="0" length="8"/>`),
		"length huge":        msg(`id="1" name="M" length="9223372036854775807"`, `<Signal name="S" offset="0" length="8"/>`),
		"signal offset huge": msg(`id="1" name="M"`, `<Signal name="S" offset="9223372036854775807" length="8"/>`),
		"signal offset max":  msg(`id="1" name="M" length="8"`, `<Signal name="S" offset="9223372036854775807" length="8" endianess="big"/>`),
		"signal length 0":    msg(`id="1" name="M"`, `<Signal name="S" offset="0" length="0"/>`),
		"signal length 65":   msg(`id="1" name="M"`, `<Signal name="S" offset="0" length="65"/>`),
		"signal length neg":  msg(`id="1" name="M"`, `<Signal name="S" offset="0" length="-8"/>`),
		"mux count huge":     msg(`id="1" name="M"`, `<Multiplex name="X" offset="0" length="4"><MuxGroup count="18446744073709551615"><Signal name="S" offset="8" length="8"/></MuxGroup></Multiplex>`),
		"label huge":         msg(`id="1" name="M"`, `<Signal name="S" offset="0" length="8"><LabelSet><Label name="a" value="9223372036854775808"/></LabelSet></Signal>`),
		"entity":             `<!DOCTYPE a [<!ENTITY x "xxxxxxxxxx">]><NetworkDefinition>&x;&x;</NetworkDefinition>`,
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
				if _, err := m.EncodeWith(nil, candb.EncodeOptions{FillMissing: true}); err != nil && m.Length >= 0 && m.Length <= 64 {
					t.Logf("%s: encode: %v", m.Name, err)
				}
			}
		})
	}
}

// TestLabelGroupBounds: LabelGroup widths are computed without int64
// wrap-around, groups at the int64 limits terminate, and groups wider
// than 256 values are not expanded.
func TestLabelGroupBounds(t *testing.T) {
	cases := []struct {
		from, to string
		values   int
		warn     string
	}{
		{"9223372036854775807", "9223372036854775807", 1, ""},
		{"9223372036854775552", "9223372036854775807", 256, ""},
		{"-9223372036854775808", "-9223372036854775808", 1, ""},
		{"-9223372036854775808", "9223372036854775807", 0, "(18446744073709551616 values) not expanded"},
		{"-4611686018427387904", "4611686018427387905", 0, "(9223372036854775810 values) not expanded"},
		{"0", "255", 256, ""},
		{"0", "256", 0, "(257 values) not expanded"},
	}
	for _, c := range cases {
		t.Run(c.from+".."+c.to, func(t *testing.T) {
			src := `<NetworkDefinition><Bus><Message id="1" name="M"><Signal name="S" offset="0" length="64"><Value type="signed"/><LabelSet><LabelGroup name="g" from="` +
				c.from + `" to="` + c.to + `"/></LabelSet></Signal></Message></Bus></NetworkDefinition>`
			db, diags, err := Parse(strings.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			if n := len(db.Messages[0].Signals[0].Values); n != c.values {
				t.Fatalf("%d values, want %d", n, c.values)
			}
			if c.warn != "" && !strings.Contains(fmt.Sprint(diags), c.warn) {
				t.Fatalf("diagnostics %v, want %q", diags, c.warn)
			}
		})
	}
}

// TestLabelGroupDatabaseBound: LabelGroup expansion stops at
// maxExpandedLabelValues per database.
func TestLabelGroupDatabaseBound(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<NetworkDefinition><Bus><Message id="1" name="M"><Signal name="S" offset="0" length="64"><LabelSet>`)
	groups := maxExpandedLabelValues/maxLabelGroupValues + 1
	for i := 0; i < groups; i++ {
		fmt.Fprintf(&b, `<LabelGroup name="g%d" from="%d" to="%d"/>`, i, i*256, i*256+255)
	}
	b.WriteString(`</LabelSet></Signal></Message></Bus></NetworkDefinition>`)
	db, diags, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(db.Messages[0].Signals[0].Values); n != maxExpandedLabelValues {
		t.Fatalf("%d values, want %d", n, maxExpandedLabelValues)
	}
	if !strings.Contains(fmt.Sprint(diags), "more than 1048576 LabelGroup values") {
		t.Fatalf("diagnostics %v", diags)
	}
}
