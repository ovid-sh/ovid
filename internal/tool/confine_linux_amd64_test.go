package tool

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ovid/internal/compile"
	"ovid/internal/module"
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
	if code := RunWith(dir, []string{leak}, RunOpts{JSON: true}, &b); code != 0 {
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
	pio := procIO{confine: &confineSpec{Syscalls: noWrite, Writable: t.TempDir()}}
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
	describeCrash(m, out.Bin, out.Marks, pr, true, r)
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
	if code := TestWith(dir, TestOpts{}, &b); code != 0 {
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

// TestLauncherNeverRunsTests: a test binary started with the launcher's
// variable set is a launcher and nothing else. It must not run its tests,
// which would start launchers of their own without end (#134).
func TestLauncherNeverRunsTests(t *testing.T) {
	start := func(env string, args ...string) (string, int, time.Duration) {
		t.Helper()
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = []string{confineEnv + "=" + env}
		t0 := time.Now()
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code, time.Since(t0)
	}
	// A program of ours, with the list from its receipt: the launcher
	// becomes it, and the test binary is nothing but that launcher.
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  ovid/io.Print(strptr(\"launched\\n\"))\n  return 3\n}\n"))
	bin, calls := buildSyscalls(t, dir)
	var nums []int64
	for _, n := range calls {
		nums = append(nums, int64(n))
	}
	spec := (&confineSpec{Syscalls: nums, Argv0: "demo", Writable: t.TempDir()}).encode()
	for _, c := range []struct {
		env  string
		args []string
		code int
		out  string
	}{
		{"not a spec", []string{"-test.run=TestLauncherNeverRunsTests"}, 111, ""}, // a bad spec
		{spec, nil, 111, ""}, // a spec and no program
		{spec, []string{"--", bin}, 3, "launched\n"},
	} {
		out, code, took := start(c.env, c.args...)
		if code != c.code || out != c.out || took > 5*time.Second {
			t.Fatalf("%q %v: exit %d in %v with %q, want %d and %q, and no tests run", c.env, c.args, code, took, out, c.code, c.out)
		}
	}
}

// TestConfineWritableDir: the writable directory is kept only when the
// program left something in it, and when there is nowhere to make one the
// program still runs, with nowhere to write.
func TestConfineWritableDir(t *testing.T) {
	needExec(t)
	// A temporary directory of this test's own: other packages' tests run
	// programs at the same time and make writable directories too.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	count := func() int {
		n, _ := filepath.Glob(filepath.Join(tmp, "ovid-writable-*"))
		return len(n)
	}
	quiet := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	before := count()
	var b bytes.Buffer
	if code := RunWith(quiet, nil, RunOpts{JSON: true}, &b); code != 0 {
		t.Fatal(b.String())
	}
	if r := last(t, b.String()); r["writable"] != nil || r["confined"] == nil || count() != before {
		t.Fatalf("a program that wrote nothing: %v, %d directories left (had %d)", r, count(), before)
	}
	b.Reset()
	if code := Test(quiet, "", false, &b); code != 0 || last(t, b.String())["writable"] != nil || count() != before {
		t.Fatalf("test, nothing written: %s", b.String())
	}

	// No temporary directory: the program runs, and may write nowhere.
	if !landlockAvailable() {
		t.Skip("without Landlock the program would write into the working directory")
	}
	writer := mkmod(t, demo(writerProg))
	leak := filepath.Join(writer, "demo", "leak.txt")
	t.Setenv("TMPDIR", filepath.Join(writer, "missing"))
	b.Reset()
	if code := RunWith(writer, []string{leak}, RunOpts{JSON: true}, &b); code != 0 {
		t.Fatal(b.String())
	}
	r := last(t, b.String())
	if r["exit"] != float64(0) || r["stdout"] != "cwd -1 module -1\n" || r["writable"] != nil {
		t.Fatalf("with nowhere to write: %v", r)
	}
	if _, err := os.Stat(leak); err == nil {
		t.Fatal("the program wrote into its module")
	}
}

// TestConfineSpecRoundTrip: a module's name may hold anything, and the spec
// crosses to the launcher as text; it must come back whole.
func TestConfineSpecRoundTrip(t *testing.T) {
	c := &confineSpec{Syscalls: []int64{1, 9, 60}, Writable: "/tmp/a dir;with\"odd\" chars", Landlock: true, Argv0: "demo;x", StatusFD: 5}
	d, err := decodeConfine(c.encode())
	if err != nil || d.Argv0 != c.Argv0 || d.Writable != c.Writable || !d.Landlock || d.StatusFD != 5 || len(d.Syscalls) != 3 || d.Syscalls[2] != 60 {
		t.Fatalf("%v: %+v", err, d)
	}
	if _, err := decodeConfine("not a spec"); err == nil {
		t.Fatal("a bad spec decoded")
	}
}

// TestConfineSetupFailureIsReported: a launcher that cannot confine the
// program never starts it, and the parent is told why, instead of taking
// the launcher's exit for the program's.
func TestConfineSetupFailureIsReported(t *testing.T) {
	needExec(t)
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	bin, calls := buildSyscalls(t, dir)
	var nums []int64
	for _, n := range calls {
		nums = append(nums, int64(n))
	}
	// A writable directory that does not exist: the launcher cannot even be
	// started there, and that is the request's error, not an exit.
	pr := runProc(bin, nil, procIO{confine: &confineSpec{Syscalls: nums, Writable: filepath.Join(t.TempDir(), "gone"), Landlock: landlockAvailable()}}, 10*time.Second)
	if pr.err == nil || pr.exited {
		t.Fatalf("a missing writable directory: exited %v code %d err %v", pr.exited, pr.code, pr.err)
	}
	// A program that is not there: execve would fail; the launcher says so
	// before the filter leaves it unable to.
	pr = runProc(filepath.Join(t.TempDir(), "missing"), nil, procIO{confine: &confineSpec{Syscalls: nums}}, 10*time.Second)
	if pr.err == nil || !strings.Contains(pr.err.Error(), "cannot execute") {
		t.Fatalf("a missing program: exited %v code %d signal %v err %v", pr.exited, pr.code, pr.signal, pr.err)
	}
	// And a program that runs reports nothing.
	pr = runProc(bin, nil, procIO{confine: &confineSpec{Syscalls: nums, Writable: t.TempDir(), Landlock: landlockAvailable()}}, 10*time.Second)
	if pr.err != nil || !pr.exited || pr.code != 0 {
		t.Fatalf("a program that runs: exited %v code %d err %v", pr.exited, pr.code, pr.err)
	}
}

// TestConfineUnenforceable: with no writable directory and no Landlock,
// nothing would make the file system read-only, and the record must not
// say otherwise: the run is refused. With either, it is not.
func TestConfineUnenforceable(t *testing.T) {
	if err := (&confineSpec{Writable: "", Landlock: false}).unenforceable(); err == nil || !strings.Contains(err.Error(), "Landlock") {
		t.Fatalf("no directory and no Landlock: %v", err)
	}
	if err := (&confineSpec{Writable: "", Landlock: true}).unenforceable(); err != nil {
		t.Fatal(err)
	}
	if err := (&confineSpec{Writable: t.TempDir(), Landlock: false}).unenforceable(); err != nil {
		t.Fatal(err)
	}
}

// TestConfineStatusPipeIsNotLeaked: a status pipe from an attempt that did
// not start is closed when the next attempt opens its own.
func TestConfineStatusPipeIsNotLeaked(t *testing.T) {
	c := &confineSpec{}
	w1, err := c.openStatus(0)
	if err != nil {
		t.Fatal(err)
	}
	r1 := c.status
	w2, err := c.openStatus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.closeStatus()
	if _, err := w1.Write([]byte("x")); err == nil {
		t.Fatal("the first pipe's write end is still open")
	}
	if _, err := r1.Read(make([]byte, 1)); err == nil {
		t.Fatal("the first pipe's read end is still open")
	}
	if _, err := w2.Write([]byte("x")); err != nil {
		t.Fatalf("the second pipe is not usable: %v", err)
	}
}

// TestConfineStatus: what the parent makes of the launcher's report.
func TestConfineStatus(t *testing.T) {
	if err := statusError([]byte("+")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ msg, want string }{
		{"", "ended before"},
		{"+!", "execve failed"},
		{"+!the kernel refused the seccomp filter: x", "could not confine the program: the kernel refused the seccomp"},
		{"the kernel refused the Landlock rules", "could not confine the program: the kernel refused"},
	} {
		if err := statusError([]byte(c.msg)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%q: %v, want %q", c.msg, err, c.want)
		}
	}
}

// TestSIGSYSHintOnlyWhenConfined: a program killed by SIGSYS outside ovid's
// confinement is not told that ovid's confinement did it.
func TestSIGSYSHintOnlyWhenConfined(t *testing.T) {
	m, err := module.Load(mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n")))
	if err != nil {
		t.Fatal(err)
	}
	pr := procResult{signal: syscall.SIGSYS}
	r := map[string]any{}
	describeCrash(m, nil, nil, pr, false, r)
	if r["hint"] != nil {
		t.Fatalf("unconfined: %v", r)
	}
	describeCrash(m, nil, nil, pr, true, r)
	if r["hint"] == nil {
		t.Fatalf("confined: %v", r)
	}
}

// TestConfineRelativeTempDir: TMPDIR may be relative, and the staged program
// and the writable directory then are too; the launcher changes directory
// before it uses either, so both must have been made absolute.
func TestConfineRelativeTempDir(t *testing.T) {
	needExec(t)
	dir := mkmod(t, demo(writerProg))
	base := t.TempDir()
	os.Mkdir(filepath.Join(base, "rel"), 0o755)
	wd, _ := os.Getwd()
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	t.Setenv("TMPDIR", "rel")
	var b bytes.Buffer
	if code := RunWith(dir, []string{filepath.Join(dir, "demo", "leak.txt")}, RunOpts{JSON: true}, &b); code != 0 {
		t.Fatalf("exit %d: %s", code, b.String())
	}
	r := last(t, b.String())
	if r["exit"] != float64(0) || r["confined"] == nil {
		t.Fatalf("%v", r)
	}
	if w, _ := r["writable"].(string); w == "" || !filepath.IsAbs(w) {
		t.Fatalf("writable %q: want an absolute path under rel/", w)
	} else {
		os.RemoveAll(w)
	}
}
