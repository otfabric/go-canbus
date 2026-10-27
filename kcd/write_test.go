// SPDX-License-Identifier: MIT

package kcd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/dbc"
)

// TestWriteRoundTrip writes the DBC fixture's representable messages as
// KCD and reads them back: every layout field must survive.
func TestWriteRoundTrip(t *testing.T) {
	src, _, err := dbc.ParseFile("../dbc/testdata/full.dbc")
	if err != nil {
		t.Fatal(err)
	}
	// ExtMux uses extended multiplexing, which KCD cannot carry.
	var keep []*candb.Message
	for _, m := range src.Messages {
		if m.Name != "ExtMux" {
			keep = append(keep, m)
		}
	}
	db := *src
	db.Messages = keep
	var buf bytes.Buffer
	diags, err := Write(&buf, &db)
	if err != nil {
		t.Fatal(err)
	}
	if !candb.HasErrors(diags) && len(diags) == 0 {
		t.Fatal("dropped attributes not reported")
	}
	written := buf.String()
	back, d2, err := Parse(&buf)
	if err != nil {
		t.Fatal(err, d2, written)
	}
	for _, m := range db.Messages {
		b := back.Message(m.ID, m.Extended)
		if b == nil || b.Name != m.Name || b.Length != m.Length || b.CycleTime != m.CycleTime || b.Comment != m.Comment {
			t.Fatalf("message %s: %+v", m.Name, b)
		}
		for _, s := range m.Signals {
			o := b.Signal(s.Name)
			if o == nil || o.StartBit != s.StartBit || o.Length != s.Length || o.ByteOrder != s.ByteOrder || s.ValueType == candb.Integer && o.Signed != s.Signed ||
				o.ValueType != s.ValueType || o.Factor != s.Factor || o.Offset != s.Offset || o.Minimum != s.Minimum || o.Maximum != s.Maximum ||
				o.Unit != s.Unit || o.IsMultiplexer != s.IsMultiplexer || o.Multiplexer != s.Multiplexer || len(o.Values) != len(s.Values) ||
				len(o.Receivers) != len(s.Receivers) || o.Comment != s.Comment {
				t.Errorf("signal %s.%s:\n%+v\n%+v", m.Name, s.Name, o, s)
			}
		}
	}
	if !strings.Contains(written, `xmlns="http://kayak.2codeornot2code.org/1.0"`) {
		t.Fatal("namespace missing")
	}
	// Extended multiplexing and mux ranges are errors.
	if _, err := Write(&bytes.Buffer{}, src); err == nil || !strings.Contains(err.Error(), "extended multiplexing") {
		t.Fatal(err)
	}
	ranged := &candb.Database{Messages: []*candb.Message{{Name: "M", Length: 8, Signals: []*candb.Signal{
		{Name: "Sw", Length: 4, Factor: 1, IsMultiplexer: true},
		{Name: "R", StartBit: 8, Length: 8, Factor: 1, Multiplexer: "Sw", MuxValues: []candb.MuxRange{{Low: 1, High: 3}}},
	}}}}
	if _, err := Write(&bytes.Buffer{}, ranged); err == nil {
		t.Fatal("range accepted")
	}
}
