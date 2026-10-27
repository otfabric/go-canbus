// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/socketcan"
)

// rawFixtures are the frame fixtures and the features a stack needs for
// each.
var rawFixtures = []struct {
	name  string
	needs []string
}{
	{"standard", nil},
	{"extended", nil},
	{"remote", []string{"remote"}},
	{"fd", []string{"fd", "brs"}},
	{"fd-esi", []string{"fd"}},
}

func missing(c *capabilities, feats []string) string {
	for _, f := range feats {
		if !c.Features[f] {
			return f
		}
	}
	return ""
}

func openGo(t *testing.T, ifc string, opts ...socketcan.Option) *socketcan.Conn {
	t.Helper()
	c, err := socketcan.Open(ifc, append([]socketcan.Option{socketcan.WithFD(), socketcan.WithReceiveBuffer(1 << 21)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func receiveN(t *testing.T, c *socketcan.Conn, n int, d time.Duration) []canbus.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var out []canbus.Frame
	for len(out) < n {
		var f canbus.Frame
		if err := c.Receive(ctx, &f); err != nil {
			t.Fatalf("received %d of %d frames: %v", len(out), n, err)
		}
		out = append(out, f)
	}
	return out
}

func compareFrames(t *testing.T, got, want []canbus.Frame) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d frames, want %d", len(got), len(want))
	}
	for i := range want {
		if !sameFrame(&got[i], &want[i]) {
			t.Fatalf("frame %d: got %s (%s), want %s (%s)", i, got[i].String(), got[i].Flags, want[i].String(), want[i].Flags)
		}
	}
}

// TestGoSendsStackReceives: go-canbus transmits each fixture, the stack
// receives it.
func TestGoSendsStackReceives(t *testing.T) {
	ifc := needVCAN(t)
	for _, a := range rawAdapters {
		for _, fx := range rawFixtures {
			scenario(t, a, "go→stack "+fx.name, func(t *testing.T) {
				c := caps(t, a)
				if !c.has("receive") {
					skipf(t, "%s has no receive", a)
				}
				if f := missing(c, fx.needs); f != "" {
					skipf(t, "%s lacks feature %s", a, f)
				}
				want := frames(t, fixture(t, "frames/"+fx.name+".jsonl"))
				args := []string{"receive", "--interface", ifc, "--count", strconv.Itoa(len(want)), "--timeout-ms", "30000"}
				if c.Features["fd"] {
					args = append(args, "--fd")
				}
				p := start(t, a, "", args...)
				tx := openGo(t, ifc)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				for i := range want {
					if err := tx.Send(ctx, &want[i]); err != nil {
						t.Fatal(err)
					}
				}
				evs, code := p.wait(t, 60*time.Second)
				var got []canbus.Frame
				for _, e := range evs {
					if e.Type == "frame" {
						got = append(got, e.Frame.frame(t))
					}
				}
				if code != 0 {
					t.Fatalf("exit %d after %d frames", code, len(got))
				}
				compareFrames(t, got, want)
			})
		}
	}
}

// TestStackSendsGoReceives: the stack transmits each fixture, go-canbus
// receives it (error frames included, which only vcan lets userspace
// inject).
func TestStackSendsGoReceives(t *testing.T) {
	ifc := needVCAN(t)
	fixtures := append(rawFixtures[:len(rawFixtures):len(rawFixtures)], struct {
		name  string
		needs []string
	}{"error", []string{"errorFramesTx"}})
	for _, a := range rawAdapters {
		for _, fx := range fixtures {
			scenario(t, a, "stack→go "+fx.name, func(t *testing.T) {
				c := caps(t, a)
				if !c.has("send") {
					skipf(t, "%s has no send", a)
				}
				needs := fx.needs
				if fx.name == "fd-esi" {
					needs = []string{"fd", "esi"}
				}
				if f := missing(c, needs); f != "" {
					skipf(t, "%s lacks feature %s", a, f)
				}
				src := fixture(t, "frames/"+fx.name+".jsonl")
				want := frames(t, src)
				rx := openGo(t, ifc, socketcan.WithFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll}))
				args := []string{"send", "--interface", ifc, "--frames", "-"}
				if strings.HasPrefix(fx.name, "fd") {
					args = append(args, "--fd")
				}
				errc := make(chan []canbus.Frame, 1)
				go func() { errc <- receiveN(t, rx, len(want), 60*time.Second) }()
				out, code := run(t, a, src, "", args...)
				if code != 0 {
					t.Fatalf("send exit %d: %s", code, out)
				}
				got := <-errc
				compareFrames(t, got, want)
				if fx.name == "error" {
					for i := range got {
						if _, ok := canbus.ParseError(&got[i]); !ok {
							t.Fatalf("frame %d not decoded as an error frame", i)
						}
					}
				}
			})
		}
	}
}

// TestBurstOrderAndLoss: 2000 back-to-back frames, both directions; the
// receiver checks order and completeness with sequence numbers.
func TestBurstOrderAndLoss(t *testing.T) {
	ifc := needVCAN(t)
	src := fixture(t, "frames/burst.jsonl")
	want := frames(t, src)
	check := func(t *testing.T, got []canbus.Frame) {
		for i := range got {
			if seq := binary.BigEndian.Uint32(got[i].Data[:4]); seq != uint32(i) {
				t.Fatalf("frame %d carries sequence %d: loss or reordering", i, seq)
			}
		}
	}
	for _, a := range rawAdapters {
		scenario(t, a, "burst stack→go", func(t *testing.T) {
			if !caps(t, a).has("send") {
				skipf(t, "%s has no send", a)
			}
			rx := openGo(t, ifc)
			errc := make(chan []canbus.Frame, 1)
			go func() { errc <- receiveN(t, rx, len(want), 120*time.Second) }()
			if out, code := run(t, a, src, "", "send", "--interface", ifc, "--frames", "-"); code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			check(t, <-errc)
		})
		scenario(t, a, "burst go→stack", func(t *testing.T) {
			if !caps(t, a).has("receive") {
				skipf(t, "%s has no receive", a)
			}
			p := start(t, a, "", "receive", "--interface", ifc, "--count", strconv.Itoa(len(want)), "--timeout-ms", "60000")
			// Paced at 200 µs (5000 frames/s): the receivers of the
			// interpreted stacks overflow their socket buffers at the
			// full vcan rate, which would measure them, not go-canbus.
			tx := openGo(t, ifc)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for i := range want {
				if err := tx.Send(ctx, &want[i]); err != nil {
					t.Fatal(err)
				}
				time.Sleep(200 * time.Microsecond)
			}
			evs, code := p.wait(t, 120*time.Second)
			var got []canbus.Frame
			for _, e := range evs {
				if e.Type == "frame" {
					got = append(got, e.Frame.frame(t))
				}
			}
			if code != 0 {
				t.Fatalf("exit %d: received %d of %d", code, len(got), len(want))
			}
			check(t, got)
		})
	}
}

// TestMultipleConsumers: every receiving stack and a go-canbus Bus with
// two subscriptions receive the same frames at once.
func TestMultipleConsumers(t *testing.T) {
	ifc := needVCAN(t)
	scenario(t, "all", "multi-consumer", func(t *testing.T) {
		want := frames(t, fixture(t, "frames/extended.jsonl"))
		var procs []*proc
		for _, a := range rawAdapters {
			if caps(t, a).has("receive") {
				procs = append(procs, start(t, a, "", "receive", "--interface", ifc, "--count", strconv.Itoa(len(want)), "--timeout-ms", "30000"))
			}
		}
		conn := openGo(t, ifc)
		bus, _ := canbus.NewBus(conn)
		defer bus.Close()
		s1, _ := bus.Subscribe()
		s2, _ := bus.Subscribe()
		tx := openGo(t, ifc)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := range want {
			if err := tx.Send(ctx, &want[i]); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range []*canbus.Subscription{s1, s2} {
			var got []canbus.Frame
			for range want {
				f, err := s.Receive(ctx)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, f)
			}
			compareFrames(t, got, want)
		}
		for _, p := range procs {
			evs, code := p.wait(t, 60*time.Second)
			var got []canbus.Frame
			for _, e := range evs {
				if e.Type == "frame" {
					got = append(got, e.Frame.frame(t))
				}
			}
			if code != 0 {
				t.Fatalf("%s exit %d", p.adapter, code)
			}
			compareFrames(t, got, want)
		}
	})
}

// TestCaptureAndReplay: a stack's candump capture of go-canbus traffic
// parses with go-canbus; a go-canbus candump log replays with the stack.
func TestCaptureAndReplay(t *testing.T) {
	ifc := needVCAN(t)
	for _, a := range rawAdapters {
		scenario(t, a, "capture: stack writes, go reads", func(t *testing.T) {
			c := caps(t, a)
			if !c.has("capture") {
				skipf(t, "%s has no capture", a)
			}
			want := append(frames(t, fixture(t, "frames/standard.jsonl")), frames(t, fixture(t, "frames/fd.jsonl"))...)
			work := workDir(t)
			p := start(t, a, work, "capture", "--interface", ifc, "--output", "/work/cap.log", "--count", strconv.Itoa(len(want)), "--timeout-ms", "30000", "--fd")
			tx := openGo(t, ifc)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for i := range want {
				if err := tx.Send(ctx, &want[i]); err != nil {
					t.Fatal(err)
				}
			}
			if _, code := p.wait(t, 60*time.Second); code != 0 {
				t.Fatalf("capture exit %d", code)
			}
			r, format, err := capture.NewReader(mustOpen(t, work+"/cap.log"))
			if err != nil || format != capture.FormatCandump {
				t.Fatalf("%v (%s)", err, format)
			}
			var got []canbus.Frame
			for {
				var f canbus.Frame
				if err := r.Read(&f); err != nil {
					if err != io.EOF {
						raw, _ := os.ReadFile(work + "/cap.log")
						t.Fatalf("after %d frames: %v\n%s", len(got), err, raw)
					}
					break
				}
				if f.Interface != ifc || f.Timestamp.IsZero() {
					t.Fatalf("metadata %q %v", f.Interface, f.Timestamp)
				}
				got = append(got, f)
			}
			compareFrames(t, got, want)
		})
		scenario(t, a, "replay: go writes, stack replays", func(t *testing.T) {
			c := caps(t, a)
			if !c.has("replay") {
				skipf(t, "%s has no replay", a)
			}
			want := append(frames(t, fixture(t, "frames/extended.jsonl")), frames(t, fixture(t, "frames/fd.jsonl"))...)
			work := workDir(t)
			f := mustCreate(t, work+"/in.log")
			w := capture.NewCandumpWriter(f, ifc)
			base := time.Unix(1700000000, 0)
			for i := range want {
				fr := want[i]
				fr.Timestamp = base.Add(time.Duration(i) * time.Millisecond)
				_ = w.Write(&fr)
			}
			_ = w.Close()
			f.Close()
			rx := openGo(t, ifc)
			errc := make(chan []canbus.Frame, 1)
			go func() { errc <- receiveN(t, rx, len(want), 60*time.Second) }()
			if out, code := run(t, a, nil, work, "replay", "--interface", ifc, "--input", "/work/in.log", "--fd"); code != 0 {
				t.Fatalf("replay exit %d: %s", code, out)
			}
			compareFrames(t, <-errc, want)
		})
	}
}

var _ = fmt.Sprint
