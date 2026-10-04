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
