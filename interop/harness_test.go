// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// Images of canbus-interop InteropRelease, pinned by multi-arch index
// digest. Override one with CANBUS_INTEROP_<ADAPTER>_IMAGE (upper case),
// e.g. a local build (make interop-images builds canbus-interop's source
// at INTEROP_REF as ghcr.io/otfabric/canbus-interop-<adapter>:dev).
const InteropRelease = "v0.5.0"

var adapters = []string{"canutils", "pythoncan", "socketcanrs", "scapy", "pythoncanisotp", "cantools", "canmatrix"}

var defaultImages = map[string]string{
	"canutils":       "ghcr.io/otfabric/canbus-interop-canutils@sha256:589652cdd8ec347904102c3780e53134927f9d9f88d4bae5fc4ccc8d1a43e533",
	"pythoncan":      "ghcr.io/otfabric/canbus-interop-pythoncan@sha256:e908aae2c571163d875d9a03dacad0758ce6794a404f9223ac43d34acc464080",
	"socketcanrs":    "ghcr.io/otfabric/canbus-interop-socketcanrs@sha256:a2b5996e6ef57cb6b004193f582e375ac0fb9330f1002fc57bae90994763d104",
	"scapy":          "ghcr.io/otfabric/canbus-interop-scapy@sha256:2d4a2b6598e43db1732e7dd9f817dae6b61e2ae5d974d0fef06cd9f07f9e38a5",
	"pythoncanisotp": "ghcr.io/otfabric/canbus-interop-pythoncanisotp@sha256:7f2e8d9f22c71d757b13ddc0e804b6ef39a23a720f9d80bd5cf71a42de1c88ae",
	"cantools":       "ghcr.io/otfabric/canbus-interop-cantools@sha256:b4cca620e311b57d4bd26df45ab7b9153b8a784e57f037a99ca8cf6e658a5434",
	"canmatrix":      "ghcr.io/otfabric/canbus-interop-canmatrix@sha256:428cdbdb832148c93a95c18bc274a9293fcd2e4f311362ee61b430fa6e7ffeda",
}

// Adapter order: raw CAN stacks, ISO-TP stacks, database oracles.
var (
	rawAdapters   = []string{"canutils", "pythoncan", "socketcanrs", "scapy"}
	isotpAdapters = []string{"canutils", "pythoncanisotp"}
	dbAdapters    = []string{"cantools", "canmatrix"}
)

func image(adapter string) string {
	if v := os.Getenv("CANBUS_INTEROP_" + strings.ToUpper(adapter) + "_IMAGE"); v != "" {
		return v
	}
	return defaultImages[adapter]
}

func dockerCmd() []string {
	if v := os.Getenv("CANBUS_INTEROP_DOCKER"); v != "" {
		return strings.Fields(v)
	}
	return []string{"docker"}
}

func iface() string {
	if v := os.Getenv("CANBUS_TEST_IFACE"); v != "" {
		return v
	}
	return "vcan0"
}

// needVCAN fails (or, without CANBUS_REQUIRE_VCAN=1, skips) when the vcan
// interface is missing.
func needVCAN(t *testing.T) string {
	t.Helper()
	name := iface()
	ifi, err := net.InterfaceByName(name)
	if err != nil || ifi.Flags&net.FlagUp == 0 || ifi.MTU < 72 {
		msg := name + " missing, down or without CAN FD MTU"
		if os.Getenv("CANBUS_REQUIRE_VCAN") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return name
}

// ---- results matrix -------------------------------------------------------

type outcome struct {
	Test     string `json:"test"`
	Adapter  string `json:"adapter"`
	Scenario string `json:"scenario"`
	Result   string `json:"result"` // pass, fail, skip, known-difference
	Detail   string `json:"detail,omitempty"`
}

var (
	resultsMu sync.Mutex
	results   []outcome
)

func record(t *testing.T, adapter, scenario, result, detail string) {
	resultsMu.Lock()
	defer resultsMu.Unlock()
	results = append(results, outcome{Test: t.Name(), Adapter: adapter, Scenario: scenario, Result: result, Detail: detail})
}

// scenario runs fn as a subtest and records its outcome.
func scenario(t *testing.T, adapter, name string, fn func(t *testing.T)) {
	t.Run(adapter+"/"+name, func(t *testing.T) {
		defer func() {
			switch {
			case t.Failed():
				record(t, adapter, name, "fail", "")
			case t.Skipped():
				record(t, adapter, name, "skip", skipReason(t))
			default:
				record(t, adapter, name, "pass", "")
			}
		}()
		fn(t)
	})
}

var skipReasons sync.Map

func skipf(t *testing.T, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	skipReasons.Store(t.Name(), msg)
	t.Skip(msg)
}

func skipReason(t *testing.T) string {
	v, _ := skipReasons.Load(t.Name())
	s, _ := v.(string)
	return s
}

func TestMain(m *testing.M) {
	// ASC and BLF carry local wall-clock time without a zone; the
	// reference containers run in UTC.
	time.Local = time.UTC
	code := m.Run()
	if path := os.Getenv("CANBUS_INTEROP_REPORT"); path != "" {
		resultsMu.Lock()
		sort.SliceStable(results, func(i, j int) bool { return results[i].Test < results[j].Test })
		b, _ := json.MarshalIndent(map[string]any{"interopRelease": InteropRelease, "images": images(), "results": results}, "", "  ")
		resultsMu.Unlock()
		_ = os.WriteFile(path, append(b, '\n'), 0o644)
	}
	os.Exit(code)
}

func images() map[string]string {
	out := map[string]string{}
	for a := range defaultImages {
		out[a] = image(a)
	}
	return out
}

// ---- containers -----------------------------------------------------------

type capabilities struct {
	Adapter    string          `json:"adapter"`
	Operations []string        `json:"operations"`
	Features   map[string]bool `json:"features"`
}

func (c *capabilities) has(op string) bool {
	for _, o := range c.Operations {
		if o == op {
			return true
		}
	}
	return false
}

var capCache sync.Map

func caps(t *testing.T, adapter string) *capabilities {
	t.Helper()
	if v, ok := capCache.Load(adapter); ok {
		return v.(*capabilities)
	}
	out, code := run(t, adapter, nil, "", "print-capabilities")
	if code != 0 {
		if os.Getenv("CANBUS_REQUIRE_INTEROP") == "1" {
			t.Fatalf("%s: image %s unusable (exit %d): %s", adapter, image(adapter), code, out)
		}
		skipf(t, "image %s not available", image(adapter))
	}
	var c capabilities
	if err := json.Unmarshal(bytes.TrimSpace(out), &c); err != nil {
		t.Fatalf("%s capabilities: %v: %s", adapter, err, out)
	}
	capCache.Store(adapter, &c)
	return &c
}

// workDir is mounted into containers at /work.
func workDir(t *testing.T) string {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The program's output does not go through Docker. Under load, Docker
// loses the output of a container that exits soon after it wrote it, with
// the right exit code and nothing on stderr; go-modbus's interop suite
// lost reference client results that way (otfabric/go-modbus#9), from the
// attached streams and from the container log alike. So the image's
// entrypoint runs under /bin/sh with stdout and stderr redirected to files
// in a host directory mounted at /out, written by the program itself.
// Every canbus-interop image has /bin/sh.
const redirect = `exec "$@" >/out/stdout 2>/out/stderr`

var entrypoints sync.Map // image -> []string

// entrypoint returns the entrypoint of an adapter's image.
func entrypoint(t *testing.T, adapter string) []string {
	t.Helper()
	img := image(adapter)
	if v, ok := entrypoints.Load(img); ok {
		return v.([]string)
	}
	inspect := func() ([]byte, error) {
		a := append(dockerCmd(), "image", "inspect", "--format", "{{json .Config.Entrypoint}}", img)
		return exec.Command(a[0], a[1:]...).Output()
	}
	out, err := inspect()
	if err != nil {
		// "docker run" would pull it; so does this.
		a := append(dockerCmd(), "pull", "-q", img)
		if pullOut, pullErr := exec.Command(a[0], a[1:]...).CombinedOutput(); pullErr == nil {
			out, err = inspect()
		} else {
			err = fmt.Errorf("%v: %s", pullErr, bytes.TrimSpace(pullOut))
		}
	}
	if err != nil {
		if os.Getenv("CANBUS_REQUIRE_INTEROP") == "1" {
			t.Fatalf("%s: image %s unusable: %v", adapter, img, err)
		}
		skipf(t, "image %s not available", img)
	}
	var ep []string
	if err := json.Unmarshal(bytes.TrimSpace(out), &ep); err != nil || len(ep) == 0 {
		t.Fatalf("image %s has no entrypoint (%s)", img, bytes.TrimSpace(out))
	}
	entrypoints.Store(img, ep)
	return ep
}

// dockerArgs returns the docker command line for a container that runs the
// adapter's program with args, and the host directory its stdout and stderr
// files are written to.
func dockerArgs(t *testing.T, adapter, work string, args []string) (argv []string, outDir string) {
	t.Helper()
	ep := entrypoint(t, adapter)
	outDir = workDir(t)
	a := append(dockerCmd(), "run", "--rm", "-i", "--network", "host", "-v", outDir+":/out")
	if work != "" {
		a = append(a, "-v", work+":/work")
	}
	a = append(a, "--name", "canbus-interop-test-"+randHex(6), "--entrypoint", "/bin/sh", image(adapter), "-c", redirect, "sh")
	a = append(a, ep...)
	return append(a, args...), outDir
}

// programStderr returns what the program wrote to stderr.
func programStderr(outDir string) string {
	b, _ := os.ReadFile(filepath.Join(outDir, "stderr"))
	return strings.TrimSpace(string(b))
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// run runs a container to completion and returns stdout and the exit code.
func run(t *testing.T, adapter string, stdin []byte, work string, args ...string) ([]byte, int) {
	t.Helper()
	argv, outDir := dockerArgs(t, adapter, work, args)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var dockerErr bytes.Buffer
	cmd.Stderr = &dockerErr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if msg := strings.TrimSpace(programStderr(outDir) + "\n" + dockerErr.String()); msg != "" {
			t.Logf("%s stderr: %s", adapter, msg)
		}
	}
	// The container has exited: what the program wrote is in the file.
	out, _ := os.ReadFile(filepath.Join(outDir, "stdout"))
	return out, code
}

// event is one line of an operation's output.
type event struct {
	Type     string    `json:"type"`
	Frame    *frameDoc `json:"frame"`
	Received int       `json:"received"`
	Complete bool      `json:"complete"`
	Sent     int       `json:"sent"`
	Len      int       `json:"len"`
	Data     string    `json:"data"`
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	raw      string
	_        struct{}
}

func parseEvents(t *testing.T, out []byte) []event {
	t.Helper()
	var evs []event
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("bad output line %q: %v", line, err)
		}
		e.raw = string(line)
		evs = append(evs, e)
	}
	return evs
}

// proc is a container running in the background (a receiver).
type proc struct {
	adapter string
	cmd     *exec.Cmd
	mu      sync.Mutex
	events  []event
	lines   []string
	ready   chan struct{}
	done    chan struct{}
	code    int
	stderr  bytes.Buffer
}

// start starts a container and waits for its "ready" event.
func start(t *testing.T, adapter, work string, args ...string) *proc {
	t.Helper()
	argv, outDir := dockerArgs(t, adapter, work, args)
	name := argv[indexOf(argv, "--name")+1]
	p := &proc{adapter: adapter, ready: make(chan struct{}), done: make(chan struct{})}
	p.cmd = exec.Command(argv[0], argv[1:]...)
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			k := append(dockerCmd(), "rm", "-f", name)
			_ = exec.Command(k[0], k[1:]...).Run()
			<-p.done
		}
	})
	exited := make(chan struct{})
	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		p.code = 0
		if ee, ok := err.(*exec.ExitError); ok {
			p.code = ee.ExitCode()
		}
		p.mu.Unlock()
		close(exited)
	}()
	go func() {
		once := sync.Once{}
		tailLines(filepath.Join(outDir, "stdout"), exited, func(line string) {
			var e event
			if json.Unmarshal([]byte(line), &e) == nil {
				e.raw = line
				p.mu.Lock()
				p.events = append(p.events, e)
				p.mu.Unlock()
				if e.Type == "ready" {
					once.Do(func() { close(p.ready) })
				}
			}
			p.mu.Lock()
			p.lines = append(p.lines, line)
			p.mu.Unlock()
		})
		p.mu.Lock()
		if msg := programStderr(outDir); msg != "" {
			if p.stderr.Len() > 0 {
				p.stderr.WriteString("\n")
			}
			p.stderr.WriteString(msg)
		}
		p.mu.Unlock()
		close(p.done)
	}()
	select {
	case <-p.ready:
		// The receiver is bound; give a script runtime a moment past the line.
		time.Sleep(50 * time.Millisecond)
	case <-p.done:
		t.Fatalf("%s exited (%d) before ready: %s %s", adapter, p.code, strings.Join(p.lines, "\n"), p.stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatalf("%s not ready", adapter)
	}
	return p
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// tailLines calls line for every complete line written to the file at path,
// as it is written, until exited is closed; then it reads what is left. The
// file may not exist yet when it starts.
func tailLines(path string, exited <-chan struct{}, line func(string)) {
	var (
		f       *os.File
		pending []byte
		buf     = make([]byte, 64<<10)
	)
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	emit := func(final bool) {
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			line(string(pending[:i]))
			pending = pending[i+1:]
		}
		if final && len(pending) > 0 {
			line(string(pending))
			pending = nil
		}
	}
	for {
		finished := false
		select {
		case <-exited:
			finished = true
		default:
		}
		if f == nil {
			f, _ = os.Open(path)
		}
		if f != nil {
			for {
				n, err := f.Read(buf)
				pending = append(pending, buf[:n]...)
				if n == 0 || err != nil {
					break
				}
			}
			emit(finished)
		}
		if finished {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// wait waits for the container to exit and returns its events and code.
func (p *proc) wait(t *testing.T, d time.Duration) ([]event, int) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(d):
		t.Fatalf("%s did not finish", p.adapter)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code != 0 && p.stderr.Len() > 0 {
		t.Logf("%s stderr: %s", p.adapter, strings.TrimSpace(p.stderr.String()))
	}
	return append([]event(nil), p.events...), p.code
}

// ---- fixtures -------------------------------------------------------------

var fixtureCache sync.Map

// fixture fetches a file of canbus-interop's fixtures/ through a database
// image (they all carry them).
func fixture(t *testing.T, path string) []byte {
	t.Helper()
	if v, ok := fixtureCache.Load(path); ok {
		return v.([]byte)
	}
	out, code := run(t, "cantools", nil, "", "print-fixture", path)
	if code != 0 {
		t.Fatalf("print-fixture %s: exit %d", path, code)
	}
	fixtureCache.Store(path, out)
	return out
}

// frameDoc is the contract's frame document.
type frameDoc struct {
	ID        uint32 `json:"id"`
	Extended  bool   `json:"extended"`
	FD        bool   `json:"fd"`
	BRS       bool   `json:"brs"`
	ESI       bool   `json:"esi"`
	Remote    bool   `json:"remote"`
	Error     bool   `json:"error"`
	Len       int    `json:"len"`
	Data      string `json:"data"`
	Timestamp string `json:"timestamp,omitempty"`
	Interface string `json:"interface,omitempty"`
}

func (d *frameDoc) frame(t *testing.T) canbus.Frame {
	t.Helper()
	var f canbus.Frame
	f.ID = canbus.ID(d.ID)
	set := func(b bool, fl canbus.Flags) {
		if b {
			f.Flags |= fl
		}
	}
	set(d.Extended, canbus.FlagExtended)
	set(d.FD, canbus.FlagFD)
	set(d.BRS, canbus.FlagBRS)
	set(d.ESI, canbus.FlagESI)
	set(d.Remote, canbus.FlagRemote)
	set(d.Error, canbus.FlagError)
	data, err := hex.DecodeString(d.Data)
	if err != nil {
		t.Fatalf("bad data %q", d.Data)
	}
	copy(f.Data[:], data)
	f.Len = uint8(d.Len)
	return f
}

func toDoc(f *canbus.Frame) frameDoc {
	return frameDoc{ID: uint32(f.ID), Extended: f.IsExtended(), FD: f.IsFD(), BRS: f.Flags&canbus.FlagBRS != 0,
		ESI: f.Flags&canbus.FlagESI != 0, Remote: f.IsRemote(), Error: f.IsError(), Len: int(f.Len),
		Data: hex.EncodeToString(f.Payload())}
}

// frames parses a JSON Lines fixture of frame documents.
func frames(t *testing.T, src []byte) []canbus.Frame {
	t.Helper()
	var out []canbus.Frame
	for _, line := range bytes.Split(src, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		var d frameDoc
		if err := json.Unmarshal(line, &d); err != nil {
			t.Fatalf("fixture line %q: %v", line, err)
		}
		out = append(out, d.frame(t))
	}
	return out
}

func docsJSONL(fs []canbus.Frame) []byte {
	var b bytes.Buffer
	for i := range fs {
		d := toDoc(&fs[i])
		j, _ := json.Marshal(d)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func sameFrame(a, b *canbus.Frame) bool {
	return a.ID == b.ID && a.Flags == b.Flags && a.Len == b.Len && a.Data == b.Data
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o666); err != nil {
		t.Fatal(err)
	}
	return "/work/" + name
}

func hexID(id canbus.ID) string { return strconv.FormatUint(uint64(id), 16) }
