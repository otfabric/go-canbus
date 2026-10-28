// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// tshark returns the path of Wireshark's tshark. Without it the Wireshark
// tests skip, unless CANBUS_REQUIRE_TSHARK=1, where they fail.
func tshark(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("tshark")
	if err != nil {
		if os.Getenv("CANBUS_REQUIRE_TSHARK") == "1" {
			t.Fatal("tshark not found")
		}
		t.Skip("tshark not found (install Wireshark; CANBUS_REQUIRE_TSHARK=1 makes this a failure)")
	}
	return p
}

// wiresharkFrames writes a trace in format and returns every packet as
// Wireshark dissects it, rebuilt as frame text ("can0 123#DEADBEEF").
func wiresharkFrames(t *testing.T, format Format, write func(XLWriter)) []string {
	t.Helper()
	bin := tshark(t)
	path := filepath.Join(t.TempDir(), "trace."+string(format))
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(fh, format)
	if err != nil {
		t.Fatal(err)
	}
	write(w.(XLWriter))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	out, err := exec.Command(bin, "-r", path, "-T", "json").Output()
	if err != nil {
		t.Fatalf("tshark: %v", err)
	}
	var packets []struct {
		Source struct {
			Layers map[string]json.RawMessage `json:"layers"`
		} `json:"_source"`
	}
	if err := json.Unmarshal(out, &packets); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range packets {
		got = append(got, dissected(t, p.Source.Layers))
	}
	return got
}

// dissected rebuilds the frame Wireshark decoded from its fields.
func dissected(t *testing.T, layers map[string]json.RawMessage) string {
	t.Helper()
	field := func(layer string) map[string]string {
		m := map[string]string{}
		_ = json.Unmarshal(layers[layer], &m) // a layer given as a plain string has no fields
		return m
	}
	num := func(m map[string]string, k string) uint64 {
		v, ok := m[k]
		if !ok {
			t.Fatalf("Wireshark field %s missing in %v", k, m)
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(v, "0x"), map[bool]int{true: 16, false: 10}[strings.HasPrefix(v, "0x")], 64)
		if err != nil {
			t.Fatalf("field %s=%q: %v", k, v, err)
		}
		return n
	}
	var data []byte
	if d := field("data")["data.data"]; d != "" {
		data, _ = hex.DecodeString(strings.ReplaceAll(d, ":", ""))
	}
	ifc := findString(layers["frame"], "frame.interface_name")
	if xl := field("canxl"); len(xl) > 0 {
		if n := int(num(xl, "canxl.len")); n != len(data) {
			t.Fatalf("canxl.len %d, %d data bytes", n, len(data))
		}
		x := canbus.XLFrame{Priority: uint16(num(xl, "canxl.priority")), VCID: uint8(num(xl, "canxl.vcid")),
			SDT: uint8(num(xl, "canxl.sdu_type")), AF: uint32(num(xl, "canxl.acceptance_field")), Data: data}
		if num(xl, "canxl.flags.sec") == 1 {
			x.Flags |= canbus.XLSEC
		}
		if num(xl, "canxl.flags.rrs") == 1 {
			x.Flags |= canbus.XLRRS
		}
		return ifc + " " + x.String()
	}
	if fd := field("canfd"); len(fd) > 0 {
		f, err := canbus.NewFD(canbus.ID(num(fd, "can.id")), num(fd, "can.flags.xtd") == 1, num(fd, "canfd.flags.brs") == 1, data)
		if err != nil {
			t.Fatal(err)
		}
		if num(fd, "canfd.flags.esi") == 1 {
			f.Flags |= canbus.FlagESI
		}
		return ifc + " " + f.String()
	}
	c := field("can")
	n := int(num(c, "can.len"))
	var f canbus.Frame
	switch {
	case num(c, "can.flags.err") == 1:
		return ifc + " error"
	case num(c, "can.flags.rtr") == 1:
		f, _ = canbus.NewRemote(canbus.ID(num(c, "can.id")), num(c, "can.flags.xtd") == 1, uint8(n))
	case num(c, "can.flags.xtd") == 1:
		f, _ = canbus.NewExtended(canbus.ID(num(c, "can.id")), data[:n])
	default:
		f, _ = canbus.NewStandard(canbus.ID(num(c, "can.id")), data[:n])
	}
	if _, ok := c["can.len8dlc"]; !ok && n == 8 {
		// Older Wireshark (4.2) does not dissect the DLC 9..15 of a
		// classical frame: mark it unknown for sameFrames.
		return ifc + " " + f.String() + noDLC
	}
	if d := num(c, "can.len8dlc"); d != 0 {
		f.Len8DLC = uint8(d)
	}
	return ifc + " " + f.String()
}

// noDLC marks a classical 8-byte frame whose DLC Wireshark did not
// dissect.
const noDLC = " (DLC not dissected)"

// sameFrames compares Wireshark's frames with the expected ones; where
// Wireshark did not dissect the DLC, a DLC 9..15 suffix is not compared.
func sameFrames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		g, w := got[i], want[i]
		if c, ok := strings.CutSuffix(g, noDLC); ok {
			g = c
			if j := strings.LastIndexByte(w, '_'); j > 0 && j == len(w)-2 {
				w = w[:j]
			}
		}
		if g != w {
			return false
		}
	}
	return true
}

// findString returns the value of the first string field named key in a
// JSON tree (tshark nests the interface name under the interface ID).
func findString(raw json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if v, ok := m[key]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		return s
	}
	for _, v := range m {
		if s := findString(v, key); s != "" {
			return s
		}
	}
	return ""
}

// short abbreviates long frame texts in failure messages.
func short(s []string) string {
	out := make([]string, len(s))
	for i, v := range s {
		if len(v) > 80 {
			v = v[:60] + "…(" + strconv.Itoa(len(v)) + " chars)"
		}
		out[i] = v
	}
	return strings.Join(out, "\n")
}

// wiresharkCases are frames of every kind for the Wireshark tests.
func wiresharkCases(t *testing.T) (frames []canbus.Frame, xls []canbus.XLFrame) {
	t.Helper()
	add := func(f canbus.Frame, err error) {
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	add(canbus.NewStandard(0x123, []byte{0xDE, 0xAD, 0xBE, 0xEF}))
	add(canbus.NewExtended(0x1234ABCD, []byte{0x11, 0x22}))
	add(canbus.NewStandard(0x7FF, nil))
	add(canbus.NewRemote(0x123, false, 4))
	add(canbus.NewFD(0x456, false, true, bytes.Repeat([]byte{0xA5}, 64)))
	add(canbus.NewFD(0x1ABCDEF0, true, false, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}))
	dlc, _ := canbus.NewStandard(0x10, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	dlc.Len8DLC = 15
	frames = append(frames, dlc)
	for _, s := range []string{
		"05242#81:01:12345678#AABBCCDDEE",
		"FF7FF#83:FF:FFFFFFFF#00",
		"00000#80:00:00000000#" + strings.Repeat("5A", 2048),
		"0A001#80:07:00000001#0102030405060708090A0B0C0D0E0F10111213",
	} {
		x, err := canbus.ParseXLFrame(s)
		if err != nil {
			t.Fatal(err)
		}
		xls = append(xls, x)
	}
	return frames, xls
}

// TestWiresharkReadsTraces writes pcap and pcapng traces of every frame
// kind and checks that Wireshark dissects each packet into the frame that
// was written, interface names included for pcapng.
func TestWiresharkReadsTraces(t *testing.T) {
	frames, xls := wiresharkCases(t)
	ts := time.Unix(1800000000, 0)
	for _, format := range []Format{FormatPCAP, FormatPCAPNG} {
		var want []string
		got := wiresharkFrames(t, format, func(w XLWriter) {
			for i := range frames {
				f := frames[i]
				f.Interface, f.Timestamp = "can"+strconv.Itoa(i%2), ts.Add(time.Duration(i)*time.Millisecond)
				if err := w.Write(&f); err != nil {
					t.Fatal(err)
				}
				ifc := f.Interface
				if format == FormatPCAP {
					ifc = ""
				}
				want = append(want, ifc+" "+f.String())
			}
			for i := range xls {
				x := xls[i]
				x.Interface = "canxl0"
				if err := w.WriteXL(&x); err != nil {
					t.Fatal(err)
				}
				ifc := x.Interface
				if format == FormatPCAP {
					ifc = ""
				}
				want = append(want, ifc+" "+x.String())
			}
		})
		if !sameFrames(got, want) {
			t.Errorf("%s: Wireshark dissected\n%s\nwant\n%s", format, short(got), short(want))
		}
	}
}

// TestWiresharkReadsRealCaptures: Wireshark and this package agree on the
// captures of Linux 7.0 in testdata.
func TestWiresharkReadsRealCaptures(t *testing.T) {
	bin := tshark(t)
	for _, path := range []string{"testdata/linux-7.0-tcpdump.pcap", "testdata/linux-7.0-dumpcap.pcapng"} {
		out, err := exec.Command(bin, "-r", path, "-T", "json").Output()
		if err != nil {
			t.Fatalf("tshark: %v", err)
		}
		var packets []struct {
			Source struct {
				Layers map[string]json.RawMessage `json:"layers"`
			} `json:"_source"`
		}
		if err := json.Unmarshal(out, &packets); err != nil {
			t.Fatal(err)
		}
		kinds, ifaces, _ := readAll(t, path)
		if len(packets) != len(kinds) {
			t.Fatalf("%s: Wireshark %d packets, this package %d", path, len(packets), len(kinds))
		}
		for i, p := range packets {
			if got, want := dissected(t, p.Source.Layers), ifaces[i]+" "+kinds[i]; !sameFrames([]string{got}, []string{want}) {
				t.Errorf("%s packet %d: Wireshark %s, this package %s", path, i+1, short([]string{got}), short([]string{want}))
			}
		}
	}
}
