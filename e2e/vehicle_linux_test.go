// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package e2e

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/isotp"
	"github.com/otfabric/go-canbus/socketcan"
)

func vcan(t *testing.T) string {
	name := os.Getenv("CANBUS_TEST_IFACE")
	if name == "" {
		name = "vcan0"
	}
	if ifi, err := net.InterfaceByName(name); err != nil || ifi.Flags&net.FlagUp == 0 || ifi.MTU < 72 {
		if os.Getenv("CANBUS_REQUIRE_VCAN") == "1" {
			t.Fatalf("no usable %s", name)
		}
		t.Skipf("no usable %s", name)
	}
	return name
}

type socketNet struct{ ifc string }

func (s socketNet) open(t *testing.T, fd bool) canbus.Transport {
	opts := []socketcan.Option{socketcan.WithReceiveBuffer(1 << 20)}
	if fd {
		opts = append(opts, socketcan.WithFD())
	}
	c, err := socketcan.Open(s.ifc, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestVehicleVCAN runs the vehicle scenario on vcan with the kernel's
// ISO-TP as the diagnostic server.
func TestVehicleVCAN(t *testing.T) {
	ifc := vcan(t)
	n := socketNet{ifc}
	server := func(t *testing.T) isotp.Endpoint {
		k, err := socketcan.DialISOTP(ifc, isotp.Config{TxID: 0x7E8, RxID: 0x7E0, Padding: true, BlockSize: 8})
		if errors.Is(err, syscall.EPROTONOSUPPORT) && os.Getenv("CANBUS_REQUIRE_ISOTP") != "1" {
			t.Skip("kernel ISO-TP not available")
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { k.Close() })
		return k
	}
	client := func(t *testing.T) isotp.Endpoint {
		b, _ := canbus.NewBus(n.open(t, false))
		c, err := isotp.Dial(b, isotp.Config{TxID: 0x7E0, RxID: 0x7E8, Padding: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close(); b.Close() })
		return c
	}
	vehicle(t, n, server, client)
}

// TestProcesses runs examples/vcan-pingpong as two processes.
func TestProcesses(t *testing.T) {
	ifc := vcan(t)
	bin := filepath.Join(t.TempDir(), "pingpong")
	if out, err := exec.Command("go", "build", "-o", bin, "../examples/vcan-pingpong").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pong := exec.CommandContext(ctx, bin, "-i", ifc, "-role", "pong")
	if err := pong.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pong.Process.Kill(); _ = pong.Wait() }()
	time.Sleep(300 * time.Millisecond)
	out, err := exec.CommandContext(ctx, bin, "-i", ifc, "-role", "ping", "-n", "200").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "200 pings") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
