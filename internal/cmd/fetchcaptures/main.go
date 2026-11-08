// SPDX-License-Identifier: MIT

// Command fetchcaptures downloads the extended corpus of whole vehicle
// recordings listed in testdata/captures/manifest.json into a cache
// directory (default .cache/captures, or $CANBUS_CAPTURES) and checks each
// file's SHA-256. The committed excerpts do not need it; `make
// captures-extended` runs it and then the extended test.
//
// CarDS publishes its CAN logs in a 3.6 GB ZIP of ZIPs: the tool reads the
// outer archive's directory with HTTP range requests and downloads only
// the inner archives it needs (uds.zip, 247 MB; benign.zip, 760 MB).
//
// Usage:
//
//	go run ./internal/cmd/fetchcaptures [-dir DIR] [-source candid,cards,tue]
package main

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/otfabric/go-canbus/internal/corpus"
)

func main() {
	dir, _ := corpus.ExtendedDir()
	flag.StringVar(&dir, "dir", dir, "cache directory")
	sources := flag.String("source", "", "comma-separated source IDs to fetch (default all)")
	flag.Parse()
	if err := run(dir, *sources); err != nil {
		fmt.Fprintln(os.Stderr, "fetchcaptures:", err)
		os.Exit(1)
	}
}

func run(dir, sources string) error {
	m, err := corpus.Load()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for s := range strings.SplitSeq(sources, ",") {
		if s != "" {
			want[s] = true
		}
	}
	f := &fetcher{dir: dir, tmp: map[string]string{}, client: &http.Client{Timeout: 2 * time.Hour}}
	defer f.cleanup()
	for _, e := range m.Extended {
		if len(want) > 0 && !want[e.Source] {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(e.Name))
		if sum, err := corpus.SHA256File(dst); err == nil && sum == e.SHA256 {
			fmt.Println("ok      ", e.Name)
			continue
		}
		fmt.Println("fetching", e.Name)
		if err := f.fetch(e, dst); err != nil {
			return fmt.Errorf("%s: %w", e.Name, err)
		}
		sum, err := corpus.SHA256File(dst)
		if err != nil {
			return err
		}
		if sum != e.SHA256 {
			_ = os.Remove(dst)
			return fmt.Errorf("%s: SHA-256 %s, manifest %s", e.Name, sum, e.SHA256)
		}
		fmt.Println("ok      ", e.Name)
	}
	return nil
}

type fetcher struct {
	dir    string
	tmp    map[string]string // downloaded archive (url + member path) -> local file
	client *http.Client
}

func (f *fetcher) cleanup() {
	for _, p := range f.tmp {
		_ = os.Remove(p)
	}
}

func (f *fetcher) fetch(e corpus.Extended, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	switch len(e.Archive) {
	case 0:
		return f.download(e.URL, dst)
	case 2: // a ZIP downloaded whole: [archive, member]
		key := e.URL
		if _, ok := f.tmp[key]; !ok {
			p := filepath.Join(f.dir, ".download-"+filepath.Base(e.Archive[0]))
			if err := f.download(e.URL, p); err != nil {
				return err
			}
			if sum, err := corpus.SHA256File(p); err != nil || sum != e.ArchiveSHA256 {
				return fmt.Errorf("%s: SHA-256 %s, manifest %s (%v)", e.Archive[0], sum, e.ArchiveSHA256, err)
			}
			f.tmp[key] = p
		}
		return extract(f.tmp[key], e.Archive[1], dst)
	case 3: // a ZIP of ZIPs read by range: [outer, inner archive, member]
		key := e.URL + "!" + e.Archive[1]
		if _, ok := f.tmp[key]; !ok {
			p := filepath.Join(f.dir, ".download-"+filepath.Base(e.Archive[1]))
			if err := f.rangeMember(e.URL, e.Archive[1], p); err != nil {
				return err
			}
			f.tmp[key] = p
		}
		return extract(f.tmp[key], e.Archive[2], dst)
	}
	return fmt.Errorf("archive path %v not supported", e.Archive)
}

func (f *fetcher) download(url, dst string) error {
	resp, err := f.client.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return writeFile(dst, resp.Body)
}

func writeFile(dst string, r io.Reader) error {
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func extract(archive, member, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	i := slices.IndexFunc(zr.File, func(f *zip.File) bool { return f.Name == member })
	if i < 0 {
		return fmt.Errorf("%s: no member %s", archive, member)
	}
	rc, err := zr.File[i].Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return writeFile(dst, rc)
}

// rangeMember downloads one member of a remote ZIP.
func (f *fetcher) rangeMember(url, member, dst string) error {
	ra := &httpReaderAt{url: url, client: f.client}
	size, err := ra.size()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(zr.File, func(f *zip.File) bool { return f.Name == member })
	if i < 0 {
		return fmt.Errorf("no member %s", member)
	}
	rc, err := zr.File[i].Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return writeFile(dst, rc)
}

// httpReaderAt reads a remote file with range requests, in blocks of 8 MiB.
type httpReaderAt struct {
	url    string
	client *http.Client
	off    int64
	buf    []byte
}

const block = 8 << 20

func (h *httpReaderAt) size() (int64, error) {
	req, _ := http.NewRequest(http.MethodGet, h.url, nil)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	var n int64
	if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes 0-0/%d", &n); err != nil || resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("%s: no range support (%s)", h.url, resp.Status)
	}
	return n, nil
}

func (h *httpReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		o := off + int64(n)
		if o < h.off || o >= h.off+int64(len(h.buf)) {
			if err := h.load(o); err != nil {
				return n, err
			}
		}
		c := copy(p[n:], h.buf[o-h.off:])
		if c == 0 {
			return n, io.EOF
		}
		n += c
	}
	return n, nil
}

func (h *httpReaderAt) load(off int64) error {
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, h.url, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+block-1))
		resp, err := h.client.Do(req)
		if err == nil {
			if resp.StatusCode != http.StatusPartialContent {
				_ = resp.Body.Close()
				return fmt.Errorf("range %d: %s", off, resp.Status)
			}
			b, rerr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if rerr == nil && len(b) > 0 {
				h.off, h.buf = off, b
				return nil
			}
			err = errors.Join(rerr, errors.New("short range response"))
		}
		if attempt == 4 {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
}
