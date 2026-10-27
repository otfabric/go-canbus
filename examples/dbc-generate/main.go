// SPDX-License-Identifier: MIT

// Command dbc-generate generates typed Go from a DBC database and shows
// the generated API, using the package generated from the repository's
// test database (internal/golden/full). Generate your own with
//
//	canbusctl dbc generate --package mydb my.dbc > mydb/mydb.go
package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/otfabric/go-canbus/codegen"
	"github.com/otfabric/go-canbus/dbc"
	"github.com/otfabric/go-canbus/internal/golden/full"
)

func main() {
	db, _, err := dbc.Parse(strings.NewReader("BO_ 1 Ping: 2 A\n SG_ Count : 0|16@1+ (1,0) [0|65535] \"\" B\n"))
	if err != nil {
		log.Fatal(err)
	}
	code, err := codegen.Generate(db, codegen.Options{Package: "ping", Source: "ping.dbc"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("generated %d lines for package ping\n", strings.Count(string(code), "\n"))

	// Using generated code: typed fields, enums, physical accessors.
	var e full.Engine
	if err := e.SetSpeedPhys(1500); err != nil {
		log.Fatal(err)
	}
	e.Mode = full.EngineModeRun
	f, err := e.Frame()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Engine frame:", f)
	m, err := full.Decode(&f)
	if err != nil {
		log.Fatal(err)
	}
	back := m.(*full.Engine)
	fmt.Printf("decoded: speed %.1f rpm, mode %s\n", back.SpeedPhys(), back.Mode)
}
