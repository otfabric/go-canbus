// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/arxml"
	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/dbc"
	"github.com/otfabric/go-canbus/kcd"
)

// dbcFixtures are canbus-interop's DBC fixtures and the oracle features
// each needs.
var dbcFixtures = []struct {
	name  string
	needs string
}{
	{"basic", ""}, {"wide", ""}, {"attributes", ""}, {"float", "dbcFloat"},
	{"mux", "dbcMultiplexing"}, {"extmux", "dbcExtendedMultiplexing"}, {"fd", "dbcFD"},
}

// knownDifference documents a disagreement between go-canbus and an
// oracle that is a deliberate choice, not a defect. A matching mismatch
// is recorded as "known-difference", never as a pass.
type knownDifference struct {
	adapter, fixture, contains, reason string
}

var knownDifferences = []knownDifference{
	{"cantools", "pdu-offset", "", "cantools places a PDU mapped at a byte offset of its frame at offset 0; go-canbus applies the PDU-TO-FRAME-MAPPING START-POSITION (AUTOSAR)"},
	{"canmatrix", "pdu-offset", "", "canmatrix places a PDU mapped at a byte offset of its frame at offset 0; go-canbus applies the PDU-TO-FRAME-MAPPING START-POSITION (AUTOSAR)"},
	{"canmatrix", "extmux", "EncodingComplexMultiplexed",
		"canmatrix 1.3.3 decodes extended multiplexing (SG_MUL_VAL_) but cannot encode it; go-canbus's encoding is checked against cantools"},
	{"canmatrix", "multiplexed", "", "canmatrix 1.3.3 reads only the selector of a MULTIPLEXED-I-PDU (named Multiplexor) and none of its signals; go-canbus's reading is checked against cantools"},
	{"cantools", "", "expected multiplexer id",
		"cantools rejects a multiplexer value that selects no multiplexed signal; go-canbus (like canmatrix) decodes the switch and the signals that are always present"},
	{"cantools", "", "A valid value for the multiplexer selector signal",
		"cantools rejects a multiplexer value that selects no multiplexed signal; go-canbus (like canmatrix) encodes the switch and the signals that are always present"},
	{"canmatrix", "", "payload longer than the message",
		"canmatrix rejects payloads longer than the message length; go-canbus (like cantools) decodes the first Length bytes, which CAN FD frames padded to a valid FD length need"},
}

func known(adapter, fixture, detail string) *knownDifference {
	for i := range knownDifferences {
		k := &knownDifferences[i]
		if k.adapter == adapter && (k.fixture == "" || k.fixture == fixture) && strings.Contains(detail, k.contains) {
			return k
		}
	}
	return nil
}

type stackSignal struct {
	Raw      string          `json:"raw"`
	Physical json.RawMessage `json:"physical"`
	Label    string          `json:"label"`
}

type stackDecoded struct {
	Type     string                 `json:"type"`
	ID       uint32                 `json:"id"`
	Extended bool                   `json:"extended"`
	Message  string                 `json:"message"`
	Signals  map[string]stackSignal `json:"signals"`
	Error    string                 `json:"error"`
	Data     string                 `json:"data"`
}

func parseStack(t *testing.T, out []byte) []stackDecoded {
	t.Helper()
	var docs []stackDecoded
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var d stackDecoded
		if err := json.Unmarshal(line, &d); err != nil {
			t.Fatalf("bad line %q", line)
		}
		docs = append(docs, d)
	}
	return docs
}

func physicalOf(raw json.RawMessage) float64 {
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	switch s {
	case "nan":
		return math.NaN()
	case "inf":
		return math.Inf(1)
	case "-inf":
		return math.Inf(-1)
	}
	return math.NaN()
}

func samePhysical(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(a))
}

// differ compares one decoded frame; it returns "" when they agree.
func differ(m *candb.Message, sigs []candb.DecodedSignal, err error, s stackDecoded) string {
	if s.Type == "decode-error" || err != nil {
		switch {
		case s.Type == "decode-error" && err != nil:
			return ""
		case err != nil:
			return fmt.Sprintf("go-canbus fails (%v), the stack decodes", err)
		default:
			if m != nil && strings.Contains(strings.ToLower(s.Error), "length") {
				return "the stack rejects a payload longer than the message: " + s.Error
			}
			return "the stack fails (" + s.Error + "), go-canbus decodes"
		}
	}
	if s.Message != m.Name {
		return fmt.Sprintf("message %s vs %s", m.Name, s.Message)
	}
	var diffs []string
	seen := map[string]bool{}
	for _, d := range sigs {
		seen[d.Signal.Name] = true
		ss, ok := s.Signals[d.Signal.Name]
		if !ok {
			diffs = append(diffs, d.Signal.Name+" active only in go-canbus")
			continue
		}
		raw := d.Signal.RawString(d.Value)
		if d.Signal.ValueType != candb.Integer {
			raw = strconv.FormatUint(d.Raw, 10)
		}
		if raw != ss.Raw {
			diffs = append(diffs, fmt.Sprintf("%s raw %s vs %s", d.Signal.Name, raw, ss.Raw))
		}
		if p := physicalOf(ss.Physical); !samePhysical(d.Physical, p) {
			diffs = append(diffs, fmt.Sprintf("%s physical %v vs %v", d.Signal.Name, d.Physical, p))
		}
		if d.Label != ss.Label {
			diffs = append(diffs, fmt.Sprintf("%s label %q vs %q", d.Signal.Name, d.Label, ss.Label))
		}
	}
	for name := range s.Signals {
		if !seen[name] {
			diffs = append(diffs, name+" active only in the stack")
		}
	}
	sort.Strings(diffs)
	return strings.Join(diffs, "; ")
}

func report(t *testing.T, adapter, fixture string, diffs []string) {
	t.Helper()
	failed := false
	for _, d := range diffs {
		if k := known(adapter, fixture, d); k != nil {
			t.Logf("KNOWN DIFFERENCE: %s (%s)", d, k.reason)
			record(t, adapter, fixture+": "+d, "known-difference", k.reason)
			continue
		}
		t.Errorf("%s", d)
		failed = true
	}
	_ = failed
}

func parseDB(t *testing.T, name string) *candb.Database { return parseKind(t, "dbc", name) }

// parseKind parses fixtures/<kind>/<name>.<kind> with go-canbus.
func parseKind(t *testing.T, kind, name string) *candb.Database {
	t.Helper()
	src := fixture(t, kind+"/"+name+"."+kind)
	var db *candb.Database
	var diags []candb.Diagnostic
	var err error
	switch kind {
	case "arxml":
		db, diags, err = arxml.Parse(bytes.NewReader(src))
	default:
		db, diags, err = dbc.ParseBytes(src)
	}
	if err != nil {
		t.Fatalf("%v %v", err, diags)
	}
	for _, d := range diags {
		t.Logf("diagnostic: %s", d)
	}
	return db
}

// TestDBCDecode decodes every fixture frame with go-canbus and each oracle
// and compares raw values (exactly), physical values, labels and the set
// of active (multiplexed) signals.
func TestDBCDecode(t *testing.T) {
	for _, a := range dbAdapters {
		for _, fx := range dbcFixtures {
			scenario(t, a, "decode "+fx.name, func(t *testing.T) {
				c := caps(t, a)
				if fx.needs != "" && !c.Features[fx.needs] {
					skipf(t, "%s lacks %s", a, fx.needs)
				}
				decodeDiff(t, a, "dbc", fx.name)
			})
		}
	}
}

// decodeDiff runs one decode comparison of fixtures/<kind>/<name>.
func decodeDiff(t *testing.T, a, kind, name string) {
	t.Helper()
	db := parseKind(t, kind, name)
	src := fixture(t, kind+"/"+name+".frames.jsonl")
	in := frames(t, src)
	out, code := run(t, a, src, "", "decode", "--db", "/fixtures/"+kind+"/"+name+"."+kind, "--frames", "-")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	docs := parseStack(t, out)
	if len(docs) != len(in) {
		t.Fatalf("%d results for %d frames", len(docs), len(in))
	}
	var diffs []string
	for i := range in {
		m, sigs, err := db.DecodeFrame(&in[i])
		if d := differ(m, sigs, err, docs[i]); d != "" {
			diffs = append(diffs, fmt.Sprintf("frame %d (%s): %s", i, in[i].String(), d))
		}
	}
	report(t, a, name, diffs)
}

// TestDBCEncode encodes the fixture's physical values and, with raw
// values, every decodable fixture frame, with go-canbus and each oracle,
// and compares the bytes.
func TestDBCEncode(t *testing.T) {
	for _, a := range dbAdapters {
		for _, fx := range dbcFixtures {
			scenario(t, a, "encode "+fx.name, func(t *testing.T) {
				c := caps(t, a)
				if fx.needs != "" && !c.Features[fx.needs] {
					skipf(t, "%s lacks %s", a, fx.needs)
				}
				db := parseDB(t, fx.name)
				src := fixture(t, "dbc/"+fx.name+".messages.jsonl")
				type msgIn struct {
					Message string                 `json:"message"`
					Signals map[string]json.Number `json:"signals"`
				}
				var inputs []msgIn
				for _, line := range bytes.Split(src, []byte("\n")) {
					line = bytes.TrimSpace(line)
					if len(line) == 0 || line[0] == '#' {
						continue
					}
					var in msgIn
					dec := json.NewDecoder(bytes.NewReader(line))
					dec.UseNumber() // 64-bit integers stay exact
					if err := dec.Decode(&in); err != nil {
						t.Fatal(err)
					}
					inputs = append(inputs, in)
				}
				out, code := run(t, a, src, "", "encode", "--db", "/fixtures/dbc/"+fx.name+".dbc", "--messages", "-")
				if code != 0 {
					t.Fatalf("exit %d: %s", code, out)
				}
				docs := parseStack(t, out)
				if len(docs) != len(inputs) {
					t.Fatalf("%d results for %d messages", len(docs), len(inputs))
				}
				var diffs []string
				for i, in := range inputs {
					m := db.MessageByName(in.Message)
					values := map[string]candb.Input{}
					for k, v := range in.Signals {
						values[k] = candb.PhysText(v.String())
					}
					data, err := m.EncodeWith(values, candb.EncodeOptions{Lax: true})
					if d := encodeDiff(data, err, docs[i]); d != "" {
						diffs = append(diffs, fmt.Sprintf("message %d (%s): %s", i, in.Message, d))
					}
				}
				// Raw values of every frame go-canbus decodes.
				if c.Features["encodeRaw"] {
					var rawIn bytes.Buffer
					var want [][]byte
					var wantErr []error
					for _, f := range frames(t, fixture(t, "dbc/"+fx.name+".frames.jsonl")) {
						m, sigs, err := db.DecodeFrame(&f)
						if err != nil || len(f.Payload()) != m.Length || hasFloat(m) {
							// Float signals: the oracles take --raw values of
							// float signals as numbers, not bit patterns
							// (canbus-interop contract question), so they
							// are compared through physical values only.
							continue
						}
						line := map[string]any{"message": m.Name}
						sv := map[string]string{}
						values := map[string]candb.Input{}
						for _, s := range sigs {
							sv[s.Signal.Name] = rawText(s)
							values[s.Signal.Name] = candb.Raw(s.Raw)
						}
						line["signals"] = sv
						j, _ := json.Marshal(line)
						rawIn.Write(append(j, '\n'))
						data, err := m.EncodeWith(values, candb.EncodeOptions{Lax: true})
						want = append(want, data)
						wantErr = append(wantErr, err)
					}
					out, code := run(t, a, rawIn.Bytes(), "", "encode", "--raw", "--db", "/fixtures/dbc/"+fx.name+".dbc", "--messages", "-")
					if code != 0 {
						t.Fatalf("raw encode exit %d: %s", code, out)
					}
					docs := parseStack(t, out)
					for i := range want {
						if d := encodeDiff(want[i], wantErr[i], docs[i]); d != "" {
							diffs = append(diffs, fmt.Sprintf("raw %d: %s", i, d))
						}
					}
				}
				report(t, a, fx.name, diffs)
			})
		}
	}
}

func rawText(s candb.DecodedSignal) string {
	if s.Signal.ValueType == candb.Integer && s.Signal.Signed {
		return strconv.FormatInt(s.Signed, 10)
	}
	return strconv.FormatUint(s.Raw, 10)
}

func encodeDiff(data []byte, err error, s stackDecoded) string {
	switch {
	case s.Type == "encode-error" && err != nil:
		return ""
	case s.Type == "encode-error":
		return "the stack fails (" + s.Error + "), go-canbus encodes " + hex.EncodeToString(data)
	case err != nil:
		return fmt.Sprintf("go-canbus fails (%v), the stack encodes %s", err, s.Data)
	}
	// Compare the message's bytes; a stack may or may not pad a CAN FD
	// payload to the next valid length.
	got, _ := hex.DecodeString(s.Data)
	n := min(len(got), len(data))
	if !bytes.Equal(got[:n], data[:n]) || bytes.Count(got[n:], []byte{0}) != len(got)-n || bytes.Count(data[n:], []byte{0}) != len(data)-n {
		return fmt.Sprintf("bytes %x vs %s", data, s.Data)
	}
	return ""
}

// TestKCD converts the KCD fixture with canmatrix and compares the
// layout canmatrix derives with go-canbus's KCD parser, then decodes the
// KCD fixture frames with both models.
func TestKCD(t *testing.T) {
	scenario(t, "canmatrix", "kcd layout", func(t *testing.T) {
		if !caps(t, "canmatrix").has("convert") {
			skipf(t, "canmatrix has no convert")
		}
		work := workDir(t)
		writeFile(t, work, "basic.kcd", fixture(t, "kcd/basic.kcd"))
		out, code := run(t, "canmatrix", nil, work, "convert", "--input", "/work/basic.kcd", "--output", "/work/basic.dbc")
		if code != 0 {
			t.Fatalf("convert exit %d: %s", code, out)
		}
		conv, _, err := dbc.ParseFile(work + "/basic.dbc")
		if err != nil {
			t.Fatal(err)
		}
		mine, diags, err := kcd.Parse(bytes.NewReader(fixture(t, "kcd/basic.kcd")))
		if err != nil {
			t.Fatal(err, diags)
		}
		for _, d := range diags {
			t.Logf("diagnostic: %s", d)
		}
		var diffs []string
		for _, m := range mine.Messages {
			o := conv.Message(m.ID, m.Extended)
			if o == nil {
				diffs = append(diffs, "message "+m.Name+" missing in canmatrix's conversion")
				continue
			}
			for _, s := range m.Signals {
				os := o.Signal(s.Name)
				if os == nil {
					diffs = append(diffs, "signal "+m.Name+"."+s.Name+" missing")
					continue
				}
				if s.StartBit != os.StartBit || s.Length != os.Length || s.ByteOrder != os.ByteOrder || s.Signed != os.Signed ||
					s.ValueType != os.ValueType || s.Factor != os.Factor || s.Offset != os.Offset || s.IsMultiplexer != os.IsMultiplexer ||
					s.Multiplexer != os.Multiplexer {
					diffs = append(diffs, fmt.Sprintf("signal %s.%s: %+v vs %+v", m.Name, s.Name, *s, *os))
				}
			}
		}
		// Both models decode the fixture frames identically.
		for _, f := range frames(t, fixture(t, "kcd/basic.frames.jsonl")) {
			m1, s1, e1 := mine.DecodeFrame(&f)
			_, s2, e2 := conv.DecodeFrame(&f)
			if (e1 == nil) != (e2 == nil) {
				diffs = append(diffs, fmt.Sprintf("%s: %v vs %v", f.String(), e1, e2))
				continue
			}
			byName := map[string]uint64{}
			for _, d := range s2 {
				byName[d.Signal.Name] = d.Raw
			}
			for _, d := range s1 {
				if r, ok := byName[d.Signal.Name]; !ok || r != d.Raw {
					diffs = append(diffs, fmt.Sprintf("%s %s.%s: %d vs %d (present %v)", f.String(), m1.Name, d.Signal.Name, d.Raw, r, ok))
				}
			}
			if len(s1) != len(s2) {
				diffs = append(diffs, fmt.Sprintf("%s: %d vs %d active signals", f.String(), len(s1), len(s2)))
			}
		}
		report(t, "canmatrix", "kcd", diffs)
	})
}

var _ = canbus.ID(0)

func hasFloat(m *candb.Message) bool {
	for _, s := range m.Signals {
		if s.ValueType != candb.Integer {
			return true
		}
	}
	return false
}
