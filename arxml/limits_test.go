// SPDX-License-Identifier: MIT

package arxml

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// repeated is a reader that never ends: its text, over and over.
type repeated struct {
	text string
	at   int
}

func (r *repeated) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.text[r.at]
		r.at = (r.at + 1) % len(r.text)
	}
	return len(p), nil
}

func refusedAs(err error, text string) bool {
	var pe *ParseError
	return errors.As(err, &pe) && strings.Contains(err.Error(), text)
}

// frameDoc returns a document with one frame of the given FRAME-LENGTH
// and n one-bit signals, all at bit 0.
func frameDoc(length string, n int) string {
	var b strings.Builder
	b.WriteString(`<AUTOSAR xmlns="http://autosar.org/schema/r4.0"><AR-PACKAGES><AR-PACKAGE><SHORT-NAME>F</SHORT-NAME><ELEMENTS>
<CAN-FRAME-TRIGGERING><SHORT-NAME>t</SHORT-NAME><FRAME-REF>/F/f</FRAME-REF><IDENTIFIER>1</IDENTIFIER></CAN-FRAME-TRIGGERING>
<CAN-FRAME><SHORT-NAME>f</SHORT-NAME><FRAME-LENGTH>` + length + `</FRAME-LENGTH><PDU-TO-FRAME-MAPPINGS><PDU-TO-FRAME-MAPPING><PDU-REF>/F/p</PDU-REF><START-POSITION>0</START-POSITION></PDU-TO-FRAME-MAPPING></PDU-TO-FRAME-MAPPINGS></CAN-FRAME>
<I-SIGNAL-I-PDU><SHORT-NAME>p</SHORT-NAME><I-SIGNAL-TO-PDU-MAPPINGS>`)
	for i := range n {
		fmt.Fprintf(&b, "<I-SIGNAL-TO-I-PDU-MAPPING><I-SIGNAL-REF>/F/s%d</I-SIGNAL-REF><START-POSITION>0</START-POSITION></I-SIGNAL-TO-I-PDU-MAPPING>\n", i)
	}
	b.WriteString(`</I-SIGNAL-TO-PDU-MAPPINGS></I-SIGNAL-I-PDU>`)
	for i := range n {
		fmt.Fprintf(&b, "<I-SIGNAL><SHORT-NAME>s%d</SHORT-NAME><LENGTH>1</LENGTH></I-SIGNAL>\n", i)
	}
	b.WriteString(`</ELEMENTS></AR-PACKAGE></AR-PACKAGES></AUTOSAR>`)
	return b.String()
}

// TestDocumentSizeBound: a document up to the bound is parsed, a document
// of one byte more is a *ParseError; a reader is read up to the bound and
// one byte, however much it holds.
func TestDocumentSizeBound(t *testing.T) {
	if maxInputSize != 1<<30 {
		t.Fatalf("bound %d: SECURITY.md and the package documentation say 1 GiB", maxInputSize)
	}
	doc := frameDoc("8", 1)
	n := int64(len(doc))
	for _, c := range []struct {
		limit int64
		ok    bool
	}{{n + 1, true}, {n, true}, {n - 1, false}, {1, false}, {0, false}} {
		db, _, err := parse(strings.NewReader(doc), c.limit)
		if c.ok != (err == nil) || c.ok != (len(db.Messages) == 1) || !c.ok && !refusedAs(err, "document larger than "+strconv.FormatInt(c.limit, 10)+" bytes") {
			t.Errorf("bound %d: %d messages, %v", c.limit, len(db.Messages), err)
		}
	}
	// An empty reader is not a document, whatever the bound.
	if _, _, err := parse(strings.NewReader(""), 0); !refusedAs(err, "not an AUTOSAR document") {
		t.Errorf("empty reader, bound 0: %v", err)
	}
	// A reader without end costs what a document of the bound costs.
	const limit = 1 << 16
	endless := io.MultiReader(strings.NewReader("<AUTOSAR>"), &repeated{text: "<a/>"})
	if got := allocBytesOf(func() {
		if _, _, err := parse(endless, limit); !refusedAs(err, "document larger than 65536 bytes") {
			t.Errorf("reader without end: %v", err)
		}
	}); got > 512*limit {
		t.Errorf("reader without end, bound %d: %d bytes allocated", limit, got)
	}
}

// allocBytesOf returns the bytes allocated while f runs.
func allocBytesOf(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestFrameLengthBound: a FRAME-LENGTH no frame carries is a *ParseError:
// more than 64 bytes, or a negative length.
func TestFrameLengthBound(t *testing.T) {
	for _, c := range []struct {
		length string
		ok     bool
	}{
		{"0", true}, {"8", true}, {"9", true}, {"63", true}, {"64", true},
		{"65", false}, {"70", false}, {"2147483647", false}, {"2147483648", false}, {"9223372036854775807", false},
		{"-1", false}, {"-2147483648", false},
	} {
		db, diags, err := Parse(strings.NewReader(frameDoc(c.length, 0)))
		var pe *ParseError
		if c.ok != (err == nil) || !c.ok && !errors.As(err, &pe) {
			t.Errorf("FRAME-LENGTH %s: %v, %v", c.length, diags, err)
		}
		if c.ok {
			if _, err := db.Messages[0].Decode(make([]byte, 64)); err != nil {
				t.Errorf("FRAME-LENGTH %s: %v", c.length, err)
			}
		}
	}
}

// TestSharedBitSignals: the overlap check of a frame whose signals all
// share a bit ends at the bound of candb.Database.Validate, with a
// warning; the document still parses.
func TestSharedBitSignals(t *testing.T) {
	const n = 6000 // n(n-1)/2 pairs: more than the 2^24 steps of the check
	db, diags, err := Parse(strings.NewReader(frameDoc("8", n)))
	if err != nil || len(db.Messages[0].Signals) != n {
		t.Fatalf("%d signals, %v", len(db.Messages[0].Signals), err)
	}
	last := diags[len(diags)-1]
	if len(diags) != 66 || !strings.HasPrefix(last.Message, "overlap check ended after 16777216 steps") {
		t.Fatalf("%d diagnostics, last %v", len(diags), last)
	}
}
