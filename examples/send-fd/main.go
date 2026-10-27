// SPDX-License-Identifier: MIT

// Command send-fd transmits a 64-byte CAN FD frame with bit rate switching
// on a SocketCAN interface with the CAN FD MTU (Linux).
//
//	go run ./examples/send-fd -i vcan0
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
)

func main() {
	iface := flag.String("i", "vcan0", "CAN interface (MTU 72: ip link set vcan0 mtu 72)")
	flag.Parse()
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i)
	}
	f, err := canbus.NewFD(0x18DA10F1, true, true, data)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := socketcan.Open(*iface, socketcan.WithFD())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Send(ctx, &f); err != nil {
		log.Fatal(err)
	}
	log.Printf("sent %s", f)
}
