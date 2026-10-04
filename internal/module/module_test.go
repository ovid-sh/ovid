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

// TestToolchainProvenance: only packages from the embedded std are the
// toolchain's; the same paths from a std dir that ovid.mod names are not,
// and a module package may not take a std package's path.
func TestToolchainProvenance(t *testing.T) {
	d := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(d, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ovid.mod", "module m\nentry m\n")
	write("m/m.ov", "package m\nimport ovid/io\nimport ovid/mem\nimport ovid/x\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n")
	write("ovid/x/x.ov", "package ovid/x\n")
	toolchain := func() map[string]bool {
		t.Helper()
		m, err := Load(d)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Errors) != 0 {
			t.Fatalf("load errors %+v", m.Errors)
		}
		out := map[string]bool{}
		for _, p := range m.Prog.Packages {
			out[p.Path] = p.Toolchain
		}
		return out
	}
	if got := toolchain(); len(got) != 4 || !got["ovid/io"] || !got["ovid/mem"] || got["m"] || got["ovid/x"] {
		t.Fatalf("embedded std: %v", got)
	}
	write(".std/ovid/io/io.ov", "package ovid/io\n")
	write(".std/ovid/mem/mem.ov", "package ovid/mem\n")
	write("ovid.mod", "module m\nentry m\nstd .std\n")
	if got := toolchain(); len(got) != 4 || got["ovid/io"] || got["ovid/mem"] {
		t.Fatalf("std line: %v", got)
	}
	write("ovid/mem/mem.ov", "package ovid/mem\n")
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Errors) != 1 || m.Errors[0].Code != "reserved_path" || m.Errors[0].Line != 1 || m.Errors[0].Col != 1 {
		t.Fatalf("a module ovid/mem: %+v", m.Errors)
	}
}
