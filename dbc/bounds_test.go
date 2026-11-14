// SPDX-License-Identifier: MIT

package dbc

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus/candb"
)

// TestMappedAttributeIndexBounds: VFrameFormat and CANFD_BRS defined as
// INT or HEX carry any integer, including negative ones; ENUM indexes
// are compared in int64. Before, -1 indexed the enum labels and the
// parser panicked.
func TestMappedAttributeIndexBounds(t *testing.T) {
	for name, src := range map[string]string{
		"INT value":      "BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"VFrameFormat\" INT -10 10;\nBA_ \"VFrameFormat\" BO_ 1 -1;\n",
		"HEX value":      "BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"CANFD_BRS\" HEX -10 10;\nBA_ \"CANFD_BRS\" BO_ 1 -1;\n",
		"INT default":    "BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"VFrameFormat\" INT -10 10;\nBA_DEF_DEF_ \"VFrameFormat\" -1;\n",
		"INT beyond":     "BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"VFrameFormat\" INT 0 10;\nBA_ \"VFrameFormat\" BO_ 1 4294967310;\n",
		"BRS large":      "BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"CANFD_BRS\" INT 0 10;\nBA_ \"CANFD_BRS\" BO_ 1 9223372036854775807;\n",
		"ENUM 2^32":      "BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"VFrameFormat\" ENUM \"StandardCAN\",\"StandardCAN_FD\";\nBA_ \"VFrameFormat\" BO_ 1 4294967296;\n",
		"ENUM 2^32 text": "BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"VFrameFormat\" ENUM \"StandardCAN\",\"StandardCAN_FD\";\nBA_ \"VFrameFormat\" BO_ 1 \"4294967297\";\n",
	} {
		t.Run(name, func(t *testing.T) {
			db, _, _ := ParseBytes([]byte(src))
			m := db.MessageByName("M")
			if m == nil || m.FD || m.BRS {
				t.Fatalf("%+v", m)
			}
		})
	}
	// Valid indexes still map.
	db, _, err := ParseBytes([]byte("BU_: A\nBO_ 1 M: 8 A\nBA_DEF_ BO_ \"VFrameFormat\" ENUM \"StandardCAN\",\"StandardCAN_FD\";\nBA_DEF_ BO_ \"CANFD_BRS\" ENUM \"0\",\"1\";\nBA_ \"VFrameFormat\" BO_ 1 1;\nBA_ \"CANFD_BRS\" BO_ 1 \"1\";\n"))
	if err != nil || !db.Messages[0].FD || !db.Messages[0].BRS {
		t.Fatal(err, db.Messages[0])
	}
	if a, ok := (&parser{}).convert(&candb.AttributeDefinition{Type: candb.AttrEnum, Enum: []string{"a"}}, token{kind: tNumber, text: "4294967296"}); ok {
		t.Fatalf("index 2^32 accepted: %+v", a)
	}
}

// TestDuplicateOfSkippedMessage: a second BO_ with the identifier of a
// valid message is reported as a duplicate, whether or not it is itself
// valid, and the first message keeps receiving the statements that name
// the identifier. Before, an invalid duplicate hid the valid message and
// applying an attribute default to it panicked.
func TestDuplicateOfSkippedMessage(t *testing.T) {
	defs := "BA_DEF_ BO_ \"GenMsgCycleTime\" INT 0 1000;\nBA_DEF_DEF_ \"GenMsgCycleTime\" 10;\n"
	for name, src := range map[string]string{
		"invalid after valid": "BU_: A\nBO_ 1 M: 8 A\nBO_ 1 M2: 99 A\n" + defs + "CM_ BO_ 1 \"c\";\n",
		"valid after invalid": "BU_: A\nBO_ 1 M2: 99 A\nBO_ 1 M: 8 A\n" + defs + "CM_ BO_ 1 \"c\";\n",
		"bits 29..30":         "BU_: A\nBO_ 1 M: 8 A\nBO_ 1610612737 M2: 8 A\n" + defs + "CM_ BO_ 1 \"c\";\n",
	} {
		t.Run(name, func(t *testing.T) {
			db, _, err := ParseBytes([]byte(src))
			if err == nil {
				t.Fatal("invalid message accepted")
			}
			m := db.MessageByName("M")
			if len(db.Messages) != 1 || m == nil || m.CycleTime != 10 || len(m.Attributes) != 0 || m.Comment != "c" {
				t.Fatalf("%+v", db.Messages)
			}
		})
	}
	if all := allDiagnostics("BO_ 1 M: 8 A\nBO_ 1 M2: 99 A\n"); !strings.Contains(all, "duplicate message identifier 1") || !strings.Contains(all, "length 99 out of range") {
		t.Fatal(all)
	}
}

// TestWriteRejectsNonIdentifiers: names written as DBC identifiers must
// be identifiers. Before, a KCD message named "M: 8 X\n\nBO_ 2047 Other"
// was written verbatim and read back as two messages.
func TestWriteRejectsNonIdentifiers(t *testing.T) {
	const bad = "M: 8 X\n\nBO_ 2047 Other"
	base := func() *candb.Database {
		return &candb.Database{
			Nodes:       []*candb.Node{{Name: "N"}},
			ValueTables: []candb.ValueTable{{Name: "VT"}},
			Messages: []*candb.Message{{Name: "M", ID: 1, Length: 8, Senders: []string{"N", "O"},
				Signals: []*candb.Signal{
					{Name: "Sw", Length: 4, Factor: 1, IsMultiplexer: true, Receivers: []string{"N"}},
					{Name: "S", StartBit: 8, Length: 8, Factor: 1, Multiplexer: "Sw", MuxValues: []candb.MuxRange{{Low: 1, High: 2}}},
				},
				SignalGroups: []candb.SignalGroup{{Name: "G", Repetitions: 1, Signals: []string{"S"}}},
			}},
		}
	}
	var buf bytes.Buffer
	if err := Write(&buf, base()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseBytes(buf.Bytes()); err != nil {
		t.Fatal(err, buf.String())
	}
	for name, set := range map[string]func(db *candb.Database, v string){
		"node":         func(db *candb.Database, v string) { db.Nodes[0].Name = v },
		"value table":  func(db *candb.Database, v string) { db.ValueTables[0].Name = v },
		"message":      func(db *candb.Database, v string) { db.Messages[0].Name = v },
		"first sender": func(db *candb.Database, v string) { db.Messages[0].Senders[0] = v },
		"other sender": func(db *candb.Database, v string) { db.Messages[0].Senders[1] = v },
		"signal":       func(db *candb.Database, v string) { db.Messages[0].Signals[1].Name = v },
		"receiver":     func(db *candb.Database, v string) { db.Messages[0].Signals[0].Receivers[0] = v },
		"multiplexer":  func(db *candb.Database, v string) { db.Messages[0].Signals[1].Multiplexer = v },
		"group":        func(db *candb.Database, v string) { db.Messages[0].SignalGroups[0].Name = v },
		"group signal": func(db *candb.Database, v string) { db.Messages[0].SignalGroups[0].Signals[0] = v },
	} {
		for _, v := range []string{bad, "", "1st", "0x10", "a b", "a\"b", "a;b", "a-b"} {
			if v == "" && name == "multiplexer" {
				continue // no multiplexer: nothing to write
			}
			db := base()
			set(db, v)
			var out bytes.Buffer
			err := Write(&out, db)
			if !errors.Is(err, ErrName) || out.Len() != 0 {
				t.Errorf("%s %q: %v, %d bytes written", name, v, err, out.Len())
			}
		}
	}
	// Keywords end the BU_ and receiver lists; elsewhere they read back.
	for _, kw := range []string{"BO_", "SG_", "CM_"} {
		db := base()
		db.Nodes[0].Name = kw
		if err := Write(&bytes.Buffer{}, db); !errors.Is(err, ErrName) {
			t.Errorf("node %s: %v", kw, err)
		}
		db = base()
		db.Messages[0].Signals[0].Receivers[0] = kw
		if err := Write(&bytes.Buffer{}, db); !errors.Is(err, ErrName) {
			t.Errorf("receiver %s: %v", kw, err)
		}
	}
	db := base()
	db.Messages[0].Name, db.Messages[0].Signals[1].Name = "CM_", "Grüße"
	db.Messages[0].SignalGroups[0].Signals[0] = "Grüße"
	buf.Reset()
	if err := Write(&buf, db); err != nil {
		t.Fatal(err)
	}
	again, _, err := ParseBytes(buf.Bytes())
	if err != nil || again.Messages[0].Name != "CM_" || again.Messages[0].Signals[1].Name != "Grüße" {
		t.Fatal(err, buf.String())
	}
}

// parseWithin parses src and fails when it takes longer than budget. The
// budgets are far above the linear cost and far below the quadratic one
// the inputs needed before.
func parseWithin(t *testing.T, src string, budget time.Duration) (*candb.Database, []candb.Diagnostic) {
	t.Helper()
	start := time.Now()
	db, diags, _ := ParseBytes([]byte(src))
	if d := time.Since(start); d > budget {
		t.Fatalf("parsing %d bytes took %v (budget %v)", len(src), d, budget)
	}
	return db, diags
}

// TestParseLinearGrowth: inputs that drove quadratic work in the parser
// parse in time linear in their size. Before, each took 10 s or more:
// 40000 messages (each message's key was searched in all messages), 80000
// line-0 diagnostics (insertion sort), 128000 BU_ names, 160000
// BO_TX_BU_ statements and 70000 node and definition attributes (linear
// lookups per name). Now each takes well under a second.
func TestParseLinearGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("large inputs")
	}
	const budget = 5 * time.Second
	var b strings.Builder

	t.Run("messages", func(t *testing.T) {
		b.Reset()
		for i := range 40000 {
			b.WriteString("BO_ " + strconv.Itoa(i) + " M" + strconv.Itoa(i) + ": 8 A\n")
		}
		if db, _ := parseWithin(t, b.String(), budget); len(db.Messages) != 40000 {
			t.Fatal(len(db.Messages))
		}
	})
	t.Run("diagnostics", func(t *testing.T) {
		b.Reset()
		for i := range 80000 {
			b.WriteString("FILTER ;\nSIG_VALTYPE_ " + strconv.Itoa(i) + " S : 1;\n")
		}
		_, diags := parseWithin(t, b.String(), budget)
		if len(diags) != 160000 {
			t.Fatal(len(diags))
		}
		for i := 1; i < len(diags); i++ {
			if diags[i].Line < diags[i-1].Line {
				t.Fatal("diagnostics not sorted by line")
			}
		}
	})
	t.Run("nodes", func(t *testing.T) {
		b.Reset()
		b.WriteString("BU_:")
		for i := range 128000 {
			b.WriteString(" N" + strconv.Itoa(i))
		}
		b.WriteString(" N0 N1\n")
		if db, _ := parseWithin(t, b.String(), budget); len(db.Nodes) != 128000 {
			t.Fatal(len(db.Nodes))
		}
	})
	t.Run("senders", func(t *testing.T) {
		b.Reset()
		b.WriteString("BO_ 1 M: 8 A\n")
		for i := range 160000 {
			b.WriteString("BO_TX_BU_ 1 : A,S" + strconv.Itoa(i) + ";\n")
		}
		if db, _ := parseWithin(t, b.String(), budget); len(db.Messages[0].Senders) != 160001 {
			t.Fatal(len(db.Messages[0].Senders))
		}
	})
	t.Run("attributes", func(t *testing.T) {
		const n = 70000
		b.Reset()
		b.WriteString("BU_:")
		for i := range n {
			b.WriteString(" N" + strconv.Itoa(i))
		}
		b.WriteString("\n")
		for i := range n {
			b.WriteString("BA_DEF_ BU_ \"A" + strconv.Itoa(i) + "\" INT 0 10;\nBA_DEF_DEF_ \"A" + strconv.Itoa(i) + "\" 1;\n")
		}
		for i := range n {
			b.WriteString("BA_ \"A" + strconv.Itoa(i) + "\" BU_ N" + strconv.Itoa(i) + " 2;\n")
		}
		db, _ := parseWithin(t, b.String(), budget)
		if a := db.Nodes[n-1].Attributes; len(a) != 1 || a[0].Int != 2 || db.AttributeDefinitions[n-1].Default.Int != 1 {
			t.Fatal(a)
		}
	})
	// Signal lookups by name go through the parser's index.
	t.Run("signals", func(t *testing.T) {
		const n = 2000
		b.Reset()
		b.WriteString("BO_ 1 M: 64 A\n")
		for i := range n {
			b.WriteString(" SG_ S" + strconv.Itoa(i) + " : " + strconv.Itoa(i%512) + "|1@1+ (1,0) [0|0] \"\" A\n")
		}
		for i := range n {
			b.WriteString("VAL_ 1 S" + strconv.Itoa(i) + " 1 \"on\" ;\nCM_ SG_ 1 S" + strconv.Itoa(i) + " \"c\";\n")
		}
		db, _ := parseWithin(t, b.String(), budget)
		if s := db.Messages[0].Signals[n-1]; s.Comment != "c" || len(s.Values) != 1 {
			t.Fatal(s)
		}
	})
}

// TestIndexedLookupsReportUnknown: lookups through the parser's indexes
// still report statements naming unknown nodes and signals.
func TestIndexedLookupsReportUnknown(t *testing.T) {
	src := "BU_: A\nBO_ 1 M: 8 A\n SG_ S : 0|8@1+ (1,0) [0|0] \"\" A\n" +
		"BA_DEF_ BU_ \"NA\" INT 0 10;\nBA_DEF_ SG_ \"SA\" INT 0 10;\n" +
		"CM_ BU_ B \"c\";\nCM_ SG_ 1 T \"c\";\nBA_ \"NA\" BU_ B 1;\nBA_ \"SA\" SG_ 1 T 1;\n" +
		"VAL_ 1 T 1 \"x\" ;\nSIG_VALTYPE_ 1 T : 1;\nSG_MUL_VAL_ 1 T S 1-1;\n"
	all := allDiagnostics(src)
	for _, want := range []string{
		"comment for unknown node B ignored", "comment for unknown signal M.T ignored", "attribute NA of unknown node B ignored",
		"attribute SA of unknown signal M.T ignored", "value descriptions for unknown signal M.T ignored",
		"SIG_VALTYPE_ for unknown signal 1 T ignored", "SG_MUL_VAL_ for unknown signal 1 T ignored",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
}
