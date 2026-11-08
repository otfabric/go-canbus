// SPDX-License-Identifier: MIT

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus/internal/corpus"
)

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// server serves files by path, with range requests (http.ServeContent),
// except under /norange/.
func server(t *testing.T, files map[string][]byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/norange")
		b, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/norange/") {
			_, _ = w.Write(b)
			return
		}
		http.ServeContent(w, r, name, time0, bytes.NewReader(b))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchEveryArchiveKind(t *testing.T) {
	log := []byte("(1.000000) can0 123#00\n")
	inner := zipOf(t, map[string][]byte{"uds/a.log": log})
	outer := zipOf(t, map[string][]byte{"CAN/uds.zip": inner})
	flat := zipOf(t, map[string][]byte{"Car/b.log": log})
	srv := server(t, map[string][]byte{"/plain.log": log, "/CAN.zip": outer, "/data.zip": flat})
	dir := t.TempDir()
	f := &fetcher{dir: dir, tmp: map[string]string{}, client: srv.Client()}
	defer f.cleanup()
	for _, e := range []corpus.Extended{
		{Name: "x/plain.log", URL: srv.URL + "/plain.log"},
		{Name: "x/nested.log", URL: srv.URL + "/CAN.zip", Archive: []string{"CAN.zip", "CAN/uds.zip", "uds/a.log"}},
		{Name: "x/nested-again.log", URL: srv.URL + "/CAN.zip", Archive: []string{"CAN.zip", "CAN/uds.zip", "uds/a.log"}},
		{Name: "x/flat.log", URL: srv.URL + "/data.zip", Archive: []string{"data.zip", "Car/b.log"}, ArchiveSHA256: sum(flat)},
	} {
		dst := filepath.Join(dir, e.Name)
		if err := f.fetch(e, dst); err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		if got, _ := os.ReadFile(dst); !bytes.Equal(got, log) {
			t.Fatalf("%s: %q", e.Name, got)
		}
	}
	if len(f.tmp) != 2 {
		t.Fatalf("archives downloaded %d times, want 2", len(f.tmp))
	}
}

func TestFetchErrors(t *testing.T) {
	flat := zipOf(t, map[string][]byte{"Car/b.log": []byte("x")})
	srv := server(t, map[string][]byte{"/data.zip": flat})
	dir := t.TempDir()
	for name, e := range map[string]corpus.Extended{
		"404":           {URL: srv.URL + "/none"},
		"archive hash":  {URL: srv.URL + "/data.zip", Archive: []string{"data.zip", "Car/b.log"}, ArchiveSHA256: "00"},
		"no member":     {URL: srv.URL + "/data.zip", Archive: []string{"data.zip", "Car/c.log"}, ArchiveSHA256: sum(flat)},
		"no range":      {URL: srv.URL + "/norange/data.zip", Archive: []string{"data.zip", "x.zip", "y"}},
		"range member":  {URL: srv.URL + "/data.zip", Archive: []string{"data.zip", "none.zip", "y"}},
		"archive depth": {URL: srv.URL + "/data.zip", Archive: []string{"a"}},
	} {
		// A fetcher per case: a fetcher keeps an archive it has checked
		// for the entries after it, and map order would decide which case
		// downloads it first.
		f := &fetcher{dir: dir, tmp: map[string]string{}, client: srv.Client()}
		e.Name = "e/" + strings.ReplaceAll(name, " ", "-")
		if err := f.fetch(e, filepath.Join(dir, e.Name)); err == nil {
			t.Errorf("%s: no error", name)
		}
		f.cleanup()
	}
}

func TestRangeReaderBlocks(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), block/5) // two blocks
	srv := server(t, map[string][]byte{"/big": data})
	h := &httpReaderAt{url: srv.URL + "/big", client: srv.Client()}
	n, err := h.size()
	if err != nil || n != int64(len(data)) {
		t.Fatal(n, err)
	}
	p := make([]byte, 20)
	if _, err := h.ReadAt(p, block-10); err != nil || !bytes.Equal(p, data[block-10:block+10]) {
		t.Fatalf("across blocks: %q, %v", p, err)
	}
	if _, err := h.ReadAt(p, int64(len(data))+5); err == nil {
		t.Fatal("past the end: no error")
	}
}

func TestRunSelectsSources(t *testing.T) {
	// No entry of the manifest has this source: run fetches nothing.
	if err := run(t.TempDir(), "nosuchsource"); err != nil {
		t.Fatal(err)
	}
}

var time0 = time.Unix(0, 0)
