package module

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if l := m.Index["st:m.main:2"]; l == nil || l.Decl != "fn:m.main" {
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
