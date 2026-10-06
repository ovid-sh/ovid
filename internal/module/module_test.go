package module

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDocStart: a doc comment is the unbroken run of // lines directly
// above a decl; a blank line or any other line ends it (#24).
func TestDocStart(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"func F", "func F"},
		{"// a\nfunc F", "// a\nfunc F"},
		{"package p\n\n// a\n  // b\nfunc F", "// a\n  // b\nfunc F"},
		{"// loose\n\nfunc F", "func F"},
		{"// a\n\n// b\nfunc F", "// b\nfunc F"},
		{"}\n// a\nfunc F", "// a\nfunc F"},
		{"/* a */\nfunc F", "func F"},
		{"// a\n/* b */ func F", "func F"},
		{"x // a\nfunc F", "func F"},
	} {
		src := []byte(c.src)
		off := len(src) - len("func F")
		if got := string(src[DocStart(src, off):]); got != c.want {
			t.Errorf("DocStart(%q) gives %q, want %q", c.src, got, c.want)
		}
	}
}

func TestWriteFiles(t *testing.T) {
	d := t.TempDir()
	a, b := filepath.Join(d, "a.ov"), filepath.Join(d, "p", "b.ov")
	if err := os.WriteFile(a, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if done, err := WriteFiles(map[string][]byte{a: []byte("A"), b: []byte("B")}, 0); err != nil || len(done) != 2 {
		t.Fatal(done, err)
	}
	for p, want := range map[string]string{a: "A", b: "B"} {
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Fatalf("%s = %q", p, got)
		}
	}
	if st, _ := os.Stat(a); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v; want the existing 0600 kept", st.Mode())
	}
	// No temp file is left behind.
	for _, dir := range []string{d, filepath.Dir(b)} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if filepath.Ext(e.Name()) == ".ovid-tmp" {
				t.Fatalf("left %s", e.Name())
			}
		}
	}
}

func TestReplaceFile(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "bin", "out")
	for _, want := range []string{"first", "second"} {
		if err := ReplaceFile(p, []byte(want), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Fatalf("%s = %q", p, got)
		}
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", st.Mode())
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("%d entries; want only the file", len(ents))
	}
	// A target that cannot be replaced is an error and leaves no temp file.
	os.Mkdir(filepath.Join(d, "dir"), 0o755)
	os.WriteFile(filepath.Join(d, "dir", "x"), nil, 0o644)
	if err := ReplaceFile(filepath.Join(d, "dir"), []byte("x"), 0o755); err == nil {
		t.Fatal("replaced a non-empty directory")
	}
	if ents, _ := os.ReadDir(d); len(ents) != 2 {
		t.Fatalf("%d entries in %s; want bin and dir", len(ents), d)
	}
}

func TestHashTellsTwinsApart(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "ovid.mod"), []byte("module m\nentry m\n"), 0o644)
	os.MkdirAll(filepath.Join(d, "m"), 0o755)
	src := "package m\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 0\n  x = x + 1\n  x = x + 1\n  return x\n}\n"
	os.WriteFile(filepath.Join(d, "m", "m.ov"), []byte(src), 0o644)
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if l := m.Index()["st:m.main:2"]; l == nil || l.Decl != "fn:m.main" {
		t.Fatalf("loc %+v", l)
	}
	a, b := m.Hash("st:m.main:2"), m.Hash("st:m.main:3")
	if a == "" || a == b {
		t.Fatalf("identical statements share hash %q", a)
	}
	// The printed forms are the leading digits of full digests.
	if d := m.Digest("st:m.main:2"); len(a) != 12 || len(d) != 64 || d[:12] != a {
		t.Fatalf("hash %q, digest %q", a, d)
	}
	if r, d := m.Revision(), m.RevisionDigest(); len(r) != 16 || len(d) != 64 || d[:16] != r {
		t.Fatalf("revision %q, digest %q", r, d)
	}
	if m.Digest("st:m.main:99") != "" || m.Hash("st:m.main:99") != "" {
		t.Fatal("an unknown id has a hash")
	}
}

// TestStoreStmts: every store builtin indexes as a statement, so ids,
// show, grep, and crash reports treat it like store8 and store64.
func TestStoreStmts(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "demo"), 0o755)
	os.WriteFile(filepath.Join(d, "ovid.mod"), []byte("module demo\nentry demo\n"), 0o644)
	os.WriteFile(filepath.Join(d, "demo/main.ov"), []byte("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  var p i64 = ovid/io.Alloc(io, 8)\n  store8(p, 1)\n  store16(p, 2)\n  store32(p, 3)\n  store64(p, 4)\n  return load16(p) + bswap16(load8(p))\n}\n"), 0o644)
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"st:demo.main:2", "st:demo.main:3", "st:demo.main:4", "st:demo.main:5"} {
		if l := m.Index()[id]; l == nil || l.Kind != "stmt" {
			t.Errorf("%s: want a stmt, got %+v", id, l)
		}
	}
	for _, id := range []string{"ex:demo.main:5", "ex:demo.main:6"} {
		if l := m.Index()[id]; l == nil || l.Kind != "expr" {
			t.Errorf("%s: want an expr, got %+v", id, l)
		}
	}
}
