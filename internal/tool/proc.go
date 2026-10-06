package tool

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"

	"ovid/internal/compile"
	"ovid/internal/elf"
	"ovid/internal/module"
)

// procResult is how a program ended. pc, frames, and addr are set
// when it was traced and died of a fault.
type procResult struct {
	exited   bool
	code     int
	signal   syscall.Signal
	timedOut bool
	pc       uint64   // where the fault happened
	frames   []uint64 // return addresses, innermost first
	addr     uint64   // the faulting address, for SIGSEGV and SIGBUS
	hasAddr  bool
	err      error
}

// procIO is a program's stdio and any further files; a nil stdio field is
// /dev/null. A program gets these and its arguments, and no environment.
type procIO struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	extra          []*os.File // fd 3 and up
	argv0          string     // the program's name for itself, when it is not bin
	confine        *confineSpec
}

func (pio procIO) command(bin string, args []string) *exec.Cmd {
	var cmd *exec.Cmd
	if pio.confine != nil {
		// ovid itself, as the launcher: it confines the process and then
		// becomes the program. It gets the spec and nothing else of ours;
		// GODEBUG keeps the Go runtime from sending itself a signal between
		// installing the filter and execve.
		self, err := os.Executable()
		if err != nil {
			self = os.Args[0]
		}
		c := *pio.confine
		c.argv0 = pio.argv0
		if c.argv0 == "" {
			c.argv0 = bin
		}
		cmd = exec.Command(self, append([]string{"--", bin}, args...)...)
		cmd.Env = []string{confineEnv + "=" + c.encode(), "GODEBUG=asyncpreemptoff=1"}
		cmd.Dir = c.writable
	} else {
		cmd = exec.Command(bin, args...)
		// An empty environment, not ovid's own: the kernel puts the
		// environment on the stack after argv, where any program can read
		// it, and the caller's often holds secrets.
		cmd.Env = []string{}
		if pio.argv0 != "" {
			cmd.Args[0] = pio.argv0
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pio.stdin, pio.stdout, pio.stderr
	cmd.ExtraFiles = pio.extra
	return cmd
}

// runProc runs bin, traced where the platform allows so a fault can be
// traced back to a statement. A timeout of 0 means none.
func runProc(bin string, args []string, pio procIO, timeout time.Duration) procResult {
	if r, ok := runTraced(bin, args, pio, timeout); ok {
		return r
	}
	return runPlain(bin, args, pio, timeout)
}

func runPlain(bin string, args []string, pio procIO, timeout time.Duration) procResult {
	cmd := pio.command(bin, args)
	if err := cmd.Start(); err != nil {
		return procResult{err: err}
	}
	timedOut := false
	if timeout > 0 {
		t := time.AfterFunc(timeout, func() { cmd.Process.Kill() })
		cmd.Wait()
		timedOut = !t.Stop()
	} else {
		cmd.Wait()
	}
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		return procResult{signal: ws.Signal(), timedOut: timedOut}
	}
	return procResult{exited: true, code: ws.ExitStatus()}
}

func fatalSignal(s syscall.Signal) bool {
	return s == syscall.SIGSEGV || s == syscall.SIGBUS || s == syscall.SIGFPE || s == syscall.SIGILL
}

// crashSite maps a code address to the func or statement compiled there.
func crashSite(marks []compile.Mark, pc uint64) string {
	base := elf.CodeVAddr()
	if pc < base || len(marks) == 0 {
		return ""
	}
	off := int(pc - base)
	i := sort.Search(len(marks), func(i int) bool { return marks[i].Off > off }) - 1
	if i < 0 {
		return ""
	}
	return marks[i].ID
}

// crashStack describes where a traced fault happened, innermost first: the
// statement that faulted, then each call that led to it. Frames outside the
// module (the test harness, startup) are left out.
func crashStack(m *module.Module, marks []compile.Mark, r procResult) []map[string]any {
	pcs := []uint64{r.pc}
	for _, ret := range r.frames {
		pcs = append(pcs, ret-1) // inside the call, not after it
	}
	var out []map[string]any
	for _, pc := range pcs {
		id := crashSite(marks, pc)
		l := m.Index()[id]
		if l == nil {
			continue
		}
		file, a, _, src := m.Where(l.Span)
		f := map[string]any{"id": id, "file": file, "line": a.Line}
		if l.Kind == "stmt" {
			f["source"] = src
		}
		out = append(out, f)
	}
	return out
}

// describeCrash adds to r what is known about a program killed by a
// signal: the signal, the statement it died in and the calls that led
// there, the faulting address, and a hint for the common causes. exe is the
// program's image.
func describeCrash(m *module.Module, exe []byte, marks []compile.Mark, pr procResult, r map[string]any) {
	r["signal"] = pr.signal.String()
	if st := crashStack(m, marks, pr); len(st) > 0 {
		r["at"] = st[0]
		r["stack"] = st
	}
	if pr.signal == syscall.SIGFPE {
		r["hint"] = "an integer / or % by zero (or the most negative i64 / -1)"
	}
	if pr.signal == syscall.SIGSYS {
		r["hint"] = confineHint
	}
	if pr.hasAddr {
		r["fault_addr"] = fmt.Sprintf("%#x", pr.addr)
		if lo, hi := elf.Rodata(exe); pr.addr < 4096 {
			r["hint"] = "a load or store through a null pointer (or a field of one): check for 0 as *T before use"
		} else if pr.addr >= lo && pr.addr < hi {
			r["hint"] = "a store into a string literal, which is read-only: copy it into ovid/io.Alloc memory (ovid/mem.Copy) and write there"
		}
	}
}
