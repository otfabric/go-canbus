// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/socketcan"
)

// xlIface is the CAN XL interface: $CANBUS_TEST_XL_IFACE or vcanxl0, up
// with the CAN XL MTU. Without it the CAN XL scenarios skip, unless
// CANBUS_REQUIRE_XL=1.
func xlIface(t *testing.T) string {
	t.Helper()
	name := os.Getenv("CANBUS_TEST_XL_IFACE")
	if name == "" {
		name = "vcanxl0"
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil || ifi.Flags&net.FlagUp == 0 || ifi.MTU < 76 {
		msg := name + " missing, down or without the CAN XL MTU (ip link set " + name + " mtu 2060)"
		if os.Getenv("CANBUS_REQUIRE_XL") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return name
}

// xlDoc is the contract's CAN XL frame document (1.3).
type xlDoc struct {
	XL        bool   `json:"xl"`
	Priority  uint16 `json:"priority"`
	VCID      uint8  `json:"vcid"`
	SEC       bool   `json:"sec"`
	RRS       bool   `json:"rrs"`
	SDT       uint8  `json:"sdt"`
	AF        uint32 `json:"af"`
	Len       int    `json:"len"`
	Data      string `json:"data"`
	Timestamp string `json:"timestamp,omitempty"`
	Interface string `json:"interface,omitempty"`
}

func (d *xlDoc) xlFrame(t *testing.T) canbus.XLFrame {
	t.Helper()
	data, err := hex.DecodeString(d.Data)
	if err != nil || !d.XL || len(data) != d.Len {
		t.Fatalf("bad CAN XL document %+v", d)
	}
	x := canbus.XLFrame{Priority: d.Priority, VCID: d.VCID, SDT: d.SDT, AF: d.AF, Data: data}
	if d.SEC {
		x.Flags |= canbus.XLSEC
	}
	if d.RRS {
		x.Flags |= canbus.XLRRS
	}
	return x
}

func xlFixture(t *testing.T) ([]byte, []canbus.XLFrame) {
	src := fixture(t, "frames/xl.jsonl")
	var out []canbus.XLFrame
	for _, line := range bytes.Split(src, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		var d xlDoc
		if err := json.Unmarshal(line, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d.xlFrame(t))
	}
	return src, out
}

func needXL(t *testing.T, a string) {
	t.Helper()
	if !caps(t, a).Features["xl"] {
		skipf(t, "%s has no CAN XL (feature xl, contract 1.3)", a)
	}
}

func compareXL(t *testing.T, got, want []canbus.XLFrame) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d CAN XL frames, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].String() != want[i].String() {
			t.Fatalf("frame %d: got %.60s…, want %.60s…", i, got[i].String(), want[i].String())
		}
	}
}

func openXL(t *testing.T, ifc string) *socketcan.Conn {
	t.Helper()
	c, err := socketcan.Open(ifc, socketcan.WithXL(), socketcan.WithReceiveBuffer(1<<21))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestXLGoSendsStackReceives: go-canbus sends the CAN XL fixture, the
// stack receives it with --xl.
func TestXLGoSendsStackReceives(t *testing.T) {
	ifc := xlIface(t)
	for _, a := range rawAdapters {
		scenario(t, a, "go→stack xl", func(t *testing.T) {
			needXL(t, a)
			_, want := xlFixture(t)
			p := start(t, a, "", "receive", "--interface", ifc, "--xl", "--count", strconv.Itoa(len(want)), "--timeout-ms", "30000")
			tx := openXL(t, ifc)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for i := range want {
				if err := tx.SendXL(ctx, &want[i]); err != nil {
					t.Fatal(err)
				}
			}
			evs, code := p.wait(t, 60*time.Second)
			var got []canbus.XLFrame
			for _, e := range evs {
				if e.Type != "frame" {
					continue
				}
				var ev struct {
					Frame xlDoc `json:"frame"`
				}
				if err := json.Unmarshal([]byte(e.raw), &ev); err != nil {
					t.Fatal(err)
				}
				got = append(got, ev.Frame.xlFrame(t))
			}
			if code != 0 {
				t.Fatalf("exit %d after %d frames", code, len(got))
			}
			compareXL(t, got, want)
		})
	}
}

// TestXLStackSendsGoReceives: the stack sends the CAN XL fixture, go-canbus
// receives it with ReceiveXL.
func TestXLStackSendsGoReceives(t *testing.T) {
	ifc := xlIface(t)
	for _, a := range rawAdapters {
		scenario(t, a, "stack→go xl", func(t *testing.T) {
			needXL(t, a)
			src, want := xlFixture(t)
			rx := openXL(t, ifc)
			got := make(chan []canbus.XLFrame, 1)
			errc := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				var out []canbus.XLFrame
				for len(out) < len(want) {
					var x canbus.XLFrame
					if err := rx.ReceiveXL(ctx, &x); err != nil {
						errc <- err
						return
					}
					out = append(out, x)
				}
				got <- out
			}()
			out, code := run(t, a, src, "", "send", "--interface", ifc, "--xl", "--frames", "-")
			if code != 0 {
				t.Fatalf("send exit %d: %s", code, out)
			}
			select {
			case g := <-got:
				compareXL(t, g, want)
			case err := <-errc:
				t.Fatal(err)
			}
		})
	}
}

// TestXLCapture: the stack captures go-canbus's CAN XL frames (and a CAN
// frame) into a candump log; go-canbus's capture reader reads it back, and
// each line's frame text equals go-canbus's own notation.
func TestXLCapture(t *testing.T) {
	ifc := xlIface(t)
	for _, a := range rawAdapters {
		scenario(t, a, "capture xl", func(t *testing.T) {
			needXL(t, a)
			c := caps(t, a)
			if !c.has("capture") {
				skipf(t, "%s has no capture", a)
			}
			_, want := xlFixture(t)
			cc, _ := canbus.NewStandard(0x123, []byte{0xAB})
			work := workDir(t)
			p := start(t, a, work, "capture", "--interface", ifc, "--xl", "--count", strconv.Itoa(len(want)+1),
				"--timeout-ms", "30000", "--output", "/work/xl.log")
			tx := openXL(t, ifc)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for i := range want {
				if err := tx.SendXL(ctx, &want[i]); err != nil {
					t.Fatal(err)
				}
				if i == 2 {
					if err := tx.Send(ctx, &cc); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, code := p.wait(t, 60*time.Second); code != 0 {
				t.Fatalf("capture exit %d", code)
			}
			raw, err := os.ReadFile(filepath.Join(work, "xl.log"))
			if err != nil {
				t.Fatal(err)
			}
			r, format, err := capture.NewReader(bytes.NewReader(raw))
			if err != nil || format != capture.FormatCandump {
				t.Fatalf("%s %v", format, err)
			}
			var got []canbus.XLFrame
			var plain []canbus.Frame
			for {
				var f canbus.Frame
				var x canbus.XLFrame
				isXL, err := r.(capture.XLReader).ReadAny(&f, &x)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if isXL {
					got = append(got, x)
				} else {
					plain = append(plain, f)
				}
			}
			compareXL(t, got, want)
			if len(plain) != 1 || plain[0].String() != cc.String() {
				t.Fatalf("CAN frames in the log: %v", plain)
			}
			// candump's text of each CAN XL frame is go-canbus's notation.
			i := 0
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 3 && canbus.IsXLText(fields[2]) {
					if fields[2] != want[i].String() {
						t.Fatalf("line %d: candump %.60s…, go-canbus %.60s…", i, fields[2], want[i].String())
					}
					i++
				}
			}
		})
	}
}
