// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// TestXLRoundTrip writes CAN, CAN FD and CAN XL frames to candump and
// JSON Lines and reads them back with ReadAny; Read skips the XL frames.
func TestXLRoundTrip(t *testing.T) {
	cc, _ := canbus.NewStandard(0x123, []byte{1, 2})
	fd, _ := canbus.NewFD(0x456, false, true, make([]byte, 12))
	x := canbus.XLFrame{Priority: 0x242, VCID: 7, Flags: canbus.XLSEC | canbus.XLRRS, SDT: 3, AF: 0xCAFE, Data: bytes.Repeat([]byte{0x5A}, 1500),
		Timestamp: time.Unix(1700000000, 123456000), Interface: "vcanxl0", Echo: true}
	cc.Timestamp, fd.Timestamp = x.Timestamp, x.Timestamp
	for _, format := range []Format{FormatCandump, FormatJSONL} {
		var buf bytes.Buffer
		w, err := NewWriter(&buf, format)
		if err != nil {
			t.Fatal(err)
		}
		xw, ok := w.(XLWriter)
		if !ok {
			t.Fatalf("%s writer is no XLWriter", format)
		}
		_ = w.Write(&cc)
		_ = xw.WriteXL(&x)
		_ = w.Write(&fd)
		_ = w.Close()
		r, got, err := NewReader(bytes.NewReader(buf.Bytes()))
		if err != nil || got != format {
			t.Fatalf("%s: detected %s, %v", format, got, err)
		}
		xr := r.(XLReader)
		var f canbus.Frame
		var g canbus.XLFrame
		var kinds []string
		for {
			isXL, err := xr.ReadAny(&f, &g)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if isXL {
				// candump logs carry no echo mark when written (as for CAN frames).
				if g.String() != x.String() || g.Interface != x.Interface || g.Echo != (format == FormatJSONL) || !g.Timestamp.Equal(x.Timestamp) {
					t.Fatalf("%s: %.40s… %s echo=%v %v", format, g.String(), g.Interface, g.Echo, g.Timestamp)
				}
				kinds = append(kinds, "xl")
			} else {
				kinds = append(kinds, f.String())
			}
		}
		if strings.Join(kinds, " ") != cc.String()+" xl "+fd.String() {
			t.Fatalf("%s: %v", format, kinds)
		}
		r, _, _ = NewReader(bytes.NewReader(buf.Bytes()))
		var all []string
		for r.Read(&f) == nil {
			all = append(all, f.String())
		}
		if strings.Join(all, " ") != cc.String()+" "+fd.String() {
			t.Fatalf("%s Read: %v", format, all)
		}
		// ReadAny without x reports the XL frame undecoded.
		r, _, _ = NewReader(bytes.NewReader(buf.Bytes()))
		_, _ = r.(XLReader).ReadAny(&f, nil)
		if isXL, err := r.(XLReader).ReadAny(&f, nil); !isXL || err != nil {
			t.Fatalf("%s: %v %v", format, isXL, err)
		}
	}
	// A first line in CAN XL notation is still a candump log.
	if _, got, err := NewReader(strings.NewReader("(1.000000) vcanxl0 00242#80:00:00000000#11\n")); err != nil || got != FormatCandump {
		t.Fatal(got, err)
	}
}

func TestXLMalformed(t *testing.T) {
	var f canbus.Frame
	var x canbus.XLFrame
	for _, in := range []string{
		"(1.0) vcanxl0 00242#80:00:00000000#",
		"(1.0) vcanxl0 00242#80:00:00000000#11 X",
		"1.0 vcanxl0 00242#80:00:00000000#11",
		"(1.x) vcanxl0 00242#80:00:00000000#11",
		"(1.0) 00242#80:00:00000000#11",
	} {
		_, err := NewCandumpReader(strings.NewReader(in)).ReadAny(&f, &x)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%q: %v", in, err)
		}
	}
	if err := ParseCandumpXLLine("(1.0) vcanxl0 00242#80:00:00000000#11 R", &x); err != nil || x.Echo {
		t.Fatal(err)
	}
	for _, in := range []string{
		`{"type":"xlframe","priority":1,"sdt":0,"af":0,"len":2,"data":"11"}`,
		`{"type":"xlframe","priority":4096,"sdt":0,"af":0,"len":1,"data":"11"}`,
		`{"type":"xlframe","priority":1,"sdt":0,"af":0,"len":1,"data":"zz"}`,
		`{"type":"xlframe","priority":1,"sdt":0,"af":0,"len":1,"data":"11","extra":1}`,
		`{"type":"xlframe","priority":2048,"sdt":0,"af":0,"len":1,"data":"11"}`,
		`{"type":"xlframe","timestamp":"x","priority":1,"sdt":0,"af":0,"len":1,"data":"11"}`,
		`{"type":`,
	} {
		_, err := NewJSONLReader(strings.NewReader(in)).ReadAny(&f, &x)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%q: %v", in, err)
		}
	}
}
