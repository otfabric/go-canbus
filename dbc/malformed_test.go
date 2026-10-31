// SPDX-License-Identifier: MIT

package dbc

import (
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
)

// allDiagnostics joins the diagnostics of src, whatever its verdict.
func allDiagnostics(src string) string {
	_, diags, _ := ParseBytes([]byte(src))
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.String() + "\n")
	}
	return b.String()
}

// Malformed statements are reported at their line, as an error or a
// warning, and the statements after them still parse.
func TestMalformedStatements(t *testing.T) {
	head := "BU_: A\nBO_ 1 M: 8 A\n SG_ S : 0|8@1+ (1,0) [0|0] \"\" A\n"
	cases := []struct {
		name, stmt string
		fatal      bool
		want       string
	}{
		{"value not a number", "VAL_ x 1 2;\n", true, "expected"},
		{"message id not a number", "BO_ x M2: 8 A\n", true, "expected an unsigned integer"},
		{"message id too big", "BO_ 99999999999999999999 M2: 8 A\n", true, "bad unsigned integer"},
		{"integer too big", "VAL_ 1 S 99999999999999999999 \"x\";\n", true, "bad integer"},
		{"integer not a number", "VAL_ 1 S - x \"y\";\n", true, "expected an integer"},
		{"name is a number", "BO_ 2 5: 8 A\n", true, "expected"},
		{"string is a number", "VAL_ 1 S 1 2;\n", true, "expected"},
		{"missing semicolon", "BA_ \"A\" 1 2\n", true, "expected"},
		{"attribute object unknown", "BA_ \"A\" XX_ 1 2;\n", true, "unknown attribute object"},
		{"attribute value is a keyword", "BA_ \"A\" BO_ 1 :;\n", true, "expected an attribute value"},
		{"negative string", "BA_ \"A\" -\"x\";\n", true, "expected a number after '-'"},
		{"environment attribute", "BA_ \"A\" EV_ Env 1;\n", false, "environment variables ignored"},
		{"environment values", "VAL_ Env 1 \"x\" ;\n", false, "environment variable Env ignored"},
		{"values of an unknown message", "VAL_ 99 S 1 \"x\" ;\n", false, "unknown message 99"},
		{"default of an undefined attribute", "BA_DEF_DEF_ \"Nope\" 1;\n", false, "undefined attribute Nope"},
		{"default of the wrong type", "BA_DEF_ \"I\" INT 0 10;\nBA_DEF_DEF_ \"I\" \"text\";\n", false, "does not match its type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := head + c.stmt + "BO_ 7 After: 8 A\n"
			db, _, err := ParseBytes([]byte(src))
			if (err != nil) != c.fatal {
				t.Fatalf("err = %v, want fatal = %v", err, c.fatal)
			}
			if all := allDiagnostics(src); !strings.Contains(all, c.want) {
				t.Fatalf("no %q in\n%s", c.want, all)
			}
			if db.MessageByName("M") == nil {
				t.Fatal("statement before the bad one lost")
			}
		})
	}
}

func TestMultiplexIndicators(t *testing.T) {
	for s, want := range map[string]bool{
		"M": true, "m0": true, "m12": true, "m3M": true,
		"m": false, "mM": false, "x1": false, "mxM": false, "": false,
	} {
		if got := validMuxIndicator(s); got != want {
			t.Errorf("validMuxIndicator(%q) = %v, want %v", s, got, want)
		}
	}
}

// Identifiers are decimal: a leading zero does not make one octal, as
// integers elsewhere in the file.
func TestIdentifierWithLeadingZero(t *testing.T) {
	db, _, err := ParseBytes([]byte("BU_: A\nBO_ 010 M: 8 A\nVAL_ 010 Q 1 \"x\" ;\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m := db.MessageByName("M"); m == nil || m.ID != 10 {
		t.Fatalf("message = %+v, want identifier 10", m)
	}
}

func TestHexadecimalIdentifier(t *testing.T) {
	db, _, err := ParseBytes([]byte("BU_: A\nBO_ 0x10 M: 8 A\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m := db.MessageByName("M"); m == nil || m.ID != 16 {
		t.Fatalf("message = %+v, want identifier 16", m)
	}
}

func TestIntegersWrittenAsFloats(t *testing.T) {
	for _, v := range []struct {
		text string
		want int64
	}{{"5.0", 5}, {"1e3", 1000}, {"0x10", 16}, {"-5.0", -5}} {
		src := "BU_: A\nBO_ 1 M: 8 A\n SG_ S : 0|8@1+ (1,0) [0|0] \"\" A\nVAL_ 1 S " + v.text + " \"x\" ;\n"
		db, _, err := ParseBytes([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", v.text, err)
		}
		vs := db.MessageByName("M").Signal("S").Values
		if len(vs) != 1 || vs[0].Value != v.want {
			t.Fatalf("%s: values = %+v, want %d", v.text, vs, v.want)
		}
	}
}

// Attribute defaults of every type reach the objects that do not set the
// attribute, and survive a write and a parse.
func TestAttributeDefaultsAllTypes(t *testing.T) {
	src := `BU_: A
BO_ 1 M: 8 A
BA_DEF_ BO_ "I" INT 0 100;
BA_DEF_ BO_ "F" FLOAT 0 10;
BA_DEF_ BO_ "T" STRING ;
BA_DEF_ BO_ "E" ENUM "off","on";
BA_DEF_DEF_ "I" 42;
BA_DEF_DEF_ "F" 2.5;
BA_DEF_DEF_ "T" "text";
BA_DEF_DEF_ "E" "on";
`
	db, diags, err := ParseBytes([]byte(src))
	if err != nil {
		t.Fatalf("%v\n%v", err, diags)
	}
	check := func(db *candb.Database) {
		t.Helper()
		for name, ok := range map[string]func(d *candb.AttributeDefinition) bool{
			"I": func(d *candb.AttributeDefinition) bool { return d.Default.Int == 42 },
			"F": func(d *candb.AttributeDefinition) bool { return d.Default.Float == 2.5 },
			"T": func(d *candb.AttributeDefinition) bool { return d.Default.String == "text" },
			"E": func(d *candb.AttributeDefinition) bool { return d.Default.Int == 1 },
		} {
			d := db.AttributeDefinition(name, candb.ObjectMessage)
			if d == nil || d.Default == nil || !ok(d) {
				t.Errorf("attribute %s: %+v", name, d)
			}
		}
	}
	check(db)
	var buf strings.Builder
	if err := Write(&buf, db); err != nil {
		t.Fatal(err)
	}
	back, _, err := ParseBytes([]byte(buf.String()))
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	check(back)
}
