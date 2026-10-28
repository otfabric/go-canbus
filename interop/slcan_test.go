// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/slcan"
)

// slcanPeer listens on TCP for python-can's slcan interface (pyserial's
// socket:// URL, contract 1.2) and returns the URL and a function that
// accepts the connection as a go-canbus slcan Conn playing the adapter.
func slcanPeer(t *testing.T) (string, func() *slcan.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	url := "slcan:socket://" + ln.Addr().String()
	return url, func() *slcan.Conn {
		_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(60 * time.Second))
		c, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := slcan.New(c, slcan.Config{NoCommands: true, Name: "slcan"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
}

func receiveSlcan(t *testing.T, c *slcan.Conn, n int) []canbus.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var got []canbus.Frame
	for len(got) < n {
		var f canbus.Frame
		if err := c.Receive(ctx, &f); err != nil {
			t.Fatalf("after %d of %d frames: %v", len(got), n, err)
		}
		got = append(got, f)
	}
	return got
}

// slcanFixtures are the frame fixtures slcan can carry: it has no error
// state indicator and no error frames.
var slcanFixtures = []string{"standard", "extended", "remote", "fd", "burst"}

// TestSlcan: frames both ways between go-canbus's slcan transport and
// python-can's slcan interface over TCP.
func TestSlcan(t *testing.T) {
	c := caps(t, "pythoncan")
	if !c.Features["slcan"] {
		t.Skip("the pinned pythoncan image predates contract 1.2 (no slcan)")
	}
	for _, fx := range slcanFixtures {
		scenario(t, "pythoncan", "slcan python-can→go "+fx, func(t *testing.T) {
			src := fixture(t, "frames/"+fx+".jsonl")
			want := frames(t, src)
			url, accept := slcanPeer(t)
			args := []string{"send", "--interface", url, "--frames", "-", "--timeout-ms", "60000"}
			if strings.HasPrefix(fx, "fd") {
				args = append(args, "--fd")
			}
			type res struct {
				out  []byte
				code int
			}
			done := make(chan res, 1)
			go func() { out, code := run(t, "pythoncan", src, "", args...); done <- res{out, code} }()
			conn := accept()
			got := receiveSlcan(t, conn, len(want))
			if r := <-done; r.code != 0 {
				t.Fatalf("send exit %d: %s", r.code, r.out)
			}
			compareFrames(t, got, want)
		})
		scenario(t, "pythoncan", "slcan go→python-can "+fx, func(t *testing.T) {
			want := frames(t, fixture(t, "frames/"+fx+".jsonl"))
			url, accept := slcanPeer(t)
			args := []string{"receive", "--interface", url, "--count", strconv.Itoa(len(want)), "--timeout-ms", "60000", "--fd"}
			p := start(t, "pythoncan", "", args...)
			conn := accept()
			select {
			case <-p.ready:
			case <-time.After(30 * time.Second):
				t.Fatal("python-can did not open its slcan bus")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for i := range want {
				if err := conn.Send(ctx, &want[i]); err != nil {
					t.Fatal(err)
				}
			}
			evs, code := p.wait(t, 90*time.Second)
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
	scenario(t, "pythoncan", "slcan fd-esi", func(t *testing.T) {
		f := frames(t, fixture(t, "frames/fd-esi.jsonl"))[0]
		if _, err := slcan.AppendFrame(nil, &f); err == nil {
			t.Fatal("slcan accepted the error state indicator")
		}
		record(t, "pythoncan", "slcan: CAN FD with ESI", "known-difference",
			"slcan has no error state indicator: go-canbus refuses to send it (ErrUnsupported), python-can 4.6.1 drops the flag")
	})
}
