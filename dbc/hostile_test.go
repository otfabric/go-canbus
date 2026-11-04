// SPDX-License-Identifier: MIT

package dbc

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
)

// TestMessageIDOutOfRange: a message reference beyond 32 bits is a syntax
// error in every statement that names a message. Before, statements other
// than BO_ truncated it to 32 bits, so 4294967297 attached a comment,
// value table, value type or attribute to message 1.
func TestMessageIDOutOfRange(t *testing.T) {
	head := "BU_: A\nBO_ 1 M: 8 A\n SG_ S : 0|8@1+ (1,0) [0|0] \"\" A\n"
	const big = "4294967297" // 1<<32 + 1
	cases := map[string]string{
		"BO_":          "BO_ " + big + " M2: 8 A\n",
		"BO_TX_BU_":    "BO_TX_BU_ " + big + " : A;\n",
		"CM_ BO_":      "CM_ BO_ " + big + " \"wrapped\";\n",
		"CM_ SG_":      "CM_ SG_ " + big + " S \"wrapped\";\n",
		"VAL_":         "VAL_ " + big + " S 1 \"wrapped\" ;\n",
		"SIG_VALTYPE_": "SIG_VALTYPE_ " + big + " S : 1;\n",
		"SG_MUL_VAL_":  "SG_MUL_VAL_ " + big + " S S 1-1;\n",
		"SIG_GROUP_":   "SIG_GROUP_ " + big + " G 1 : S;\n",
		"BA_ BO_":      "BA_DEF_ BO_ \"X\" INT 0 10;\nBA_ \"X\" BO_ " + big + " 5;\n",
		"BA_ SG_":      "BA_DEF_ SG_ \"X\" INT 0 10;\nBA_ \"X\" SG_ " + big + " S 5;\n",
	}
	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			src := head + stmt
			db, _, err := ParseBytes([]byte(src))
			if err == nil {
				t.Fatal("identifier beyond 32 bits accepted")
			}
			if all := allDiagnostics(src); !strings.Contains(all, "message identifier 4294967297 out of range") {
				t.Fatalf("diagnostics:\n%s", all)
			}
			m := db.MessageByName("M")
			if m == nil {
				t.Fatal("message before the bad statement lost")
			}
			s := m.Signal("S")
			if m.Comment != "" || s.Comment != "" || len(s.Values) != 0 || s.ValueType != candb.Integer || len(m.Attributes) != 0 || len(s.Attributes) != 0 {
				t.Fatalf("statement wrapped onto message 1: %+v %+v", m, s)
			}
		})
	}
	// The largest 32-bit identifier still parses (extended, bits 29..30 clear).
	db, _, err := ParseBytes([]byte("BO_ 2684354559 M: 8 A\nCM_ BO_ 2684354559 \"ok\";\n"))
	if err != nil || db.Messages[0].Comment != "ok" || !db.Messages[0].Extended || db.Messages[0].ID != 0x1FFFFFFF {
		t.Fatal(err, db.Messages)
	}
}

// TestIntegerBounds: signed integers cover exactly the int64 range.
func TestIntegerBounds(t *testing.T) {
	head := "BO_ 1 M: 8 A\n SG_ S : 0|64@1- (1,0) [0|0] \"\" A\n"
	for _, c := range []struct {
		text string
		want int64
		ok   bool
	}{
		{"-9223372036854775808", math.MinInt64, true},
		{"-9223372036854775807", math.MinInt64 + 1, true},
		{"9223372036854775807", math.MaxInt64, true},
		{"0x7FFFFFFFFFFFFFFF", math.MaxInt64, true},
		{"-0x8000000000000000", math.MinInt64, true},
		{"9223372036854775808", 0, false},
		{"-9223372036854775809", 0, false},
		{"-0x8000000000000001", 0, false},
		{"18446744073709551615", 0, false},
		{"1e3", 1000, true},
		{"-5.0", -5, true},
		{"1e19", 0, false},
	} {
		t.Run(c.text, func(t *testing.T) {
			db, _, err := ParseBytes([]byte(head + "VAL_ 1 S " + c.text + " \"v\" ;\n"))
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok = %v", err, c.ok)
			}
			if !c.ok {
				return
			}
			vs := db.Messages[0].Signal("S").Values
			if len(vs) != 1 || vs[0].Value != c.want {
				t.Fatalf("values %+v, want %d", vs, c.want)
			}
		})
	}
}

// TestSigValTypeRange: only 0, 1 and 2 are value types.
func TestSigValTypeRange(t *testing.T) {
	head := "BO_ 1 M: 8 A\n SG_ S : 0|32@1+ (1,0) [0|0] \"\" A\n"
	for _, c := range []struct {
		vt   string
		want candb.ValueType
		bad  bool
	}{
		{"0", candb.Integer, false},
		{"1", candb.Float32, false},
		{"2", candb.Float64, false},
		{"3", candb.Integer, true},
		{"-1", candb.Integer, true},
		{"256", candb.Integer, true},
		{"9223372036854775807", candb.Integer, true},
	} {
		src := head + "SIG_VALTYPE_ 1 S : " + c.vt + ";\n"
		db, _, _ := ParseBytes([]byte(src))
		if got := db.Messages[0].Signal("S").ValueType; got != c.want {
			t.Errorf("SIG_VALTYPE_ %s: type %v, want %v", c.vt, got, c.want)
		}
		if has := strings.Contains(allDiagnostics(src), "unknown (0 integer"); has != c.bad {
			t.Errorf("SIG_VALTYPE_ %s: diagnostic %v, want %v", c.vt, has, c.bad)
		}
	}
}

// TestHostileInput: oversized and degenerate inputs end in diagnostics,
// never a panic or a hang.
func TestHostileInput(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	cases := map[string]string{
		"very long identifier":   "BO_ 1 " + long + ": 8 A\n",
		"very long string":       "BO_ 1 M: 8 A\nCM_ BO_ 1 \"" + long + "\";\n",
		"unterminated string":    "BO_ 1 M: 8 A\nCM_ BO_ 1 \"" + long,
		"very long number":       "BO_ " + strings.Repeat("9", 1<<16) + " M: 8 A\n",
		"many signals":           "BO_ 1 M: 8 A\n" + strings.Repeat(" SG_ S : 0|8@1+ (1,0) [0|0] \"\" A\n", 10000),
		"negative length":        "BO_ 1 M: -8 A\n",
		"huge length":            "BO_ 1 M: 9223372036854775807 A\n SG_ S : 0|8@1+ (1,0) [0|0] \"\" A\n",
		"bit length 0":           "BO_ 1 M: 8 A\n SG_ S : 0|0@1+ (1,0) [0|0] \"\" A\n",
		"bit length 65":          "BO_ 1 M: 16 A\n SG_ S : 0|65@1+ (1,0) [0|0] \"\" A\n",
		"bit length huge":        "BO_ 1 M: 8 A\n SG_ S : 0|9223372036854775807@1+ (1,0) [0|0] \"\" A\n",
		"start bit huge":         "BO_ 1 M: 8 A\n SG_ S : 9223372036854775807|8@1+ (1,0) [0|0] \"\" A\n",
		"start bit negative":     "BO_ 1 M: 8 A\n SG_ S : -1|8@0+ (1,0) [0|0] \"\" A\n",
		"mux range reversed":     "BO_ 1 M: 8 A\n SG_ S M : 0|8@1+ (1,0) [0|0] \"\" A\n SG_ T m1 : 8|8@1+ (1,0) [0|0] \"\" A\nSG_MUL_VAL_ 1 T S 5-1;\n",
		"mux range huge":         "BO_ 1 M: 8 A\n SG_ S M : 0|8@1+ (1,0) [0|0] \"\" A\n SG_ T m1 : 8|8@1+ (1,0) [0|0] \"\" A\nSG_MUL_VAL_ 1 T S 0-18446744073709551615;\n",
		"NUL bytes":              "BO_ 1 M\x00: 8 A\x00\n",
		"invalid UTF-8":          "BO_ 1 \xff\xfe: 8 A\n",
		"only punctuation":       strings.Repeat(":;|@()[],", 10000),
		"deep value description": "VAL_TABLE_ T " + strings.Repeat("1 \"a\" ", 100000) + ";\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			db, _, err := ParseBytes([]byte(src))
			if err != nil {
				return
			}
			// A database that parses must also write and decode.
			var buf bytes.Buffer
			if err := Write(&buf, db); err != nil {
				t.Fatal(err)
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
