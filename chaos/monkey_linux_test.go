// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package chaos_test

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canbus/isotp"
	"github.com/otfabric/go-canbus/socketcan"
)

func vcanPair(t *testing.T) (string, string) {
	t.Helper()
	a, b := os.Getenv("CANBUS_TEST_IFACE"), os.Getenv("CANBUS_TEST_IFACE2")
	if a == "" {
		a = "vcan0"
	}
	if b == "" {
		b = "vcan1"
	}
	for _, n := range []string{a, b} {
		if ifi, err := net.InterfaceByName(n); err != nil || ifi.Flags&net.FlagUp == 0 || ifi.MTU < 72 {
			msg := "needs two vcan interfaces (" + a + ", " + b + "), up, MTU 72 or more"
			if os.Getenv("CANBUS_REQUIRE_VCAN") == "1" {
				t.Fatal(msg)
			}
			t.Skip(msg)
		}
	}
	return a, b
}

// TestMonkeyKernelISOTP runs the Linux kernel's ISO-TP (on one vcan) and
// go-canbus's userspace ISO-TP (on another) against each other through a
// chaos bridge, both directions. Invariants: a message is either received
// exactly as sent or not at all; every call returns by its deadline; after
// the faults stop, transfers succeed again.
func TestMonkeyKernelISOTP(t *testing.T) {
	ifA, ifB := vcanPair(t)
	for seed := 0; seed < iterations(6); seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		cfg := randomFaults(r, true)
		bridgeA, err := socketcan.Open(ifA, socketcan.WithFD(), socketcan.WithReceiveBuffer(1<<20))
		if err != nil {
			t.Fatal(err)
		}
		bridgeB, _ := socketcan.Open(ifB, socketcan.WithFD(), socketcan.WithReceiveBuffer(1<<20))
		br, _ := chaos.New(cfg)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- br.Both(ctx, bridgeA, bridgeB) }()

		kcfg := isotp.Config{TxID: 0x6A0, RxID: 0x6A8, MaxMessageSize: 4095}
		k, err := socketcan.DialISOTP(ifA, kcfg)
		if errors.Is(err, syscall.EPROTONOSUPPORT) && os.Getenv("CANBUS_REQUIRE_ISOTP") != "1" {
			cancel()
			<-done
			t.Skip("kernel ISO-TP (can-isotp) is not available")
		}
		if err != nil {
			t.Fatal(err)
		}
		sock, _ := socketcan.Open(ifB, socketcan.WithReceiveBuffer(1<<20))
		bus, _ := canbus.NewBus(sock)
		u, _ := isotp.Dial(bus, isotp.Config{TxID: 0x6A8, RxID: 0x6A0, MaxMessageSize: 4095, TimeoutBs: time.Second, TimeoutCr: time.Second,
			FlowControlDelay: 200 * time.Microsecond})

		check := func(from, to isotp.Endpoint, name string) {
			sent := map[string]bool{}
			for i := 0; i < 5; i++ {
				msg := make([]byte, 1+r.Intn(600))
				r.Read(msg)
				sent[string(msg)] = true
				c, cl := context.WithTimeout(context.Background(), 3*time.Second)
				start := time.Now()
				_ = from.Send(c, msg)
				cl()
				if time.Since(start) > 3500*time.Millisecond {
					t.Fatalf("seed %d %s: send did not return by its deadline", seed, name)
				}
			}
			for {
				c, cl := context.WithTimeout(context.Background(), 300*time.Millisecond)
				got, err := to.Receive(c)
				cl()
				if errors.Is(err, context.DeadlineExceeded) {
					return
				}
				if err == nil && !sent[string(got)] {
					t.Fatalf("seed %d %s (%+v): received %d bytes that were never sent", seed, name, cfg, len(got))
				}
			}
		}
		check(k, u, "kernel→userspace")
		check(u, k, "userspace→kernel")

		// Recovery: stop the faults (a transparent bridge) and transfer.
		cancel()
		<-done
		clean, _ := chaos.New(chaos.Config{})
		ctx, cancel = context.WithCancel(context.Background())
		done = make(chan error, 1)
		go func() { done <- clean.Both(ctx, bridgeA, bridgeB) }()
		msg := bytes.Repeat([]byte("ok"), 300)
		c, cl := context.WithTimeout(context.Background(), 5*time.Second)
		errc := make(chan error, 1)
		go func() { errc <- k.Send(c, msg) }()
		for {
			got, err := u.Receive(c)
			if c.Err() != nil {
				var sendErr error
				select {
				case sendErr = <-errc:
				default:
				}
				t.Fatalf("seed %d: no recovery: receive %v, kernel send %v, bridge %+v", seed, err, sendErr, clean.Stats())
			}
			// Errors of receptions the faults broke may still be queued.
			if err == nil && bytes.Equal(got, msg) {
				break
			}
		}
		if err := <-errc; err != nil {
			t.Fatalf("seed %d: recovery send: %v", seed, err)
		}
		cl()
		t.Logf("seed %d %+v: recovered", seed, cfg)
		u.Close()
		bus.Close()
		k.Close()
		cancel()
		<-done
		bridgeA.Close()
		bridgeB.Close()
	}
}
