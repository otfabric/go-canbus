// SPDX-License-Identifier: MIT

// Command vcan-pingpong shows two processes talking over a Linux vcan
// interface: start the responder, then the requester.
//
//	sudo ip link add vcan0 type vcan && sudo ip link set vcan0 mtu 72 up
//	go run ./examples/vcan-pingpong -role pong &
//	go run ./examples/vcan-pingpong -role ping -n 100
//
// The same exchange runs against independent implementations (can-utils,
// python-can, socketcan-rs, scapy) in the interop suite.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
)

func main() {
	iface := flag.String("i", "vcan0", "CAN interface")
	role := flag.String("role", "ping", "ping or pong")
	n := flag.Int("n", 10, "pings to send")
	flag.Parse()
	reqID, respID := canbus.ID(0x321), canbus.ID(0x322)
	listen := respID
	if *role == "pong" {
		listen = reqID
	}
	conn, err := socketcan.Open(*iface, socketcan.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(listen)}}))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var f canbus.Frame
	if *role == "pong" {
		for conn.Receive(ctx, &f) == nil {
			f.ID = respID
			f.Timestamp, f.Interface, f.Echo = time.Time{}, "", false
			if err := conn.Send(ctx, &f); err != nil {
				log.Fatal(err)
			}
		}
		return
	}
	var total time.Duration
	for i := 0; i < *n; i++ {
		var d [8]byte
		binary.BigEndian.PutUint64(d[:], uint64(i))
		req, _ := canbus.NewStandard(reqID, d[:])
		start := time.Now()
		if err := conn.Send(ctx, &req); err != nil {
			log.Fatal(err)
		}
		c, cancel := context.WithTimeout(ctx, time.Second)
		err := conn.Receive(c, &f)
		cancel()
		if err != nil {
			log.Fatalf("ping %d: no answer: %v", i, err)
		}
		if binary.BigEndian.Uint64(f.Data[:8]) != uint64(i) {
			log.Fatalf("ping %d: wrong answer %s", i, f)
		}
		total += time.Since(start)
	}
	fmt.Printf("%d pings, mean round trip %v\n", *n, total/time.Duration(*n))
}
