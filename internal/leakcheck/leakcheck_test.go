// SPDX-License-Identifier: MIT

package leakcheck

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// recorder is a Reporter that keeps what a real test would report.
type recorder struct {
	cleanups []func()
	errors   []string
}

func (r *recorder) Helper()          {}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) finish() {
	for _, f := range r.cleanups {
		f()
	}
}

func parkedForLeakTest(stop <-chan struct{}) { <-stop }

func TestReportsLeakedGoroutineByStack(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	r := &recorder{}
	check(r, 20*time.Millisecond)
	go parkedForLeakTest(stop)
	r.finish()
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "parkedForLeakTest") {
		t.Fatalf("want one error naming the goroutine, got %q", r.errors)
	}
}

// A goroutine that ended must not make room for one that started: a count
// comparison misses this leak.
func TestLeakBehindAnEndedGoroutine(t *testing.T) {
	stop, ended := make(chan struct{}), make(chan struct{})
	defer close(stop)
	go func() { <-ended }()
	r := &recorder{}
	check(r, 20*time.Millisecond)
	close(ended)
	go parkedForLeakTest(stop)
	r.finish()
	if len(r.errors) != 1 {
		t.Fatalf("want one error, got %q", r.errors)
	}
}

func TestGoroutineThatEndsInTime(t *testing.T) {
	stop := make(chan struct{})
	r := &recorder{}
	check(r, 2*time.Second)
	go parkedForLeakTest(stop)
	close(stop)
	r.finish()
	if len(r.errors) != 0 {
		t.Fatalf("unexpected errors: %q", r.errors)
	}
}

func TestCheckRunsAtTestEnd(t *testing.T) {
	r := &recorder{}
	Check(r)
	if len(r.cleanups) != 1 {
		t.Fatalf("cleanups = %d, want 1", len(r.cleanups))
	}
	r.finish()
	if len(r.errors) != 0 {
		t.Fatalf("unexpected errors: %q", r.errors)
	}
}

func TestSnapshotGrowsTheBuffer(t *testing.T) {
	if s := allStacks(); !strings.Contains(s, "TestSnapshotGrowsTheBuffer") {
		t.Fatalf("own stack missing from %q", s)
	}
}
