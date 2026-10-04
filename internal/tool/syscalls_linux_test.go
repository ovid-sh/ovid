//go:build linux && amd64

package tool

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
)

// sysCall is one system call a traced program made: its number and its
// first four arguments.
type sysCall struct {
	nr   uint64
	args [4]uint64
}

// traceSyscalls runs bin under ptrace and returns every system call it
// makes, in order, and its exit code. The exec that starts it is not
// included.
func traceSyscalls(t *testing.T, bin string, args ...string) ([]sysCall, int) {
	t.Helper()
	// Every ptrace request must come from the thread that started the tracee.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("ptrace is not allowed here: %v", err)
	}
	defer cmd.Process.Release()
	pid := cmd.Process.Pid
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil || !ws.Stopped() {
		t.Fatalf("no stop after exec: %v", err)
	}
	// With TRACESYSGOOD a syscall stop is SIGTRAP|0x80, so it cannot be
	// taken for a signal.
	const exitKill = 0x100000
	if err := syscall.PtraceSetOptions(pid, syscall.PTRACE_O_TRACESYSGOOD|exitKill); err != nil {
		t.Fatal(err)
	}
	var calls []sysCall
	entering := true
	sig := 0
	for {
		if err := syscall.PtraceSyscall(pid, sig); err != nil {
			t.Fatal(err)
		}
		if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
			t.Fatal(err)
		}
		switch {
		case ws.Exited():
			return calls, ws.ExitStatus()
		case ws.Signaled():
			t.Fatalf("%s was killed by %v after %d calls", filepath.Base(bin), ws.Signal(), len(calls))
		case ws.Stopped() && ws.StopSignal() == syscall.SIGTRAP|0x80:
			sig = 0
			if entering {
				var r syscall.PtraceRegs
				if err := syscall.PtraceGetRegs(pid, &r); err != nil {
					t.Fatal(err)
				}
				calls = append(calls, sysCall{r.Orig_rax, [4]uint64{r.Rdi, r.Rsi, r.Rdx, r.R10}})
			}
			entering = !entering
		case ws.Stopped():
			sig = int(ws.StopSignal())
		}
	}
}

// TestHeapIsNotReserved checks how a program asks for its heap: the first
// region the startup code maps, a block big enough for a mapping of its own,
// and a second region Alloc grows into. A host refuses a reserved mapping
// larger than its memory, so a reserved 128 MiB region keeps every program
// from starting in a small sandbox. That refusal cannot be staged on a
// development machine; what the program asks the kernel for can be observed
// anywhere.
func TestHeapIsNotReserved(t *testing.T) {
	const (
		sysMmap      = 9
		mapPrivate   = 0x02
		mapAnonymous = 0x20
		mapNoReserve = 0x4000
	)
	dir := mkmod(t, demo(`package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var i i64 = 0
  while i < 130 {
    if i == 127 {
      ovid/io.Alloc(io, 100 << 20)
    }
    ovid/io.Alloc(io, 1 << 20)
    i = i + 1
  }
  return 3
}
`))
	calls, code := traceSyscalls(t, mustBuild(t, dir))
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	var sizes []uint64
	for _, c := range calls {
		if c.nr != sysMmap {
			continue
		}
		sizes = append(sizes, c.args[1])
		if flags := c.args[3]; flags != mapPrivate|mapAnonymous|mapNoReserve {
			t.Errorf("mmap of %d bytes: flags %#x, want MAP_PRIVATE|MAP_ANONYMOUS|MAP_NORESERVE (%#x)",
				c.args[1], flags, mapPrivate|mapAnonymous|mapNoReserve)
		}
	}
	if want := []uint64{128 << 20, 100 << 20, 128 << 20}; !slices.Equal(sizes, want) {
		t.Fatalf("mmap sizes %v, want %v: the startup region, the large block, and a second region", sizes, want)
	}
}
