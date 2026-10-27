// SPDX-License-Identifier: MIT

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/arxml"
	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/codegen"
	"github.com/otfabric/go-canbus/dbc"
	"github.com/otfabric/go-canbus/kcd"
)

// TestGeneratedModules generates Go from a DBC, a KCD and an ARXML
// database into a separate module (go-canbus replaced by this checkout),
// builds it and runs a program that encodes and decodes every message of
// each through the generated code and the dynamic codec.
func TestGeneratedModules(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module")
	}
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(here))
	dir := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module e2egen\n\ngo 1.23\n\nrequire github.com/otfabric/go-canbus v0.0.0\n\nreplace github.com/otfabric/go-canbus => "+root+"\n")
	sources := []struct {
		pkg   string
		parse func() (*candb.Database, error)
	}{
		{"fromdbc", func() (*candb.Database, error) { db, _, err := dbc.ParseFile("../dbc/testdata/full.dbc"); return db, err }},
		{"fromkcd", func() (*candb.Database, error) { db, _, err := kcd.ParseFile("../kcd/testdata/sample.kcd"); return db, err }},
		{"fromarxml", func() (*candb.Database, error) { db, _, err := arxml.ParseFile("../arxml/testdata/system.arxml"); return db, err }},
	}
	var main strings.Builder
	main.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n")
	for _, s := range sources {
		db, err := s.parse()
		if err != nil {
			t.Fatal(err)
		}
		code, err := codegen.Generate(db, codegen.Options{Package: s.pkg, Source: s.pkg})
		if err != nil {
			t.Fatal(err)
		}
		write(s.pkg+"/"+s.pkg+".go", string(code))
		main.WriteString("\t\"e2egen/" + s.pkg + "\"\n")
	}
	main.WriteString("\t\"github.com/otfabric/go-canbus\"\n)\n\n")
	main.WriteString(`func check(name string, m interface {
	Frame() (canbus.Frame, error)
	Encode() ([]byte, error)
}, decode func(f *canbus.Frame) (interface{ Encode() ([]byte, error) }, error)) {
	f, err := m.Frame()
	if err != nil {
		fmt.Println(name, err)
		os.Exit(1)
	}
	back, err := decode(&f)
	if err != nil {
		fmt.Println(name, "decode:", err)
		os.Exit(1)
	}
	a, _ := m.Encode()
	b, _ := back.Encode()
	if string(a) != string(b) {
		fmt.Println(name, "round trip differs")
		os.Exit(1)
	}
	fmt.Println(name, "ok", f.String())
}

func main() {
	e := fromdbc.Engine{Speed: 1234, Temp: -5, Mode: fromdbc.EngineModeRun}
	check("dbc Engine", &e, func(f *canbus.Frame) (interface{ Encode() ([]byte, error) }, error) { return fromdbc.Decode(f) })
	x := fromkcd.X{Mux: 1, Q: 0xBEEF}
	check("kcd X", &x, func(f *canbus.Frame) (interface{ Encode() ([]byte, error) }, error) { return fromkcd.Decode(f) })
	g := fromarxml.Engine{Gear: fromarxml.EngineGearDrive, Torque: -100, Level: 200}
	check("arxml Engine", &g, func(f *canbus.Frame) (interface{ Encode() ([]byte, error) }, error) { return fromarxml.Decode(f) })
	c := fromarxml.Climate{Fan: 3, Ratio: 0.5}
	check("arxml Climate", &c, func(f *canbus.Frame) (interface{ Encode() ([]byte, error) }, error) { return fromarxml.Decode(f) })
}
`)
	write("main.go", main.String())
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Count(string(out), " ok ") != 4 {
		t.Fatalf("%s", out)
	}
	t.Logf("%s", out)
}
