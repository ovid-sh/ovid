package tool

import (
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"

	"ovid/internal/compile"
	"ovid/internal/elf"
	"ovid/internal/module"
)

// procResult is how a test process ended. pc, frames, and addr are set
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

// runProc runs bin with its output in out, traced where the platform allows
// so a fault can be traced back to a statement.
func runProc(bin string, args []string, out *os.File, timeout time.Duration) procResult {
	if r, ok := runTraced(bin, args, out, timeout); ok {
		return r
	}
	return runPlain(bin, args, out, timeout)
}

func runPlain(bin string, args []string, out *os.File, timeout time.Duration) procResult {
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return procResult{err: err}
	}
	t := time.AfterFunc(timeout, func() { cmd.Process.Kill() })
	cmd.Wait()
	timedOut := !t.Stop()
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
		l := m.Index[id]
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
