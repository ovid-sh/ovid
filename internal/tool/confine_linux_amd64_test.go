package tool

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ovid/internal/compile"
)

// writerProg writes "hello" to out.txt in its working directory and to the
// path in its first argument, and prints both results.
const writerProg = `package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var here i64 = ovid/io.WriteFile(io, strptr("out.txt"), 7, strptr("hello\n"), 6, 420)
  var there i64 = ovid/io.WriteFile(io, ovid/io.Arg(io, 1), ovid/io.CLen(ovid/io.Arg(io, 1)), strptr("hello\n"), 6, 420)
  ovid/io.Print(strptr("cwd "))
  ovid/io.PrintInt(io, here)
  ovid/io.Print(strptr(" module "))
  ovid/io.PrintInt(io, there)
  ovid/io.Print(strptr("\n"))
  return 0
}
`

// TestRunConfine: under --confine a program writes in the directory the
// record names and nowhere else, the record says what was applied, and a
// system call outside the program's list kills it.
func TestRunConfine(t *testing.T) {
	needExec(t)
	dir := mkmod(t, demo(writerProg))
	leak := filepath.Join(dir, "demo", "leak.txt")
	var b bytes.Buffer
	if code := RunWith(dir, []string{leak}, RunOpts{JSON: true, Confine: true}, &b); code != 0 {
		t.Fatalf("run: %s", b.String())
	}
	r := last(t, b.String())
	if r["exit"] != float64(0) {
		t.Fatalf("the launcher or the program failed: %s", b.String())
	}
	confined, _ := r["confined"].([]any)
	writable, _ := r["writable"].(string)
	if len(confined) == 0 || confined[0] != "seccomp" || writable == "" {
		t.Fatalf("record: %s", b.String())
	}
	defer os.RemoveAll(writable)
	if got, _ := os.ReadFile(filepath.Join(writable, "out.txt")); string(got) != "hello\n" {
		t.Fatalf("out.txt in the writable directory holds %q", got)
	}
	if !landlockAvailable() {
		if len(confined) != 1 {
			t.Fatalf("no Landlock here, yet the record says %v", confined)
		}
		t.Log("no Landlock on this kernel: the file-system half is not checked")
	} else {
		if len(confined) != 2 || confined[1] != "landlock" || r["stdout"] != "cwd 0 module -1\n" {
			t.Fatalf("with Landlock: %s", b.String())
		}
		if _, err := os.Stat(leak); err == nil {
			t.Fatal("the program wrote into its module")
		}
	}

	// The filter kills a call outside the list: run the same program with
	// write taken off it.
	m, err := loadBuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := compile.CompileAll(m.Prog)
	if err != nil {
		t.Fatal(err)
	}
	var noWrite []int64
	for _, n := range out.Syscalls {
		if n != syscall.SYS_WRITE {
			noWrite = append(noWrite, n)
		}
	}
	st, err := stage(out.Bin, t.TempDir(), "p", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer st.done()
	pio := procIO{confine: &confineSpec{syscalls: noWrite, writable: t.TempDir()}}
	if st.extra != nil {
		pio.extra = []*os.File{st.extra}
	}
	pr := runProc(st.path, []string{leak}, pio, 10*time.Second)
	if pr.exited && pr.code == 113 {
		t.Skip("no seccomp filter here: the launcher exited 113")
	}
	if pr.exited || pr.signal != syscall.SIGSYS {
		t.Fatalf("without write on the list: exited %v code %d signal %v", pr.exited, pr.code, pr.signal)
	}
	r = map[string]any{}
	describeCrash(m, out.Bin, out.Marks, pr, r)
	if r["signal"] != "bad system call" || !strings.Contains(r["hint"].(string), "receipt") {
		t.Fatalf("a death by SIGSYS is described as %v", r)
	}
}

// TestTestConfine: test --confine runs every test confined and says so in
// the summary.
func TestTestConfine(t *testing.T) {
	needExec(t)
	dir := mkmod(t, map[string]string{"demo/main.ov": writerProg, "demo/main_test.ov": `package demo
import ovid/io
func TestWrite(io *ovid/io.Cap) i64 {
  return ovid/io.WriteFile(io, strptr("t.txt"), 5, strptr("x"), 1, 420)
}
`})
	var b bytes.Buffer
	if code := TestWith(dir, TestOpts{Confine: true}, &b); code != 0 {
		t.Fatalf("test: %s", b.String())
	}
	sum := last(t, b.String())
	writable, _ := sum["writable"].(string)
	confined, _ := sum["confined"].([]any)
	if sum["ok"] != true || writable == "" || len(confined) == 0 {
		t.Fatalf("summary: %s", b.String())
	}
	defer os.RemoveAll(writable)
	if _, err := os.Stat(filepath.Join(writable, "t.txt")); err != nil {
		t.Fatalf("the test's file is not in the writable directory: %v", err)
	}
}
