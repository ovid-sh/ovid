package tool

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// updater changes the file named by its second argument in place. "u"
// writes "PAGE" at 4096, reads it back and the file's first bytes, grows
// the file to 8192, syncs it, and checks the page through a mapping; "l"
// takes a write lock on the whole file (and with a third argument, waits
// for it), says "locked", holds it until standard input ends, gives it
// back, and says "unlocked". Each failed step exits with its own code.
const updater = `package demo
import ovid/io
import ovid/mem

func Update(io *ovid/io.Cap, f *ovid/io.File) i64 {
  if ovid/io.Pwrite(f, "PAGE", 4096) != 0 {
    return 10
  }
  var b bytes = ovid/io.AllocBytes(io, 4)
  var n i64, e i64 = ovid/io.Pread(f, b, 4096)
  if e != 0 || n != 4 || !ovid/mem.Eq(b, "PAGE") {
    return 11
  }
  n, e = ovid/io.Pread(f, b, 0)
  if e != 0 || n != 4 || !ovid/mem.Eq(b, "xxxx") {
    return 12
  }
  n, e = ovid/io.Pread(f, b, 100000)
  if e != 0 || n != 0 {
    return 13
  }
  if ovid/io.Ftruncate(f, 8192) != 0 {
    return 14
  }
  if ovid/io.Fdatasync(f) != 0 {
    return 15
  }
  var m bytes, me i64 = ovid/io.MapFile(f, 8192)
  if me != 0 || !ovid/mem.Eq(m[4096:4100], "PAGE") || m[8191] != 0 || m[0] != 120 {
    return 16
  }
  return 0
}

func Hold(io *ovid/io.Cap, f *ovid/io.File, wait bool) i64 {
  var e i64 = ovid/io.Lock(io, f, ovid/io.LOCK_WRITE, 0, 0, wait)
  if e == ovid/io.E_AGAIN || e == ovid/io.E_ACCES {
    return 3
  }
  if e != 0 {
    return 20
  }
  ovid/io.Print(io, "locked\n")
  var in *ovid/io.File = ovid/io.Stdin(io)
  var buf bytes = ovid/io.AllocBytes(io, 64)
  var got i64, _ = ovid/io.Read(in, buf)
  while got > 0 {
    got, _ = ovid/io.Read(in, buf)
  }
  if ovid/io.Lock(io, f, ovid/io.UNLOCK, 0, 0, false) != 0 {
    return 21
  }
  ovid/io.Print(io, "unlocked\n")
  return 0
}

func main(io *ovid/io.Cap) i64 {
  var f *ovid/io.File, e i64 = ovid/io.Open(io, ovid/io.Arg(io, 2), ovid/io.O_RDWR, 0)
  if e != 0 {
    return 2
  }
  var how bytes = ovid/io.Arg(io, 1)
  var r i64 = 1
  if ovid/mem.Eq(how, "u") {
    r = Update(io, f)
  } else if ovid/mem.Eq(how, "l") {
    r = Hold(io, f, ovid/io.Argc(io) > 3)
  }
  if r == 0 && ovid/io.Close(f) != 0 {
    r = 4
  }
  return r
}
`

// buildUpdater builds updater into a temp module and returns the binary.
func buildUpdater(t *testing.T) string {
	t.Helper()
	dir := mkmod(t, demo(updater))
	bin := filepath.Join(dir, "bin", "demo")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build %d:\n%s", code, b.String())
	}
	needExec(t)
	return bin
}

// TestFileUpdateInPlace: a page is written at an offset and read back,
// the rest of the file is untouched, and the grown file reads as zeros.
func TestFileUpdateInPlace(t *testing.T) {
	bin := buildUpdater(t)
	p := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "u", p).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 8192)
	copy(want, strings.Repeat("x", 100))
	copy(want[4096:], "PAGE")
	if !bytes.Equal(got, want) {
		t.Fatalf("file is %d bytes; first 100 %q, at 4096 %q", len(got), got[:min(100, len(got))], got[min(4096, len(got)):min(4100, len(got))])
	}
}
