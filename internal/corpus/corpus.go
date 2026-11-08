// SPDX-License-Identifier: MIT

// Package corpus gives the tests access to the recorded vehicle traffic in
// testdata/captures: its manifest (sources, licences, excerpt cuts and the
// statistics every excerpt must decode to), the SHA256SUMS check, and the
// optional extended corpus of whole recordings fetched by
// internal/cmd/fetchcaptures.
//
// It is test infrastructure, not part of the public API.
package corpus

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Stats are the frame counts an excerpt decodes to.
type Stats struct {
	Frames     int      `json:"frames"`
	Interfaces []string `json:"interfaces"`
	Standard   int      `json:"standard"`
	Extended   int      `json:"extended"`
	FD         int      `json:"fd"`
	BRS        int      `json:"brs"`
	ESI        int      `json:"esi"`
	Remote     int      `json:"remote"`
	Error      int      `json:"error"`
	TxMarked   int      `json:"tx_marked"`        // lines ending in " T"
	Tokens     int      `json:"direction_tokens"` // lines ending in " R" or " T"
	OutOfOrder int      `json:"out_of_order"`     // frames older than the one before
}

// Original describes the recording an excerpt was cut from.
type Original struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Archive []string `json:"archive,omitempty"` // nested archive members, outermost first
	Size    int64    `json:"size"`
	SHA256  string   `json:"sha256"`
	Frames  int      `json:"frames,omitempty"`
}

// File is one committed file.
type File struct {
	Path        string    `json:"path"`
	Source      string    `json:"source"`
	Class       string    `json:"class,omitempty"`
	Kind        string    `json:"kind,omitempty"` // "" (a trace), "golden" or "licence"
	Device      string    `json:"device,omitempty"`
	What        string    `json:"what,omitempty"`
	DerivedFrom string    `json:"derived_from,omitempty"`
	Original    *Original `json:"original,omitempty"`
	Cut         string    `json:"cut,omitempty"`
	SHA256      string    `json:"sha256"`
	Stats       *Stats    `json:"stats,omitempty"`
}

// Extended is one whole recording of the extended corpus.
type Extended struct {
	Name          string   `json:"name"` // path below the cache directory
	Source        string   `json:"source"`
	URL           string   `json:"url"`
	Archive       []string `json:"archive,omitempty"`
	ArchiveSHA256 string   `json:"archive_sha256,omitempty"`
	Size          int64    `json:"size"`
	SHA256        string   `json:"sha256"`
	Frames        int      `json:"frames"`
}

// Source is a publisher of recordings.
type Source struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	DOI     string `json:"doi"`
	URL     string `json:"url"`
	Licence string `json:"licence"`
	Class   string `json:"class"`
}

// Manifest is testdata/captures/manifest.json.
type Manifest struct {
	Sources  []Source   `json:"sources"`
	Files    []File     `json:"files"`
	Extended []Extended `json:"extended"`
}

// Traces returns the committed traces (not golden or licence files).
func (m *Manifest) Traces() []File {
	var out []File
	for _, f := range m.Files {
		if f.Kind == "" {
			out = append(out, f)
		}
	}
	return out
}

// Dir returns the absolute path of testdata/captures.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "captures")
}

// Path returns the absolute path of a file of the corpus.
func Path(name string) string { return filepath.Join(Dir(), filepath.FromSlash(name)) }

// Load reads the manifest.
func Load() (*Manifest, error) { return load(Dir()) }

func load(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("corpus: manifest.json: %w", err)
	}
	return &m, nil
}

// SHA256File returns the hex SHA-256 of a file.
func SHA256File(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = fh.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Verify checks that SHA256SUMS lists every committed file below the
// source directories with its hash, and that the manifest agrees with it.
func Verify(m *Manifest) error { return verify(Dir(), m) }

func verify(dir string, m *Manifest) error {
	path := func(name string) string { return filepath.Join(dir, filepath.FromSlash(name)) }
	sums, err := os.ReadFile(path("SHA256SUMS"))
	if err != nil {
		return err
	}
	listed := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok || len(sum) != 64 {
			return fmt.Errorf("corpus: SHA256SUMS: bad line %q", sc.Text())
		}
		listed[name] = sum
	}
	var errs []error
	var onDisk []string
	for _, src := range m.Sources {
		if _, err := os.Stat(path(src.ID)); err != nil {
			continue // a source with no committed excerpt
		}
		_ = filepath.WalkDir(path(src.ID), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				onDisk = append(onDisk, filepath.ToSlash(rel))
			}
			return err
		})
	}
	sort.Strings(onDisk)
	for _, name := range onDisk {
		want, ok := listed[name]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: not in SHA256SUMS", name))
			continue
		}
		got, err := SHA256File(path(name))
		if err != nil {
			errs = append(errs, err)
		} else if got != want {
			errs = append(errs, fmt.Errorf("%s: SHA-256 %s, SHA256SUMS has %s", name, got, want))
		}
		delete(listed, name)
	}
	for name := range listed {
		errs = append(errs, fmt.Errorf("%s: in SHA256SUMS but missing", name))
	}
	inManifest := map[string]bool{}
	for _, f := range m.Files {
		inManifest[f.Path] = true
		if sum, err := SHA256File(path(f.Path)); err != nil || sum != f.SHA256 {
			errs = append(errs, fmt.Errorf("%s: manifest SHA-256 %s does not match the file (%s, %v)", f.Path, f.SHA256, sum, err))
		}
	}
	for _, name := range onDisk {
		if !inManifest[name] {
			errs = append(errs, fmt.Errorf("%s: not in manifest.json", name))
		}
	}
	return errors.Join(errs...)
}

// ExtendedEnv names the directory of the extended corpus; RequireEnv=1
// turns a missing extended corpus into a failure.
const (
	ExtendedEnv = "CANBUS_CAPTURES"
	RequireEnv  = "CANBUS_REQUIRE_CAPTURES"
)

// ExtendedDir returns the directory holding the extended corpus:
// $CANBUS_CAPTURES, else .cache/captures in the repository, and whether
// it exists.
func ExtendedDir() (string, bool) {
	dir := os.Getenv(ExtendedEnv)
	if dir == "" {
		dir = filepath.Join(Dir(), "..", "..", ".cache", "captures")
	}
	st, err := os.Stat(dir)
	return dir, err == nil && st.IsDir()
}
