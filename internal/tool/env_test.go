package tool

import (
	"bytes"
	"strings"
	"testing"
)

// envProg prints every string the kernel put after the arguments on its
// stack that has an "=" in it, which is what an environment entry looks
// like (the program's own path follows them and has none); and says
// whether ovid/io.Arg will hand one over. It finds them by address from
// the last argument: a Cap's fields are not readable outside ovid/io.
const envProg = `package demo

import ovid/io

func Dump(io *ovid/io.Cap) i64 {
  var n i64 = 0
  var argc i64 = ovid/io.Argc(io)
  var p i64 = ovid/io.ArgC(io, argc - 1)
  p = (p + ovid/io.CLen(p)) + 1
  while load8(p) != 0 {
    var len i64 = ovid/io.CLen(p)
    var eq bool = false
    var i i64 = 0
    while i < len {
      eq = eq || load8(p + i) == 61
      i = i + 1
    }
    if eq {
      ovid/io.Print(io, bytes(p, len))
      ovid/io.Print(io, "\n")
      n = n + 1
    }
    p = (p + len) + 1
  }
  if ovid/io.ArgC(io, argc + 1) != 0 || ovid/io.ArgC(io, argc) != 0 || ovid/io.ArgC(io, -1) != 0 {
    ovid/io.Print(io, "Arg reads past the arguments\n")
  }
  ovid/io.Print(io, "entries: ")
  ovid/io.PrintInt(io, n)
  ovid/io.Print(io, "\n")
  return 0
}

func main(io *ovid/io.Cap) i64 {
  return Dump(io)
}
`

const envProgTest = `package demo

import ovid/io

func TestDump(io *ovid/io.Cap) i64 {
  return Dump(io)
}
`

// TestProgramGetsNoEnvironment: a program run or tested by ovid is not
// handed ovid's environment. It has no API for one, but the kernel puts the
// environment on the stack after argv, where a program can read it by
// address; a caller's environment is where its secrets are.
func TestProgramGetsNoEnvironment(t *testing.T) {
	needExec(t)
	t.Setenv("OVID_TEST_SECRET", "sk-live-12345")
	dir := mkmod(t, map[string]string{"demo/main.ov": envProg, "demo/main_test.ov": envProgTest})
	clean := func(what, out string) {
		t.Helper()
		if strings.Contains(out, "OVID_TEST_SECRET") || strings.Contains(out, "sk-live") || strings.Contains(out, "PATH=") {
			t.Fatalf("%s: the program read ovid's environment:\n%.600s", what, out)
		}
		if !strings.Contains(out, "entries: 0") || strings.Contains(out, "Arg reads past") {
			t.Fatalf("%s: want an empty environment and an Arg bounded by argc:\n%.600s", what, out)
		}
	}
	var b bytes.Buffer
	if code := RunWith(dir, []string{"a", "b"}, RunOpts{JSON: true}, &b); code != 0 {
		t.Fatalf("run: %s", b.String())
	}
	clean("run --json", last(t, b.String())["stdout"].(string))

	b.Reset()
	if code := Test(dir, "", false, &b); code != 0 {
		t.Fatalf("test: %s", b.String())
	}
	clean("test", lines(t, b.String())[0]["output"].(string))
}
