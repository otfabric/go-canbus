// SPDX-License-Identifier: MIT

package slcan

import (
	"runtime"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// TestOpenSerial opens the slave of a pseudo-terminal as a serial port
// (go-serial sets raw mode) and exchanges frames with a Conn on the
// master, the way a USB adapter's tty carries them.
func TestOpenSerial(t *testing.T) {
	m, path, err := openPTY()
	if err != nil {
		t.Skip("pseudo-terminal:", err)
	}
	dev, err := New(m, Config{NoCommands: true, Name: "device"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := Open(path, 0, Config{Bitrate: 250000})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	must := mustFrame(t)
	frames := []canbus.Frame{
		must(canbus.NewStandard(0x123, []byte{1, 2, 3})),
		must(canbus.NewExtended(0x18DAF110, []byte{0x02, 0x10, 0x03})),
		must(canbus.NewRemote(0x7DF, false, 8)),
		must(canbus.NewFD(0x10, false, true, make([]byte, 64))),
	}
	for i := range frames {
		if err := host.Send(ctx, &frames[i]); err != nil {
			t.Fatal(err)
		}
		var g canbus.Frame
		if err := dev.Receive(ctx, &g); err != nil || g.String() != frames[i].String() || g.Flags != frames[i].Flags {
			t.Fatalf("host → device %d: %v %v", i, g, err)
		}
		if err := dev.Send(ctx, &frames[i]); err != nil {
			t.Fatal(err)
		}
		if err := host.Receive(ctx, &g); err != nil || g.String() != frames[i].String() || g.Interface != path {
			t.Fatalf("device → host %d: %v %v", i, g, err)
		}
	}
	if runtime.GOOS != "linux" {
		// Closing a macOS pty master does not end a pending read on it;
		// closing the slave does.
		_ = host.Close()
	}
	done := make(chan error, 1)
	go func() { done <- dev.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close of the pty master did not end its read")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open("/nonexistent/tty", 0, Config{}); err == nil {
		t.Fatal("opened a missing device")
	}
}
