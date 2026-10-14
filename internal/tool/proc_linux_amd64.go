package tool

import (
	"encoding/binary"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	ptraceGetSiginfo = 0x4202
	ptraceExitKill   = 0x100000
	ptraceTraceExec  = 0x10
	ptraceEventExec  = 4
)

// runTraced runs bin under ptrace. On a fatal signal it records the
// faulting pc, the frame-pointer chain, and the fault address, then lets
// the signal kill the process as usual. ok is false if tracing is not
// allowed here, so the caller can run the program plainly.
func runTraced(bin string, args []string, pio procIO, timeout time.Duration) (r procResult, ok bool) {
	// Every ptrace request must come from the thread that started the tracee.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd, err := pio.command(bin, args)
	if err != nil {
		return procResult{err: err}, true
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
	if err := cmd.Start(); err != nil {
		return r, false
	}
	defer cmd.Process.Release()
	pid := cmd.Process.Pid
	var timedOut atomic.Bool
	if timeout > 0 {
		t := time.AfterFunc(timeout, func() {
			timedOut.Store(true)
			syscall.Kill(pid, syscall.SIGKILL)
		})
		defer t.Stop()
	}
	started := false
	for {
		var ws syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
			if err == syscall.EINTR {
				continue
			}
			r.err = err
			return r, true
		}
		switch {
		case ws.Exited():
			r.exited, r.code = true, ws.ExitStatus()
			return r, true
		case ws.Signaled():
			r.signal, r.timedOut = ws.Signal(), timedOut.Load()
			return r, true
		case ws.Stopped():
			sig := ws.StopSignal()
			if !started && sig == syscall.SIGTRAP {
				// The stop after exec. A confined program execs twice, the
				// launcher and then itself; the second stop is marked as an
				// exec event and is not a signal to deliver.
				started = true
				syscall.PtraceSetOptions(pid, ptraceExitKill|ptraceTraceExec)
				sig = 0
			} else if sig == syscall.SIGTRAP && ws.TrapCause() == ptraceEventExec {
				sig = 0
			} else if fatalSignal(sig) && r.pc == 0 {
				capture(pid, sig, &r)
			}
			syscall.PtraceCont(pid, int(sig))
		}
	}
}

func capture(pid int, sig syscall.Signal, r *procResult) {
	var regs syscall.PtraceRegs
	if syscall.PtraceGetRegs(pid, &regs) != nil {
		return
	}
	r.pc = regs.Rip
	if sig == syscall.SIGSEGV || sig == syscall.SIGBUS {
		var si [128]byte
		_, _, e := syscall.Syscall6(syscall.SYS_PTRACE, ptraceGetSiginfo, uintptr(pid), 0, uintptr(unsafe.Pointer(&si[0])), 0, 0)
		if e == 0 {
			r.addr, r.hasAddr = binary.LittleEndian.Uint64(si[16:]), true
		}
	}
	// Every Ovid func keeps rbp as a frame pointer: [rbp] is the caller's
	// rbp and [rbp+8] the return address.
	bp := regs.Rbp
	for i := 0; i < 64 && bp != 0; i++ {
		var b [16]byte
		if n, err := syscall.PtracePeekData(pid, uintptr(bp), b[:]); err != nil || n < 16 {
			break
		}
		next, ret := binary.LittleEndian.Uint64(b[:8]), binary.LittleEndian.Uint64(b[8:])
		r.frames = append(r.frames, ret)
		if next <= bp {
			break
		}
		bp = next
	}
}

// signalHint explains a death by a signal only confinement sends.
func signalHint(sig syscall.Signal) string {
	if sig == syscall.SIGSYS {
		return confineHint
	}
	return ""
}
