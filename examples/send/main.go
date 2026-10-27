// SPDX-License-Identifier: MIT

// Command send transmits one classical CAN frame on a SocketCAN interface
// (Linux). Writing to a CAN bus can move actuators: use a test bus.
//
//	go run ./examples/send -i vcan0 123#DEADBEEF
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/socketcan"
)

func main() {
	iface := flag.String("i", "vcan0", "CAN interface")
	flag.Parse()
	text := "123#DEADBEEF"
	if flag.NArg() > 0 {
		text = flag.Arg(0)
	}
	var f canbus.Frame
	if err := capture.ParseFrame(text, &f); err != nil {
		log.Fatal(err)
	}
	if f.IsFD() {
		log.Fatal("a CAN FD frame: use examples/send-fd")
	}
	conn, err := socketcan.Open(*iface)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Send(ctx, &f); err != nil {
		log.Fatal(err)
	}
	log.Printf("sent %s on %s", f, *iface)
}
