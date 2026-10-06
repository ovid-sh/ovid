package tool

import (
	"runtime"
	"syscall"
	"unsafe"
)

const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446
	landlockRulesetVersion   = 1 // LANDLOCK_CREATE_RULESET_VERSION
	landlockRulePathBeneath  = 1

	// LANDLOCK_ACCESS_FS_*: what a confined program may not do outside its
	// writable directory. Reads and execution are not handled, so they are
	// allowed everywhere.
	llExecute    = 1 << 0
	llWriteFile  = 1 << 1
	llRemoveDir  = 1 << 4
	llRemoveFile = 1 << 5
	llMakeChar   = 1 << 6
	llMakeDir    = 1 << 7
	llMakeReg    = 1 << 8
	llMakeSock   = 1 << 9
	llMakeFifo   = 1 << 10
	llMakeBlock  = 1 << 11
	llMakeSym    = 1 << 12
	llRefer      = 1 << 13 // ABI 2
	llTruncate   = 1 << 14 // ABI 3

	oPath              = 0x200000
	sysSeccomp         = 317
	seccompSetFilter   = 1
	prSetNoNewPrivs    = 38
	bpfLdAbs           = 0x20
	bpfJeq             = 0x15
	bpfRet             = 0x06
	seccompRetAllow    = 0x7fff0000
	seccompRetKillProc = 0x80000000
	auditArchX8664     = 0xc000003e
)

// confineSupported: programs are linux/amd64 binaries, and so are the
// seccomp filter and Landlock that confine them.
const confineSupported = true

// landlockAvailable reports whether this kernel has Landlock: the ABI query
// succeeds.
func landlockAvailable() bool {
	return landlockABI() > 0
}

func landlockABI() int {
	r, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, 0, 0, landlockRulesetVersion)
	if e != 0 {
		return 0
	}
	return int(r)
}

// writeRights are the file system rights to handle, for a kernel of the
// given Landlock ABI: every way of changing the file system, and execution,
// which only the program's own file keeps.
func writeRights(abi int) uint64 {
	r := uint64(llExecute | llWriteFile | llRemoveDir | llRemoveFile | llMakeChar | llMakeDir | llMakeReg | llMakeSock | llMakeFifo | llMakeBlock | llMakeSym)
	if abi >= 2 {
		r |= llRefer
	}
	if abi >= 3 {
		r |= llTruncate
	}
	return r
}

// confineAndExec confines this process as c says and replaces it with bin.
// A failure to set up is written to c.StatusFD, for the parent to report as
// its own, and ends the process: 112 if the kernel refuses a rule, 113 if
// the filter cannot be installed, 114 if execve fails.
func confineAndExec(c *confineSpec, bin string, argv []string) {
	runtime.LockOSThread()
	// The status pipe closes on exec: the parent reads nothing when the
	// program started.
	syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(c.StatusFD), syscall.F_SETFD, syscall.FD_CLOEXEC)
	fail := func(code int, what string) {
		syscall.Write(c.StatusFD, []byte(what))
		syscall.RawSyscall(syscall.SYS_EXIT_GROUP, uintptr(code), 0, 0)
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		fail(112, "no_new_privs refused: "+e.Error())
	}
	// The program must be there before the rules are in place.
	if err := syscall.Access(bin, 1); err != nil {
		fail(114, "cannot execute "+bin+": "+err.Error())
	}
	if c.Landlock {
		if !restrictFS(c.Writable, bin) {
			fail(112, "the kernel refused the Landlock rules")
		}
	}
	path, _ := syscall.BytePtrFromString(bin)
	// The filter: this architecture, then each allowed number, then the
	// launcher's own needs: exit_group for a failed execve (the same as the
	// exit every list has), and execve itself only with the path at the
	// address the launcher holds it at. The filter outlives execve, but a program
	// cannot place a string at an address of its launcher's heap, so the
	// one execve it inherits is unusable to it; where the kernel has
	// Landlock, nothing but its own file is executable anyway. Anything else
	// kills the process.
	type insn struct {
		code   uint16
		jt, jf uint8
		k      uint32
	}
	prog := []insn{{bpfLdAbs, 0, 0, 4}, {bpfJeq, 1, 0, auditArchX8664}, {bpfRet, 0, 0, seccompRetKillProc}, {bpfLdAbs, 0, 0, 0}}
	allow := append([]int64{syscall.SYS_EXIT_GROUP}, c.Syscalls...)
	for _, n := range allow {
		prog = append(prog, insn{bpfJeq, 0, 1, uint32(n)}, insn{bpfRet, 0, 0, seccompRetAllow})
	}
	ptr := uint64(uintptr(unsafe.Pointer(path)))
	prog = append(prog,
		insn{bpfJeq, 0, 5, syscall.SYS_EXECVE}, // not execve: kill
		insn{bpfLdAbs, 0, 0, 16},               // args[0], low word
		insn{bpfJeq, 0, 3, uint32(ptr)},
		insn{bpfLdAbs, 0, 0, 20}, // args[0], high word
		insn{bpfJeq, 0, 1, uint32(ptr >> 32)},
		insn{bpfRet, 0, 0, seccompRetAllow},
		insn{bpfRet, 0, 0, seccompRetKillProc})
	fprog := struct {
		n uint16
		f *insn
	}{uint16(len(prog)), &prog[0]}
	var av []*byte
	for _, a := range argv {
		p, _ := syscall.BytePtrFromString(a)
		av = append(av, p)
	}
	av = append(av, nil)
	// The program gets no environment, as it does when run unconfined.
	env := []*byte{nil}
	// From here to execve only raw system calls: nothing else may run on
	// this thread once the filter is in place.
	// "+" says the rules are in place and the filter and execve are next;
	// written before the filter, which may not allow a write. It is the
	// last thing the parent reads when the program started. "!" after it
	// says execve itself failed (written under the filter: every receipt
	// has write, for the startup code's own message).
	plus, bang := [1]byte{'+'}, [1]byte{'!'}
	syscall.RawSyscall(syscall.SYS_WRITE, uintptr(c.StatusFD), uintptr(unsafe.Pointer(&plus[0])), 1)
	if _, _, e := syscall.RawSyscall(sysSeccomp, seccompSetFilter, 0, uintptr(unsafe.Pointer(&fprog))); e != 0 {
		fail(113, "!the kernel refused the seccomp filter: "+e.Error())
	}
	syscall.RawSyscall(syscall.SYS_EXECVE, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&av[0])), uintptr(unsafe.Pointer(&env[0])))
	syscall.RawSyscall(syscall.SYS_WRITE, uintptr(c.StatusFD), uintptr(unsafe.Pointer(&bang[0])), 1)
	syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 114, 0, 0)
}

// restrictFS makes the file system read-only for this process except under
// dir, or everywhere when dir is "", and executable only at bin. It reports
// whether the kernel took the rules.
func restrictFS(dir, bin string) bool {
	abi := landlockABI()
	if abi <= 0 {
		return false
	}
	rights := writeRights(abi)
	attr := struct{ handledAccessFS uint64 }{rights}
	rs, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return false
	}
	// A rule: these rights at and under path. struct landlock_path_beneath_attr
	// is packed: 8 bytes of rights, then a 4-byte descriptor.
	rule := func(path string, access uint64, flags int) bool {
		fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC|flags, 0)
		if err != nil {
			return false
		}
		var pb [12]byte
		*(*uint64)(unsafe.Pointer(&pb[0])) = access
		*(*int32)(unsafe.Pointer(&pb[8])) = int32(fd)
		_, _, e := syscall.RawSyscall6(sysLandlockAddRule, rs, landlockRulePathBeneath, uintptr(unsafe.Pointer(&pb[0])), 0, 0, 0)
		syscall.Close(fd)
		return e == 0
	}
	if dir != "" && !rule(dir, rights&^llExecute, syscall.O_DIRECTORY) {
		return false
	}
	// Execution only of the program's own file. A program held in memory
	// (/proc/self/fd/N) is on no mounted file system and Landlock cannot
	// name it: then execution is not handled, and the seccomp filter's
	// pinned execve is what keeps the program from running anything.
	if !rule(bin, llExecute, 0) {
		syscall.Close(int(rs))
		attr.handledAccessFS = rights &^ llExecute
		if rs, _, e = syscall.RawSyscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0); e != 0 {
			return false
		}
		if dir != "" && !rule(dir, rights&^llExecute, syscall.O_DIRECTORY) {
			return false
		}
	}
	_, _, e = syscall.RawSyscall(sysLandlockRestrictSelf, rs, 0, 0)
	syscall.Close(int(rs))
	return e == 0
}
