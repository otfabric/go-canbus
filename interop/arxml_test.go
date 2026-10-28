// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/dbc"
	"github.com/otfabric/go-canbus/kcd"
)

var arxmlFixtures = []string{"basic", "fd", "pdu-offset", "multiplexed"}

// TestARXMLDecode compares go-canbus's ARXML reader with cantools and
// canmatrix, which decode the same ARXML files (contract 1.1).
func TestARXMLDecode(t *testing.T) {
	for _, a := range dbAdapters {
		for _, name := range arxmlFixtures {
			scenario(t, a, "arxml decode "+name, func(t *testing.T) {
				if !caps(t, a).Features["arxml"] {
					skipf(t, "%s has no ARXML (contract 1.1)", a)
				}
				decodeDiff(t, a, "arxml", name)
			})
		}
	}
}

// TestARXMLEncode encodes the fixtures' physical values with go-canbus and
// each oracle and compares the bytes.
func TestARXMLEncode(t *testing.T) {
	for _, a := range dbAdapters {
		for _, name := range arxmlFixtures {
			scenario(t, a, "arxml encode "+name, func(t *testing.T) {
				if !caps(t, a).Features["arxml"] {
					skipf(t, "%s has no ARXML (contract 1.1)", a)
				}
				db := parseKind(t, "arxml", name)
				src := fixture(t, "arxml/"+name+".messages.jsonl")
				out, code := run(t, a, src, "", "encode", "--db", "/fixtures/arxml/"+name+".arxml", "--messages", "-")
				if code != 0 {
					t.Fatalf("exit %d: %s", code, out)
				}
				docs := parseStack(t, out)
				var diffs []string
				i := 0
				for _, line := range bytes.Split(src, []byte("\n")) {
					line = bytes.TrimSpace(line)
					if len(line) == 0 || line[0] == '#' {
						continue
					}
					var in struct {
						Message string                 `json:"message"`
						Signals map[string]json.Number `json:"signals"`
					}
					dec := json.NewDecoder(bytes.NewReader(line))
					dec.UseNumber()
					if err := dec.Decode(&in); err != nil {
						t.Fatal(err)
					}
					m := db.MessageByName(in.Message)
					if m == nil {
						t.Fatalf("no message %s", in.Message)
					}
					values := map[string]candb.Input{}
					for k, v := range in.Signals {
						values[k] = candb.PhysText(v.String())
					}
					data, err := m.EncodeWith(values, candb.EncodeOptions{Lax: true})
					if i >= len(docs) {
						t.Fatalf("%d results", len(docs))
					}
					if d := encodeDiff(data, err, docs[i]); d != "" {
						diffs = append(diffs, fmt.Sprintf("message %d (%s): %s", i, in.Message, d))
					}
					i++
				}
				report(t, a, name, diffs)
			})
		}
	}
}

// TestKCDWrite: go-canbus writes the DBC fixtures as KCD, canmatrix
// converts that KCD to DBC, and the layout canmatrix read must equal the
// one go-canbus wrote.
func TestKCDWrite(t *testing.T) {
	for _, name := range []string{"basic", "wide", "float", "mux", "fd"} {
		scenario(t, "canmatrix", "kcd write "+name, func(t *testing.T) {
			if !caps(t, "canmatrix").has("convert") {
				skipf(t, "canmatrix has no convert")
			}
			db := parseKind(t, "dbc", name)
			var b bytes.Buffer
			if _, err := kcd.Write(&b, db); err != nil {
				t.Fatal(err)
			}
			work := workDir(t)
			writeFile(t, work, name+".kcd", b.Bytes())
			out, code := run(t, "canmatrix", nil, work, "convert", "--input", "/work/"+name+".kcd", "--output", "/work/"+name+".dbc")
			if code != 0 {
				t.Fatalf("convert exit %d: %s", code, out)
			}
			conv, _, err := dbc.ParseFile(work + "/" + name + ".dbc")
			if err != nil {
				t.Fatal(err)
			}
			var diffs []string
			for _, m := range db.Messages {
				o := conv.Message(m.ID, m.Extended)
				if o == nil {
					diffs = append(diffs, "message "+m.Name+" missing")
					continue
				}
				for _, s := range m.Signals {
					os := o.Signal(s.Name)
					if os == nil || s.StartBit != os.StartBit || s.Length != os.Length || s.ByteOrder != os.ByteOrder ||
						s.ValueType != os.ValueType || s.ValueType == candb.Integer && s.Signed != os.Signed ||
						s.Factor != os.Factor || s.Offset != os.Offset || s.IsMultiplexer != os.IsMultiplexer || s.Multiplexer != os.Multiplexer {
						diffs = append(diffs, fmt.Sprintf("signal %s.%s: wrote %+v, canmatrix read %+v", m.Name, s.Name, s, os))
					}
				}
			}
			report(t, "canmatrix", name, diffs)
		})
	}
}
