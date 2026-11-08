// SPDX-License-Identifier: MIT

package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedCorpusVerifies(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(m); err != nil {
		t.Fatal(err)
	}
	if len(m.Traces()) == 0 || len(m.Extended) == 0 {
		t.Fatalf("%d traces, %d extended recordings", len(m.Traces()), len(m.Extended))
	}
	for _, f := range m.Traces() {
		if f.Stats == nil || f.Original == nil || f.Cut == "" || (f.Class != "A" && f.Class != "B") {
			t.Errorf("%s: trace without stats, original, cut or real-device class", f.Path)
		}
	}
}

// fixture writes a small corpus into a temporary directory.
func fixture(t *testing.T, files map[string]string, sums string, m string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sums != "" {
		_ = os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o644)
	}
	if m != "" {
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(m), 0o644)
	}
	return dir
}

const abcSum = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" // "abc"

func TestVerifyReportsEveryDifference(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		sums  string
		m     string
		want  string
	}{
		{"ok", map[string]string{"s/a": "abc"}, abcSum + "  s/a\n",
			`{"sources":[{"id":"s"},{"id":"none"}],"files":[{"path":"s/a","sha256":"` + abcSum + `"}]}`, ""},
		{"changed", map[string]string{"s/a": "abd"}, abcSum + "  s/a\n",
			`{"sources":[{"id":"s"}],"files":[{"path":"s/a","sha256":"` + abcSum + `"}]}`, "SHA256SUMS has"},
		{"unlisted", map[string]string{"s/a": "abc", "s/b": "x"}, abcSum + "  s/a\n",
			`{"sources":[{"id":"s"}],"files":[{"path":"s/a","sha256":"` + abcSum + `"}]}`, "s/b: not in SHA256SUMS"},
		{"missing", map[string]string{"s/a": "abc"}, abcSum + "  s/a\n" + abcSum + "  s/c\n",
			`{"sources":[{"id":"s"}],"files":[{"path":"s/a","sha256":"` + abcSum + `"}]}`, "s/c: in SHA256SUMS but missing"},
		{"not in manifest", map[string]string{"s/a": "abc"}, abcSum + "  s/a\n",
			`{"sources":[{"id":"s"}],"files":[]}`, "not in manifest.json"},
		{"manifest hash", map[string]string{"s/a": "abc"}, abcSum + "  s/a\n",
			`{"sources":[{"id":"s"}],"files":[{"path":"s/a","sha256":"00"}]}`, "manifest SHA-256"},
		{"bad line", map[string]string{"s/a": "abc"}, "nonsense\n", `{"sources":[{"id":"s"}]}`, "bad line"},
		{"no sums", map[string]string{"s/a": "abc"}, "", `{"sources":[{"id":"s"}]}`, "SHA256SUMS"},
	} {
		dir := fixture(t, tc.files, tc.sums, tc.m)
		m, err := load(dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		err = verify(dir, m)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := load(t.TempDir()); err == nil {
		t.Fatal("no manifest: no error")
	}
	if _, err := load(fixture(t, nil, "", "{")); err == nil {
		t.Fatal("bad manifest: no error")
	}
	if _, err := SHA256File(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("missing file: no error")
	}
}

func TestExtendedDir(t *testing.T) {
	t.Setenv(ExtendedEnv, t.TempDir())
	if _, ok := ExtendedDir(); !ok {
		t.Fatal("existing directory not found")
	}
	t.Setenv(ExtendedEnv, filepath.Join(t.TempDir(), "none"))
	if _, ok := ExtendedDir(); ok {
		t.Fatal("missing directory found")
	}
	t.Setenv(ExtendedEnv, "")
	if dir, _ := ExtendedDir(); !strings.HasSuffix(filepath.ToSlash(dir), ".cache/captures") {
		t.Fatal(dir)
	}
	if !strings.HasSuffix(Path("x"), filepath.Join("testdata", "captures", "x")) {
		t.Fatal(Path("x"))
	}
}
