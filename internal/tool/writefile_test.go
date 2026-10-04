package tool

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writer writes "new\n" to the path in its second argument, with mode
// 0755: "a" through WriteFileAtomic, "d" through WriteFileDurable.
const writer = `package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var path i64 = ovid/io.Arg(io, 2)
  var n i64 = ovid/io.CLen(path)
  var r i64 = 0
  if load8(ovid/io.Arg(io, 1)) == 97 {
    r = ovid/io.WriteFileAtomic(io, path, n, strptr("new\n"), 4, 493)
  } else {
    r = ovid/io.WriteFileDurable(io, path, n, strptr("new\n"), 4, 493)
  }
  if r != 0 {
    return 1
  }
  return 0
}
`

func TestWriteFileAtomic(t *testing.T) {
	dir := mkmod(t, demo(writer))
	bin := filepath.Join(dir, "bin", "demo")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build %d:\n%s", code, b.String())
	}
	needExec(t)
	// write runs the program in cwd and returns its exit code.
	write := func(cwd, how, path string) int {
		t.Helper()
		cmd := exec.Command(bin, how, path)
		cmd.Dir = cwd
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0
	}
	// replaced checks that p holds the new content with the mode asked for
	// and that its directory holds nothing else: no temp file is left.
	replaced := func(p string) {
		t.Helper()
		if got, _ := os.ReadFile(p); string(got) != "new\n" {
			t.Fatalf("%s holds %q", p, got)
		}
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o755 {
			t.Fatalf("%s has mode %o", p, st.Mode().Perm())
		}
		if es, _ := os.ReadDir(filepath.Dir(p)); len(es) != 1 {
			t.Fatalf("%s: %d entries beside it", p, len(es)-1)
		}
	}
	for _, how := range []string{"a", "d"} {
		work := t.TempDir()
		// Over an existing file with another mode, by absolute path.
		abs := filepath.Join(work, "abs", "f")
		os.MkdirAll(filepath.Dir(abs), 0o755)
		os.WriteFile(abs, []byte("old content"), 0o600)
		if code := write(work, how, abs); code != 0 {
			t.Fatalf("%s absolute: exit %d", how, code)
		}
		replaced(abs)
		// A new file named without a directory, and one under a relative one.
		for _, rel := range []string{"bare", "sub/f"} {
			cwd := filepath.Join(work, "rel-"+filepath.Base(rel))
			os.MkdirAll(filepath.Join(cwd, filepath.Dir(rel)), 0o755)
			if code := write(cwd, how, rel); code != 0 {
				t.Fatalf("%s %s: exit %d", how, rel, code)
			}
			replaced(filepath.Join(cwd, rel))
		}
		// A directory that does not exist: an error, and nothing appears.
		if code := write(work, how, filepath.Join(work, "missing", "f")); code != 1 {
			t.Fatalf("%s into a missing directory: exit %d", how, code)
		}
		if _, err := os.Stat(filepath.Join(work, "missing")); err == nil {
			t.Fatalf("%s created the missing directory", how)
		}
	}
	// A running program can be replaced: here, by itself.
	if code := write(dir, "a", bin); code != 0 {
		t.Fatalf("replacing the running binary: exit %d", code)
	}
	if got, _ := os.ReadFile(bin); string(got) != "new\n" {
		t.Fatalf("the binary holds %d bytes after replacing itself", len(got))
	}
}
