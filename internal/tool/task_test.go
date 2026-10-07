package tool

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// taskProg exercises ovid/task natively (async POC C): Join returns a
// task's result, a group waits for all of its tasks, tasks sleeping
// concurrently overlap, a task's heap is its own, and a parent that
// returns first waits for a released child.
const taskProg = `package demo

import ovid/io
import ovid/task

func nap(io *ovid/io.Cap, arg i64) i64 {
  var p i64 = ovid/io.Alloc(io, 64)
  store64(p, arg)
  ovid/task.Sleep(io, 20)
  return load64(p)
}

func late(io *ovid/io.Cap, arg i64) i64 {
  ovid/task.Sleep(io, 30)
  ovid/io.Print(strptr("late\n"))
  return 0
}

func root(io *ovid/io.Cap, arg i64) i64 {
  var t0 i64 = ovid/io.NowNs(io)
  var sum i64 = 0
  var i i64 = 1
  var g *ovid/task.Group = ovid/task.NewGroup(io)
  while i <= 20 {
    ovid/task.Go(io, g, taskfn(nap), 0)
    i = i + 1
  }
  var a *ovid/task.Task = ovid/task.Spawn(io, taskfn(nap), 5)
  var b *ovid/task.Task = ovid/task.Spawn(io, taskfn(nap), 10)
  sum = ovid/task.Join(io, a) + ovid/task.Join(io, b)
  ovid/io.PrintInt(io, ovid/task.Wait(io, g))
  ovid/io.Print(strptr(" "))
  ovid/io.PrintInt(io, sum)
  var ms i64 = (ovid/io.NowNs(io) - t0) / 1000000
  if ms < 100 {
    ovid/io.Print(strptr(" overlapped\n"))
  }
  ovid/task.Release(io, ovid/task.Spawn(io, taskfn(late), 0))
  return arg
}

func main(io *ovid/io.Cap) i64 {
  return ovid/task.Run(io, taskfn(root), 7)
}
`

func TestTasks(t *testing.T) {
	dir := mkmod(t, map[string]string{"demo/main.ov": taskProg})
	bin := filepath.Join(t.TempDir(), "prog")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build: %s", b.String())
	}
	if !canExec {
		t.Skip("built only: cannot execute linux/amd64 binaries here")
	}
	out, err := exec.Command(bin).Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if want := "0 15 overlapped\nlate\n"; string(out) != want || code != 7 {
		t.Fatalf("got %q exit %d, want %q exit 7", out, code, want)
	}
}

// TestTaskfnChecked: taskfn names a func of the task form, and only
// ovid/task may switch stacks.
func TestTaskfnChecked(t *testing.T) {
	dir := mkmod(t, map[string]string{"demo/main.ov": `package demo

import ovid/io

func two(a i64, b i64) i64 {
  return a + b
}

func main(io *ovid/io.Cap) i64 {
  var f i64 = taskfn(two)
  return swapstack(f, f)
}
`})
	var b bytes.Buffer
	Check(dir, false, &b)
	got := b.String()
	for _, want := range []string{`"code":"type_mismatch"`, `"code":"syscall_forbidden"`} {
		if !strings.Contains(got, want) {
			t.Errorf("check output lacks %s:\n%s", want, got)
		}
	}
}
