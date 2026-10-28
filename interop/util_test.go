// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"encoding/json"
	"os"
	"testing"
)

func mustOpen(t *testing.T, p string) *os.File {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func mustCreate(t *testing.T, p string) *os.File {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
