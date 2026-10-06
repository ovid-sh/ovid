package tool

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// buildSyscalls builds the module and returns the program's path and the
// system calls its receipt lists.
func buildSyscalls(t *testing.T, dir string) (string, []int) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "p")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build: %s", b.String())
	}
	var r struct {
		Syscalls []int
		Unknown  *int `json:"syscalls_unknown"`
	}
	if err := json.Unmarshal(b.Bytes(), &r); err != nil || r.Syscalls == nil || r.Unknown != nil {
		t.Fatalf("receipt: %v: %s", err, b.String())
	}
	return bin, r.Syscalls
}

// jailed runs bin under a seccomp filter that allows only the listed system
// calls (and the execve that starts it) and kills the process on any other.
// It returns the program's stdout and how it ended.
func jailed(t *testing.T, bin string, allow []int, args ...string) (string, syscall.WaitStatus) {
	t.Helper()
	var list []string
	for _, n := range allow {
		list = append(list, strconv.Itoa(n))
	}
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperJail$", "--", bin}, args...)...)
	// The helper makes three raw system calls in a row; a preemption signal
	// between them would have the Go runtime make one the filter kills.
	cmd.Env = append(os.Environ(), "OVID_HELPER_JAIL="+strings.Join(list, ","), "GODEBUG=asyncpreemptoff=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Run()
	return out.String(), cmd.ProcessState.Sys().(syscall.WaitStatus)
}

// TestHelperJail is the child of jailed; alone it does nothing. It installs
// the filter on this thread and replaces itself with the program.
func TestHelperJail(t *testing.T) {
	spec := os.Getenv("OVID_HELPER_JAIL")
	if spec == "" {
		t.Skip("run by jailed")
	}
	argv := os.Args[len(os.Args)-1:]
	for i, a := range os.Args {
		if a == "--" {
			argv = os.Args[i+1:]
			break
		}
	}
	type insn struct {
		code   uint16
		jt, jf uint8
		k      uint32
	}
	const (
		ldAbs, jeq, ret       = 0x20, 0x15, 0x06
		retAllow, retKill     = 0x7fff0000, 0x80000000 // SECCOMP_RET_ALLOW, _KILL_PROCESS
		archX8664             = 0xc000003e
		offNr, offArch        = 0, 4
		prSetNoNewPrivs       = 38
		sysSeccomp, setFilter = 317, 1
	)
	prog := []insn{{ldAbs, 0, 0, offArch}, {jeq, 1, 0, archX8664}, {ret, 0, 0, retKill}, {ldAbs, 0, 0, offNr},
		{jeq, 0, 1, syscall.SYS_EXECVE}, {ret, 0, 0, retAllow}}
	for _, f := range strings.Split(spec, ",") {
		n, err := strconv.Atoi(f)
		if err != nil {
			os.Exit(111)
		}
		prog = append(prog, insn{jeq, 0, 1, uint32(n)}, insn{ret, 0, 0, retAllow})
	}
	prog = append(prog, insn{ret, 0, 0, retKill})
	fprog := struct {
		n uint16
		f *insn
	}{uint16(len(prog)), &prog[0]}
	path, _ := syscall.BytePtrFromString(argv[0])
	var av []*byte
	for _, a := range argv {
		p, _ := syscall.BytePtrFromString(a)
		av = append(av, p)
	}
	av = append(av, nil)
	env := []*byte{nil}
	runtime.LockOSThread()
	// From here to execve only raw system calls: nothing else may run on
	// this thread once the filter is in place.
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		os.Exit(112)
	}
	if _, _, e := syscall.RawSyscall(sysSeccomp, setFilter, 0, uintptr(unsafe.Pointer(&fprog))); e != 0 {
		syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 113, 0, 0)
	}
	syscall.RawSyscall(syscall.SYS_EXECVE, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&av[0])), uintptr(unsafe.Pointer(&env[0])))
	syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 114, 0, 0)
}

// TestBuildReportsSyscalls: build's receipt lists the system calls the
// program can make. The list is enough to run it under a filter that allows
// nothing else, and it is not padded: without one of them the program is
// killed.
func TestBuildReportsSyscalls(t *testing.T) {
	needExec(t)
	hello := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  ovid/io.Print(strptr(\"hi\\n\"))\n  return 3\n}\n"))
	bin, calls := buildSyscalls(t, hello)
	if want := []int{1, 9, 60}; !reflect.DeepEqual(calls, want) { // write, mmap, exit
		t.Fatalf("a program that prints lists %v, want %v", calls, want)
	}
	// Only the helper's own failure to install a filter is a reason to skip.
	// Any other end, a death by SIGSYS above all, is the list being wrong.
	if out, ws := jailed(t, bin, calls); ws.Exited() && (ws.ExitStatus() == 112 || ws.ExitStatus() == 113) {
		t.Skipf("no seccomp filter here: the helper exited %d", ws.ExitStatus())
	} else if out != "hi\n" || !ws.Exited() || ws.ExitStatus() != 3 {
		t.Fatalf("under its own list: %q, exited %v code %d, signal %v", out, ws.Exited(), ws.ExitStatus(), ws.Signal())
	}
	if _, ws := jailed(t, bin, []int{9, 60}); !ws.Signaled() || ws.Signal() != syscall.SIGSYS {
		t.Fatalf("without write on the list: %v, want death by SIGSYS", ws)
	}

	// A program that reads a file can make more, and only what it reaches:
	// ReadFile's calls, not the ones for writing files.
	reader := mkmod(t, demo(`package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var n i64 = ovid/io.Alloc(io, 8)
  var p i64, e i64 = ovid/io.ReadFile(io, ovid/io.Arg(io, 1), ovid/io.CLen(ovid/io.Arg(io, 1)), n)
  if e != 0 {
    return 1
  }
  ovid/io.Stdout(p, load64(n))
  return 0
}
`))
	rbin, rcalls := buildSyscalls(t, reader)
	has := func(n int) bool {
		for _, c := range rcalls {
			if c == n {
				return true
			}
		}
		return false
	}
	if !has(syscall.SYS_OPENAT) || !has(syscall.SYS_READ) || has(syscall.SYS_RENAME) || has(syscall.SYS_FSYNC) || has(syscall.SYS_UNLINK) {
		t.Fatalf("a program that reads a file lists %v", rcalls)
	}
	f := filepath.Join(t.TempDir(), "f")
	os.WriteFile(f, []byte("content\n"), 0o644)
	if out, ws := jailed(t, rbin, rcalls, f); out != "content\n" || !ws.Exited() || ws.ExitStatus() != 0 {
		t.Fatalf("the reader under its own list: %q, %v", out, ws)
	}

	// Both compilers say the same, and the self-hosted compiler builds
	// itself under the list of its own receipt.
	prog := filepath.Join(repo(t), "prog")
	self, selfCalls := buildSyscalls(t, prog)
	stdDir := filepath.Join(repo(t), "std")
	for _, dir := range []string{hello, reader, prog} {
		_, want := buildSyscalls(t, dir)
		out, code := run(t, self, "build", dir, "-o", filepath.Join(t.TempDir(), "x"), "--std", stdDir)
		var r struct{ Syscalls []int }
		if json.Unmarshal([]byte(out), &r) != nil || code != 0 || !reflect.DeepEqual(r.Syscalls, want) {
			t.Fatalf("%s: self-hosted says %s, the Go compiler %v", dir, out, want)
		}
	}
	again := filepath.Join(t.TempDir(), "again")
	if out, ws := jailed(t, self, selfCalls, "build", prog, "-o", again, "--std", stdDir); !ws.Exited() || ws.ExitStatus() != 0 {
		t.Fatalf("the self-hosted compiler under its own list: %v %s", ws, out)
	}
	a, _ := os.ReadFile(self)
	b, _ := os.ReadFile(again)
	if len(a) == 0 || !bytes.Equal(a, b) {
		t.Fatalf("built under the filter: %d bytes, want the same %d", len(b), len(a))
	}
}
