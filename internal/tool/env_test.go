package tool

import (
	"bytes"
	"strings"
	"testing"
)

// envProg prints every string the kernel put after argv on its stack, the
// environment, reading past the arguments by address; and says whether
// ovid/io.Arg will hand one over.
const envProg = `package demo

import ovid/io

func Dump(io *ovid/io.Cap) i64 {
  var n i64 = 0
  var at i64 = io.argv + ((io.argc + 1) * 8)
  while load64(at) != 0 {
    var p i64 = load64(at)
    ovid/io.Stdout(p, ovid/io.CLen(p))
    ovid/io.Print(strptr("\n"))
    n = n + 1
    at = at + 8
  }
  if ovid/io.Arg(io, io.argc + 1) != 0 || ovid/io.Arg(io, io.argc) != 0 || ovid/io.Arg(io, -1) != 0 {
    ovid/io.Print(strptr("Arg reads past the arguments\n"))
  }
  ovid/io.Print(strptr("entries: "))
  ovid/io.PrintInt(io, n)
  ovid/io.Print(strptr("\n"))
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
