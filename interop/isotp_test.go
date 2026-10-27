// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/isotp"
	"github.com/otfabric/go-canbus/socketcan"
)

type isotpMode struct {
	name  string
	needs string // capability feature, or ""
	// go is the go-canbus side; the stack side mirrors it.
	cfg   isotp.Config
	flags []string // stack flags besides ids
}

func isotpModes() []isotpMode {
	return []isotpMode{
		{name: "normal", cfg: isotp.Config{TxID: 0x7E0, RxID: 0x7E8}},
		{name: "29-bit", needs: "isotpExtendedIds", cfg: isotp.Config{TxID: 0x18DA10F1, RxID: 0x18DAF110, ExtendedIDs: true}, flags: []string{"--extended-ids"}},
		{name: "extended addressing", needs: "isotpExtendedAddressing",
			cfg:   isotp.Config{TxID: 0x6F0, RxID: 0x6F1, Addressing: isotp.Extended, TxAddress: 0x55, RxAddress: 0xAA},
			flags: []string{"--addressing", "extended", "--tx-ext", "aa", "--rx-ext", "55"}},
		{name: "mixed addressing", needs: "isotpMixedAddressing",
			cfg:   isotp.Config{TxID: 0x6F2, RxID: 0x6F3, Addressing: isotp.Mixed, TxAddress: 0x33, RxAddress: 0x33},
			flags: []string{"--addressing", "mixed", "--tx-ext", "33", "--rx-ext", "33"}},
		{name: "padding", cfg: isotp.Config{TxID: 0x6F4, RxID: 0x6F5, Padding: true}, flags: []string{"--padding", "cc"}},
		{name: "block size and STmin", cfg: isotp.Config{TxID: 0x6F6, RxID: 0x6F7, BlockSize: 4, STmin: time.Millisecond},
			flags: []string{"--bs", "3", "--stmin", "1"}},
		{name: "CAN FD", needs: "isotpFD", cfg: isotp.Config{TxID: 0x6F8, RxID: 0x6F9, FD: true}, flags: []string{"--fd", "--tx-dl", "64"}},
		{name: "CAN FD TX_DL 16", needs: "isotpFD", cfg: isotp.Config{TxID: 0x6FA, RxID: 0x6FB, FD: true, TxDL: 16}, flags: []string{"--fd", "--tx-dl", "16"}},
	}
}

var isotpSizes = []int{1, 7, 8, 62, 100, 1000, 4095}

func pattern(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i % 251)
	}
	return p
}

// stackFlags returns the stack's flags for a mode: the stack transmits on
// the go side's RxID and receives on its TxID.
func stackFlags(m isotpMode) []string {
	f := []string{"--tx-id", hexID(m.cfg.RxID), "--rx-id", hexID(m.cfg.TxID)}
	return append(f, m.flags...)
}

func dialGo(t *testing.T, ifc string, cfg isotp.Config) *isotp.Conn {
	t.Helper()
	conn, err := socketcan.Open(ifc, socketcan.WithFD())
	if err != nil {
		t.Fatal(err)
	}
	bus, _ := canbus.NewBus(conn)
	cfg.MaxMessageSize = 1 << 16
	c, err := isotp.Dial(bus, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); bus.Close() })
	return c
}

// kernelISOTPMissing reports a stack error that means the kernel has no
// can-isotp module (the canutils adapter ends with exit 4).
func kernelISOTPMissing(t *testing.T, a string, code int) {
	if a == "canutils" && code == 4 {
		if os.Getenv("CANBUS_REQUIRE_ISOTP") == "1" {
			t.Fatal("kernel ISO-TP (can-isotp) is not available")
		}
		skipf(t, "kernel ISO-TP (can-isotp) is not available")
	}
}

// TestISOTP exchanges messages between go-canbus's userspace ISO-TP and
// each independent ISO-TP implementation (the Linux kernel through
// can-utils, python-can-isotp), in both directions.
func TestISOTP(t *testing.T) {
	ifc := needVCAN(t)
	for _, a := range isotpAdapters {
		for _, m := range isotpModes() {
			scenario(t, a, "go→stack "+m.name, func(t *testing.T) {
				c := caps(t, a)
				if !c.has("isotp-receive") || m.needs != "" && !c.Features[m.needs] {
					skipf(t, "%s lacks %s", a, m.needs)
				}
				g := dialGo(t, ifc, m.cfg)
				args := append([]string{"isotp-receive", "--interface", ifc, "--count", strconv.Itoa(len(isotpSizes)), "--timeout-ms", "60000"}, stackFlags(m)...)
				if a == "pythoncanisotp" && indexOf(m.flags, "--stmin") < 0 {
					// python-can-isotp's receiver asks for STmin 0 but loses
					// consecutive frames of a 4095-byte message sent back
					// to back on vcan (its Python receive path is slower
					// than the bus). Ask for 1 ms; the kernel receiver
					// keeps STmin 0. See INTEROPERABILITY.md.
					args = append(args, "--stmin", "1")
				}
				p := start(t, a, "", args...)
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				for _, n := range isotpSizes {
					if err := g.Send(ctx, pattern(n)); err != nil {
						t.Fatalf("%d bytes: %v", n, err)
					}
				}
				evs, code := p.wait(t, 90*time.Second)
				kernelISOTPMissing(t, a, code)
				var got [][]byte
				for _, e := range evs {
					if e.Type == "message" {
						b, _ := hex.DecodeString(e.Data)
						got = append(got, b)
					}
				}
				if code != 0 || len(got) != len(isotpSizes) {
					t.Fatalf("exit %d, %d of %d messages: %s", code, len(got), len(isotpSizes), lastLine(evs))
				}
				for i, n := range isotpSizes {
					if !bytes.Equal(got[i], pattern(n)) {
						t.Fatalf("message %d (%d bytes) differs", i, n)
					}
				}
			})
			scenario(t, a, "stack→go "+m.name, func(t *testing.T) {
				c := caps(t, a)
				if !c.has("isotp-send") || m.needs != "" && !c.Features[m.needs] {
					skipf(t, "%s lacks %s", a, m.needs)
				}
				g := dialGo(t, ifc, m.cfg)
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				for _, n := range isotpSizes {
					errc := make(chan error, 1)
					go func() {
						got, err := g.Receive(ctx)
						if err == nil && !bytes.Equal(got, pattern(n)) {
							err = fmt.Errorf("payload differs (%d bytes)", len(got))
						}
						errc <- err
					}()
					args := append([]string{"isotp-send", "--interface", ifc, "--size", strconv.Itoa(n), "--timeout-ms", "20000"}, stackFlags(m)...)
					out, code := run(t, a, nil, "", args...)
					kernelISOTPMissing(t, a, code)
					if code != 0 {
						t.Fatalf("%d bytes: send exit %d: %s", n, code, out)
					}
					if err := <-errc; err != nil {
						t.Fatalf("%d bytes: %v", n, err)
					}
				}
			})
		}
	}
}

// TestISOTPOverflow: a go-canbus receiver with a small limit answers
// OVERFLOW, and each stack's sender fails instead of sending.
func TestISOTPOverflow(t *testing.T) {
	ifc := needVCAN(t)
	for _, a := range isotpAdapters {
		scenario(t, a, "overflow from go receiver", func(t *testing.T) {
			if !caps(t, a).has("isotp-send") {
				skipf(t, "%s has no isotp-send", a)
			}
			conn, _ := socketcan.Open(ifc)
			bus, _ := canbus.NewBus(conn)
			defer bus.Close()
			g, _ := isotp.Dial(bus, isotp.Config{TxID: 0x6E0, RxID: 0x6E1, MaxMessageSize: 100})
			defer g.Close()
			out, code := run(t, a, nil, "", "isotp-send", "--interface", ifc, "--tx-id", "6e1", "--rx-id", "6e0", "--size", "500", "--timeout-ms", "10000")
			kernelISOTPMissing(t, a, code)
			if code != 1 {
				t.Fatalf("stack sender: exit %d (want 1, protocol): %s", code, out)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := g.Receive(ctx); err == nil {
				t.Fatal("go receiver reported no refused message")
			}
		})
	}
}

func lastLine(evs []event) string {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].raw
}
