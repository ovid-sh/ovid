package module

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeFixture makes a.ov and b.ov with old text in a temp dir and returns
// their paths and the new text WriteFiles is asked to put there.
func writeFixture(t *testing.T) (a, b string, files map[string][]byte) {
	t.Helper()
	d := t.TempDir()
	a, b = filepath.Join(d, "a.ov"), filepath.Join(d, "b.ov")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, b, map[string][]byte{a: []byte("new A"), b: []byte("new B")}
}

func read(t *testing.T, p string) string {
	t.Helper()
	src, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// noTemps fails if a temp file is left in dir.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".ovid-tmp" {
			t.Fatalf("left %s", e.Name())
		}
	}
}

// TestWriteFilesFailsBeforePublishing: when a temp file cannot be made,
// no file changes and nothing is reported written.
func TestWriteFilesFailsBeforePublishing(t *testing.T) {
	a, b, files := writeFixture(t)
	// A file where the third file's parent directory should be: making
	// that directory fails after the first two temp files were made.
	c := filepath.Join(filepath.Dir(a), "c")
	if err := os.WriteFile(c, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	files[filepath.Join(c, "d.ov")] = []byte("new D")
	done, err := WriteFiles(files, 0)
	if err == nil || len(done) != 0 {
		t.Fatalf("done %v, err %v", done, err)
	}
	if read(t, a) != "old" || read(t, b) != "old" {
		t.Fatal("a file changed")
	}
	noTemps(t, filepath.Dir(a))
}

// TestWriteFilesFailsAtTheSecondRename: the first file has its new text
// and is reported written, the second keeps its old text, and no temp file
// is left.
func TestWriteFilesFailsAtTheSecondRename(t *testing.T) {
	a, b, files := writeFixture(t)
	n := 0
	renameFile = func(from, to string) error {
		if n++; n == 2 {
			return errors.New("injected")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameFile = os.Rename })
	done, err := WriteFiles(files, 0)
	if err == nil || !reflect.DeepEqual(done, []string{a}) {
		t.Fatalf("done %v, err %v", done, err)
	}
	if read(t, a) != "new A" || read(t, b) != "old" {
		t.Fatalf("a %q, b %q", read(t, a), read(t, b))
	}
	noTemps(t, filepath.Dir(a))
}

// TestWriteFilesFailsAtTheDirectorySync: every file has its new text and
// is reported written, with the error.
func TestWriteFilesFailsAtTheDirectorySync(t *testing.T) {
	a, b, files := writeFixture(t)
	syncDirOf = func(string) error { return errors.New("injected") }
	t.Cleanup(func() { syncDirOf = syncDir })
	done, err := WriteFiles(files, 0)
	if err == nil || !reflect.DeepEqual(done, []string{a, b}) {
		t.Fatalf("done %v, err %v", done, err)
	}
	if read(t, a) != "new A" || read(t, b) != "new B" {
		t.Fatal("not all files have their new text")
	}
	noTemps(t, filepath.Dir(a))
}
