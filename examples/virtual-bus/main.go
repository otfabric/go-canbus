// SPDX-License-Identifier: MIT

// Command virtual-bus simulates two nodes on an in-memory bus with
// injected faults and shows how a receiver detects loss with sequence
// numbers. It needs no hardware and runs on every OS.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
)

func main() {
	vb := virtual.New(virtual.Config{Name: "sim0", Seed: 42})
	defer vb.Close()
	vb.SetFaults(virtual.Faults{Drop: 0.05, Duplicate: 0.02})
	sender, _ := vb.Open()
	rxPort, _ := vb.Open(virtual.WithQueueSize(4096))
	bus, err := canbus.NewBus(rxPort)
	if err != nil {
		log.Fatal(err)
	}
	defer bus.Close()
	sub, _ := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(0x100)}}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const n = 1000
	for i := uint32(0); i < n; i++ {
		var d [4]byte
		binary.BigEndian.PutUint32(d[:], i)
		f, _ := canbus.NewStandard(0x100, d[:])
		if err := sender.Send(ctx, &f); err != nil {
			log.Fatal(err)
		}
	}
	var next uint32
	lost, dups, got := 0, 0, 0
	for {
		c, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		f, err := sub.Receive(c)
		stop()
		if err != nil {
			break
		}
		got++
		seq := binary.BigEndian.Uint32(f.Data[:4])
		switch {
		case seq < next:
			dups++
		case seq > next:
			lost += int(seq - next)
			next = seq + 1
		default:
			next++
		}
	}
	lost += int(n - next)
	st := vb.FaultStats()
	fmt.Printf("received %d frames: %d lost, %d duplicated (injected: %d dropped, %d duplicated)\n", got, lost, dups, st.Dropped, st.Duplicated)
	if uint64(lost) != st.Dropped || uint64(dups) != st.Duplicated {
		log.Fatal("detection does not match the injected faults")
	}
}
