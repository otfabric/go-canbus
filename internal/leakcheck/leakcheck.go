// SPDX-License-Identifier: MIT

// Package leakcheck checks tests for goroutines they leave behind. It
// compares goroutine stacks, not counts, so a failure names the goroutine
// and a goroutine that ended cannot hide one that started. The repository
// keeps zero test dependencies, hence no goleak
// (standards/concurrency.md#leaks).
package leakcheck

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// grace is how long goroutines of a closed object may take to end.
const grace = 2 * time.Second

// Reporter is the part of testing.TB that [Check] uses.
type Reporter interface {
	Helper()
	Cleanup(func())
	Errorf(format string, args ...any)
}

var _ Reporter = testing.TB(nil)

// Check records the running goroutines and, when the test ends, fails it if
// a goroutine that was not running then is still running after a grace
// period. Goroutines of the test framework itself do not count.
func Check(t Reporter) {
	t.Helper()
	check(t, grace)
}

func check(t Reporter, grace time.Duration) {
	before := map[string]bool{}
	for id := range snapshot() {
		before[id] = true
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(grace)
		for {
			leaked := leaks(before)
			if len(leaked) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("%d goroutine(s) leaked:\n\n%s", len(leaked), strings.Join(leaked, "\n\n"))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// leaks returns the stacks of the goroutines that are not in before.
func leaks(before map[string]bool) []string {
	var out []string
	for id, stack := range snapshot() {
		if !before[id] {
			out = append(out, stack)
		}
	}
	return out
}

var reID = regexp.MustCompile(`^goroutine (\d+) `)

// snapshot returns the stacks of the other goroutines by goroutine ID.
func snapshot() map[string]string {
	stacks := strings.Split(allStacks(), "\n\n")
	out := make(map[string]string, len(stacks))
	for i, s := range stacks {
		m := reID.FindStringSubmatch(s)
		if i == 0 || m == nil || framework(s) { // the first is the caller's
			continue
		}
		out[m[1]] = s
	}
	return out
}

// framework reports a goroutine of package testing: a test running in
// parallel, the runner, or an alarm.
func framework(stack string) bool {
	return strings.Contains(stack, "testing.tRunner(") ||
		strings.Contains(stack, "testing.(*M).") ||
		strings.Contains(stack, "created by testing.")
}

func allStacks() string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}
