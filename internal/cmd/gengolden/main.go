// SPDX-License-Identifier: MIT

// Command gengolden regenerates the code generator's golden output,
// internal/golden/*, from the DBC fixtures (go generate ./...).
// With -check it fails when the files on disk differ instead.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/otfabric/go-canbus/codegen"
	"github.com/otfabric/go-canbus/dbc"
)

var goldens = []struct{ src, pkg string }{
	{"dbc/testdata/full.dbc", "full"},
}

func main() {
	check := flag.Bool("check", false, "fail when the golden files are out of date")
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	stale := false
	for _, g := range goldens {
		src, err := os.ReadFile(filepath.Join(*root, g.src))
		if err != nil {
			fail(err)
		}
		db, _, err := dbc.ParseBytes(src)
		if err != nil {
			fail(err)
		}
		sum := sha256.Sum256(src)
		opts := codegen.Options{Package: g.pkg, Source: filepath.Base(g.src), SourceSHA256: hex.EncodeToString(sum[:])}
		code, err := codegen.Generate(db, opts)
		if err != nil {
			fail(err)
		}
		js, err := codegen.GenerateJSON(db, opts)
		if err != nil {
			fail(err)
		}
		dir := filepath.Join(*root, "internal/golden", g.pkg)
		for name, want := range map[string][]byte{g.pkg + ".go": code, g.pkg + ".json": js} {
			path := filepath.Join(dir, name)
			if *check {
				have, _ := os.ReadFile(path)
				if !bytes.Equal(have, want) {
					fmt.Fprintf(os.Stderr, "%s is out of date: run make generate\n", path)
					stale = true
				}
				continue
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				fail(err)
			}
		}
	}
	if stale {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gengolden:", err)
	os.Exit(1)
}
