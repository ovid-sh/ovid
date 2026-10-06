//go:build linux && amd64

package tool

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
)

// sysCall is one system call a traced program made: its number, its
// first four arguments, and its result (0 for a call that never returns,
// like exit).
type sysCall struct {
	nr   uint64
	args [4]uint64
	ret  int64
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
			var r syscall.PtraceRegs
			if err := syscall.PtraceGetRegs(pid, &r); err != nil {
				t.Fatal(err)
			}
			if entering {
				calls = append(calls, sysCall{nr: r.Orig_rax, args: [4]uint64{r.Rdi, r.Rsi, r.Rdx, r.R10}})
			} else {
				calls[len(calls)-1].ret = int64(r.Rax)
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
	// The large block carries Alloc's 16-byte mapping header.
	if want := []uint64{128 << 20, 100<<20 + 16, 128 << 20}; !slices.Equal(sizes, want) {
		t.Fatalf("mmap sizes %v, want %v: the startup region, the large block, and a second region", sizes, want)
	}
}

// TestResetHeapUnmaps: a reset gives back every mapping Alloc took after
// the mark, the blocks with mappings of their own and the regions it grew
// into, so a host that resets between requests keeps only its first region.
func TestResetHeapUnmaps(t *testing.T) {
	const (
		sysMmap   = 9
		sysMunmap = 11
	)
	dir := mkmod(t, demo(`package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var m *ovid/io.HeapMark = ovid/io.MarkHeap(io)
  var r i64 = 0
  while r < 3 {
    var i i64 = 0
    while i < 200 {
      if i == 50 {
        ovid/io.Alloc(io, 100 << 20)
      }
      ovid/io.Alloc(io, 1 << 20)
      i = i + 1
    }
    ovid/io.ResetHeap(io, m)
    r = r + 1
  }
  return 3
}
`))
	calls, code := traceSyscalls(t, mustBuild(t, dir))
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	// live is every mapping not yet given back, by address: its length.
	live := map[uint64]uint64{}
	var sizes []uint64
	unmaps := 0
	for _, c := range calls {
		switch c.nr {
		case sysMmap:
			if c.ret < 0 {
				t.Fatalf("mmap of %d bytes failed: %d", c.args[1], c.ret)
			}
			live[uint64(c.ret)] = c.args[1]
			sizes = append(sizes, c.args[1])
		case sysMunmap:
			if c.ret != 0 {
				t.Fatalf("munmap(%#x, %d) failed: %d", c.args[0], c.args[1], c.ret)
			}
			if n, ok := live[c.args[0]]; !ok || n != c.args[1] {
				t.Fatalf("munmap(%#x, %d) is not a whole mapping the program took", c.args[0], c.args[1])
			}
			delete(live, c.args[0])
			unmaps++
		}
	}
	// Three requests' worth of a large block and a second region.
	want := []uint64{128 << 20, 100<<20 + 16, 128 << 20, 100<<20 + 16, 128 << 20, 100<<20 + 16, 128 << 20}
	if !slices.Equal(sizes, want) {
		t.Fatalf("mmap sizes %v, want the startup region and then %v", sizes, want[1:])
	}
	// Every mapping but the startup region is given back.
	if unmaps != len(want)-1 || len(live) != 1 {
		t.Fatalf("%d munmaps, %d mappings left, want %d and only the startup region", unmaps, len(live), len(want)-1)
	}
	for _, n := range live {
		if n != 128<<20 {
			t.Fatalf("the mapping left is %d bytes, want the startup region", n)
		}
	}
}

// TestOutOfMemoryAtStartup stages the refusal of the heap's first region
// with an address-space limit, which refuses the mapping whatever its
// flags: the program says so and exits 71, in both compilers' output.
func TestOutOfMemoryAtStartup(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to set the limit with")
	}
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	goBin := mustBuild(t, dir)
	g1 := mustBuild(t, filepath.Join(repo(t), "prog"))
	selfBin := filepath.Join(t.TempDir(), "self")
	if out, code := run(t, g1, "build", dir, "-o", selfBin, "--std", filepath.Join(repo(t), "std")); code != 0 {
		t.Fatalf("self-hosted build %d: %s", code, out)
	}
	for _, bin := range []string{goBin, selfBin} {
		if out, code := run(t, bin); code != 0 || out != "" {
			t.Fatalf("%s without a limit: exit %d %q", bin, code, out)
		}
		// ulimit -v counts KiB: 64 MiB of address space, half a region.
		cmd := exec.Command(sh, "-c", `ulimit -v 65536 && exec "$0"`, bin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		ee, _ := err.(*exec.ExitError)
		if ee == nil || ee.ExitCode() != 71 || stderr.String() != "out of memory\n" || stdout.Len() != 0 {
			t.Fatalf("%s under the limit: %v, stdout %q, stderr %q; want exit 71 and \"out of memory\\n\" on stderr", bin, err, stdout.String(), stderr.String())
		}
	}
}

// TestTestReportsOutOfMemory: a test the runtime ended for want of memory
// is reported as that, and no return statement of the test is blamed. A
// test that prints the runtime's message and returns its code is not.
func TestTestReportsOutOfMemory(t *testing.T) {
	dir := mkmod(t, demo(`package demo
import ovid/io
func TestHuge(io *ovid/io.Cap) i64 {
  var p i64 = ovid/io.Alloc(io, 1 << 47)
  return p & 1
}
func TestReturns71(io *ovid/io.Cap) i64 {
  ovid/io.Eprint(strptr("out of memory\n"))
  return 71
}
func main(io *ovid/io.Cap) i64 {
  return 0
}
`))
	var b bytes.Buffer
	if code := Test(dir, "", false, &b); code != ExitFail {
		t.Fatalf("exit %d:\n%s", code, b.String())
	}
	rs := lines(t, b.String())
	if len(rs) != 3 {
		t.Fatalf("want two tests and a summary:\n%s", b.String())
	}
	huge, plain := rs[0], rs[1]
	if huge["id"] != "fn:demo.TestHuge" || huge["error"] != "out_of_memory" || huge["exit"] != float64(71) || huge["returned_by"] != nil || huge["ok"] != false {
		t.Fatalf("TestHuge: %v", huge)
	}
	// The same code from a return statement is an ordinary failure, even
	// with the runtime's message in its output.
	if plain["id"] != "fn:demo.TestReturns71" || plain["error"] != nil || plain["returned_by"] == nil {
		t.Fatalf("TestReturns71: %v", plain)
	}
}
