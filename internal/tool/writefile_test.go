package tool

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writer writes "new\n" to the path in its second argument, with mode
// 0755: "a" through WriteFileAtomic, "d" through WriteFileDurable. It waits
// for standard input to end first, so a test can act once its pid is known.
const writer = `package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  while ovid/io.Read(0, ovid/io.Alloc(io, 8), 8) > 0 {
  }
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
	// The temp name is taken, by a symlink to a file that is not ours: the
	// link is not followed and not removed, and the write goes to the next
	// name. The name holds the writer's pid, so it is held at its read until
	// the link is in place.
	for _, how := range []string{"a", "d"} {
		work := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		os.WriteFile(victim, []byte("theirs"), 0o600)
		cmd := exec.Command(bin, how, "f")
		cmd.Dir = work
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(work, fmt.Sprintf(".f.%d.0.ovid-tmp", cmd.Process.Pid))
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
		stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("%s with the temp name taken: %v", how, err)
		}
		if got, _ := os.ReadFile(filepath.Join(work, "f")); string(got) != "new\n" {
			t.Fatalf("%s with the temp name taken: f holds %q", how, got)
		}
		if got, _ := os.ReadFile(victim); string(got) != "theirs" {
			t.Fatalf("%s wrote through the symlink: %q", how, got)
		}
		if st, _ := os.Stat(victim); st.Mode().Perm() != 0o600 {
			t.Fatalf("%s changed the mode behind the symlink to %o", how, st.Mode().Perm())
		}
		if dest, _ := os.Readlink(link); dest != victim {
			t.Fatalf("%s replaced the symlink (now %q)", how, dest)
		}
		if es, _ := os.ReadDir(work); len(es) != 2 {
			t.Fatalf("%s left %d entries; want f and the link", how, len(es))
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
