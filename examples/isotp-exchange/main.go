// SPDX-License-Identifier: MIT

// Command isotp-exchange sends a 1000-byte ISO-TP message between two
// endpoints on a virtual bus, with block size and STmin, and prints the
// frames on the bus. It needs no hardware; on Linux the same endpoints
// work on SocketCAN (socketcan.Open, or kernel sockets with
// socketcan.DialISOTP).
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/isotp"
	"github.com/otfabric/go-canbus/virtual"
)

func main() {
	vb := virtual.New(virtual.Config{Name: "vbus"})
	defer vb.Close()
	spy, _ := vb.Open(virtual.WithQueueSize(4096))
	dial := func(tx, rx canbus.ID, bs uint8) *isotp.Conn {
		port, _ := vb.Open()
		bus, _ := canbus.NewBus(port)
		c, err := isotp.Dial(bus, isotp.Config{TxID: tx, RxID: rx, BlockSize: bs, STmin: time.Millisecond, Padding: true})
		if err != nil {
			log.Fatal(err)
		}
		return c
	}
	tester := dial(0x7E0, 0x7E8, 0)
	ecu := dial(0x7E8, 0x7E0, 8)
	defer tester.Close()
	defer ecu.Close()

	msg := bytes.Repeat([]byte("ISO-TP "), 143)[:1000]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		if err := tester.Send(ctx, msg); err != nil {
			log.Fatal(err)
		}
	}()
	got, err := ecu.Receive(ctx)
	if err != nil || !bytes.Equal(got, msg) {
		log.Fatalf("receive: %v", err)
	}
	frames := 0
	for {
		var f canbus.Frame
		c, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		err := spy.Receive(c, &f)
		cancel()
		if err != nil {
			break
		}
		if frames < 4 {
			fmt.Println(" ", f)
		}
		frames++
	}
	fmt.Printf("received %d bytes in %d frames\n", len(got), frames)
}
