// SPDX-License-Identifier: MIT

package codegen

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/dbc"
	"github.com/otfabric/go-canbus/kcd"
)

// strayTexts end a // comment or a string literal, or are not valid in Go
// source, each followed by Go code that must never become code.
var strayTexts = []string{
	"\n}\nfunc init() { panic(\"x\") }\n//",
	"\r\nfunc init() { panic(\"x\") }\r\n//",
	"\u2028func init() { panic(\"x\") }\u2029//",
	"\", ErrRange, func() any { panic(\"x\") }(), \"",
	"`\n}\nfunc init() { panic(\"x\") }\n//`",
	"%v%s%d%w%%",
	"*/ /* */",
	"\x00\x7f\x85\ufeff\xff\xfe",
}

// checkGenerated parses generated source and requires that only the
// generator's own declarations exist: no init function, no panic call,
// the standard imports, fmt.Errorf formats from the generator, and unique
// top-level and field names.
func checkGenerated(t *testing.T, src []byte) {
	t.Helper()
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "gen.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("generated code does not parse: %v", err)
	}
	var imports []string
	for _, im := range f.Imports {
		imports = append(imports, im.Path.Value)
	}
	if got := strings.Join(imports, " "); got != `"errors" "fmt" "math" "github.com/otfabric/go-canbus" "github.com/otfabric/go-canbus/candb/bits"` {
		t.Fatalf("imports %s", got)
	}
	top := map[string]bool{}
	methods := map[string]bool{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv == nil {
				if name != "b2u" && name != "New" && name != "Decode" {
					t.Fatalf("unexpected function %s", name)
				}
				continue
			}
			recv := d.Recv.List[0].Type
			if st, ok := recv.(*ast.StarExpr); ok {
				recv = st.X
			}
			key := recv.(*ast.Ident).Name + "." + name
			if methods[key] {
				t.Fatalf("duplicate method %s", key)
			}
			methods[key] = true
			switch {
			case name == "MessageID", name == "MessageName", name == "Encode", name == "Decode", name == "Frame", name == "String":
			case strings.HasSuffix(name, "Phys"):
			default:
				t.Fatalf("unexpected method %s", key)
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if n.Name != "_" && top[n.Name] {
							t.Fatalf("duplicate %s", n.Name)
						}
						top[n.Name] = true
					}
				case *ast.TypeSpec:
					if top[sp.Name.Name] {
						t.Fatalf("duplicate %s", sp.Name.Name)
					}
					top[sp.Name.Name] = true
					if st, ok := sp.Type.(*ast.StructType); ok {
						fields := map[string]bool{}
						for _, fl := range st.Fields.List {
							for _, n := range fl.Names {
								if fields[n.Name] {
									t.Fatalf("duplicate field %s.%s", sp.Name.Name, n.Name)
								}
								fields[n.Name] = true
							}
						}
					}
				default:
				}
			}
		}
	}
	formats := map[string]bool{`"%w: %s: %v"`: true, `"%w: %s: %d"`: true, `"%w: %s"`: true}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "panic" || fn.Name == "println" {
				t.Fatalf("stray call to %s at %v", fn.Name, fset.Position(call.Pos()))
			}
		case *ast.SelectorExpr:
			if x, ok := fn.X.(*ast.Ident); ok && x.Name == "fmt" && fn.Sel.Name == "Errorf" {
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || !formats[lit.Value] && !strings.HasPrefix(lit.Value, `"%w: %s needs `) {
					t.Fatalf("unexpected fmt.Errorf format at %v", fset.Position(call.Pos()))
				}
			}
		case *ast.FuncLit:
			t.Fatalf("stray function literal call at %v", fset.Position(call.Pos()))
		default:
		}
		return true
	})
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.ContainsAny(c.Text, "\r\u2028\u2029\x00\ufeff") {
				t.Fatalf("line terminator or control character in comment %q", c.Text)
			}
		}
	}
}

// TestGenerateEscapesStrings: every database string the generator embeds
// (message and signal names, units, senders, multiplexer names, comments,
// value descriptions, the database version and the provenance options)
// lands only in comments or string literals. Before, a newline in a unit
// or sender name ended its comment and a '"' in a message name closed an
// fmt.Errorf literal, so the rest compiled as Go code.
func TestGenerateEscapesStrings(t *testing.T) {
	for i, x := range strayTexts {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			sw := "Sw" + x
			db := &candb.Database{Version: "v" + x, Comment: "db" + x, Messages: []*candb.Message{{
				Name: "M" + x, ID: 0x10, Length: 8, Comment: "c" + x + "\nsecond" + x,
				Senders: []string{"N" + x, "O" + x},
				Signals: []*candb.Signal{
					{Name: sw, Length: 4, Factor: 1, IsMultiplexer: true, Unit: "u" + x, Comment: "sc" + x, Minimum: 0, Maximum: 15},
					{Name: "S" + x, StartBit: 8, Length: 12, Signed: true, Factor: 1, Unit: "u" + x, Minimum: -5, Maximum: 5,
						Multiplexer: sw, MuxValues: []candb.MuxRange{{Low: 1, High: 3}},
						Values: []candb.ValueDescription{{Value: 1, Description: "d" + x}, {Value: 2, Description: x}}},
					{Name: "U" + x, StartBit: 24, Length: 7, Factor: 1, Unit: x},
					{Name: "W" + x, StartBit: 0, Length: 64, Factor: 1, ByteOrder: candb.LittleEndian, Multiplexer: sw, MuxValues: []candb.MuxRange{{Low: 9, High: 9}}},
				},
			}}}
			out, err := Generate(db, Options{Package: "p", Source: "src" + x, SourceSHA256: "sha" + x})
			if err != nil {
				t.Fatal(err)
			}
			checkGenerated(t, out)
		})
	}
}

// TestGenerateEscapesParsedStrings: the same through the DBC and KCD
// parsers, whose units, node names and message names carry the text.
func TestGenerateEscapesParsedStrings(t *testing.T) {
	unit := "VERSION \"\"\n\nBU_: N\n\nBO_ 16 M: 1 N\n SG_ S : 0|8@1+ (1,0) [0|0] \"V\nS uint8\nZ string `\n}\nfunc init() { println(\\\"x\\\") }\n//\" N\n"
	db, _, err := dbc.ParseBytes([]byte(unit))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(db, Options{Package: "p"})
	if err != nil {
		t.Fatal(err)
	}
	checkGenerated(t, out)

	for _, k := range []string{
		`<Node id="1" name="A&#10;func init() { println(&quot;x&quot;) }&#10;//"/>`,
		`<Node id="1" name="A&#x2028;func init() { println(&quot;x&quot;) }&#x2029;//"/>`,
	} {
		src := `<?xml version="1.0" encoding="UTF-8"?>
<NetworkDefinition xmlns="http://kayak.2codeornot2code.org/1.0">
  <Document name="t" version="1"/>
  ` + k + `
  <Bus name="Main">
    <Message id="0x10" name="M%v&quot;, ErrLength, func() any { println(&quot;x&quot;); return 0 }(), &quot;" length="8">
      <Producer><NodeRef id="1"/></Producer>
      <Signal name="S" offset="0" length="8"/>
    </Message>
  </Bus>
</NetworkDefinition>`
		db, _, err := kcd.Parse(strings.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		out, err := Generate(db, Options{Package: "p"})
		if err != nil {
			t.Fatal(err)
		}
		checkGenerated(t, out)
	}
}

func TestCommentText(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb":           "a b",
		"a\r\nb":         "a  b",
		"a\u2028b\u2029": "a b ",
		"\x00\x1f\x7f":   "   ",
		"\u0085\ufeff":   "  ",
		"a\xffb":         "a b",
		"*/ `\"% é":      "*/ `\"% é",
	} {
		if got := commentText(in); got != want {
			t.Errorf("commentText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestUniqueNamesLinear: thousands of value descriptions with the same
// label, or signals whose names map to the same identifier, are named in
// linear time with short suffixes. Before, the suffix search restarted at
// 2 for every label (8000 labels took seconds) and colliding signal names
// grew by one underscore each, making the output quadratic.
func TestUniqueNamesLinear(t *testing.T) {
	const k = 8000
	s := &candb.Signal{Name: "E", Length: 32, Factor: 1}
	for i := range k {
		s.Values = append(s.Values, candb.ValueDescription{Value: int64(i), Description: "same"})
	}
	m := &candb.Message{Name: "M", ID: 1, Length: 8, Signals: []*candb.Signal{s}}
	const n = 1500
	fd := &candb.Message{Name: "F", ID: 2, Length: 64, FD: true}
	for i := range n {
		// Distinct names that all map to the identifier "S".
		fd.Signals = append(fd.Signals, &candb.Signal{Name: "S" + string(rune(0x100+i)), StartBit: i % 512, Length: 1, Factor: 1})
	}
	db := &candb.Database{Messages: []*candb.Message{m, fd}}
	var out []byte
	allocs := testing.AllocsPerRun(1, func() {
		var err error
		if out, err = Generate(db, Options{Package: "p"}); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 2_000_000 {
		t.Fatalf("Generate made %.0f allocations", allocs)
	}
	if len(out) > 8<<20 {
		t.Fatalf("generated %d bytes", len(out))
	}
	src := string(out)
	for _, want := range []string{`MESame\s+ME = 0\b`, `MESame2\s+ME = 1\b`, `MESame8000\s+ME = 7999\b`,
		`\tS\s+bool`, `\tS_\s+bool`, `\tS__\s+bool`, `\tS___\s+bool`, `\tS_2\s+bool`, `\tS_1497\s+bool`} {
		if !regexp.MustCompile(want).MatchString(src) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(src, "S____") {
		t.Error("underscore suffixes grow without bound")
	}
	checkGenerated(t, out)

	// A numbered suffix taken by another signal's own name is skipped.
	col := &candb.Message{Name: "C", ID: 3, Length: 8}
	for _, n := range []string{"S_2", "S", "s", "S\u0100", "S\u0101", "S\u0102", "S\u0103"} {
		col.Signals = append(col.Signals, &candb.Signal{Name: n, Length: 1, Factor: 1})
	}
	out, err := Generate(&candb.Database{Messages: []*candb.Message{col}}, Options{Package: "p"})
	if err != nil {
		t.Fatal(err)
	}
	checkGenerated(t, out)
	for _, want := range []string{`\tS_2\s+bool`, `\tS___\s+bool`, `\tS_3\s+bool`, `\tS_4\s+bool`} {
		if !regexp.MustCompile(want).Match(out) {
			t.Errorf("missing %s", want)
		}
	}

	g := &gen{used: map[string]bool{}, next: map[string]int{}}
	for i, want := range []string{"A", "A2", "A3"} {
		if got := g.unique("A"); got != want {
			t.Fatalf("unique #%d = %s, want %s", i, got, want)
		}
	}
	g.used["B2"] = true
	if g.unique("B") != "B" || g.unique("B") != "B3" {
		t.Fatal("unique skips a used suffix")
	}
}
