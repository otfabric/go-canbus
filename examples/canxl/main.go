// SPDX-License-Identifier: MIT

// Command canxl sends a 512-byte CAN XL frame and prints every frame on
// the interface, CAN, CAN FD and CAN XL alike, in can-utils notation. The
// interface needs the CAN XL MTU (Linux ≥ 6.2):
//
//	ip link add vcanxl0 type vcan && ip link set vcanxl0 mtu 2060 up
//	go run ./examples/canxl -i vcanxl0
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
	iface := flag.String("i", "vcanxl0", "CAN interface with the CAN XL MTU")
	n := flag.Int("n", 1, "frames to print before exiting")
	flag.Parse()
	conn, err := socketcan.Open(*iface, socketcan.WithXL(), socketcan.WithRecvOwn())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := canbus.XLFrame{Priority: 0x242, Flags: canbus.XLSEC, SDT: 0x03, AF: 0xC0FFEE, Data: make([]byte, 512)}
	for i := range x.Data {
		x.Data[i] = byte(i)
	}
	if err := conn.SendXL(ctx, &x); err != nil {
		log.Fatal(err)
	}
	var f canbus.Frame
	var g canbus.XLFrame
	for i := 0; i < *n; i++ {
		xl, err := conn.ReceiveAny(ctx, &f, &g)
		if err != nil {
			log.Fatal(err)
		}
		if xl {
			log.Printf("%s %.48s… (%d bytes, echo %v)", g.Interface, g.String(), len(g.Data), g.Echo)
		} else {
			log.Printf("%s %s", f.Interface, f.String())
		}
	}
}
