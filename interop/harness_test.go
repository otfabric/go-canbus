// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bufio"
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
const InteropRelease = "v0.4.0"

var adapters = []string{"canutils", "pythoncan", "socketcanrs", "scapy", "pythoncanisotp", "cantools", "canmatrix"}

var defaultImages = map[string]string{
	"canutils":       "ghcr.io/otfabric/canbus-interop-canutils@sha256:ff087fe538519f3b0635484309862b84e60f939b73d2a6f4c06bdf04b0c2918c",
	"pythoncan":      "ghcr.io/otfabric/canbus-interop-pythoncan@sha256:25d4a94710202e0cd3def0b637b8fdf9493a943a0609a43cad405307109d893b",
	"socketcanrs":    "ghcr.io/otfabric/canbus-interop-socketcanrs@sha256:79e7a52cf1a7a5474e84afb3ccfb531b75549c6f1d388b5de3f6f9a4e3be1b6b",
	"scapy":          "ghcr.io/otfabric/canbus-interop-scapy@sha256:2e1b8871a4d08e32ee80411af338edc968771911eaf1fdec358b6fa4861e529c",
	"pythoncanisotp": "ghcr.io/otfabric/canbus-interop-pythoncanisotp@sha256:e7cdfa55e1e465f622ccfa524ba5da4f62a677689e606be1df523f8f05663720",
	"cantools":       "ghcr.io/otfabric/canbus-interop-cantools@sha256:f93f9e1ab882aa8a323c91df77936e64c355ec621e241cb37bac4fe0c37193bc",
	"canmatrix":      "ghcr.io/otfabric/canbus-interop-canmatrix@sha256:1afbf9eb61f3d7c9400bbc1f8d2b23e4af6c3fe6ecd666d2df32d3cfe6180e54",
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

func dockerArgs(adapter, work string, args []string) []string {
	a := append(dockerCmd(), "run", "--rm", "-i", "--network", "host")
	if work != "" {
		a = append(a, "-v", work+":/work")
	}
	a = append(a, "--name", "canbus-interop-test-"+randHex(6), image(adapter))
	return append(a, args...)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// run runs a container to completion and returns stdout and the exit code.
func run(t *testing.T, adapter string, stdin []byte, work string, args ...string) ([]byte, int) {
	t.Helper()
	argv := dockerArgs(adapter, work, args)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := 0
	if err != nil {
		code = -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if stderr.Len() > 0 {
			t.Logf("%s stderr: %s", adapter, strings.TrimSpace(stderr.String()))
		}
	}
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
	argv := dockerArgs(adapter, work, args)
	name := argv[indexOf(argv, "--name")+1]
	p := &proc{adapter: adapter, ready: make(chan struct{}), done: make(chan struct{})}
	p.cmd = exec.Command(argv[0], argv[1:]...)
	p.cmd.Stderr = &p.stderr
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
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
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
		once := sync.Once{}
		for sc.Scan() {
			line := sc.Text()
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
		}
		err := p.cmd.Wait()
		p.code = 0
		if ee, ok := err.(*exec.ExitError); ok {
			p.code = ee.ExitCode()
		}
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
