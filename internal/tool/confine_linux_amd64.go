package tool

import (
	"os"
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
// given Landlock ABI.
func writeRights(abi int) uint64 {
	r := uint64(llWriteFile | llRemoveDir | llRemoveFile | llMakeChar | llMakeDir | llMakeReg | llMakeSock | llMakeFifo | llMakeBlock | llMakeSym)
	if abi >= 2 {
		r |= llRefer
	}
	if abi >= 3 {
		r |= llTruncate
	}
	return r
}

// confineAndExec confines this process as c says and replaces it with bin.
// It exits with 112 if the kernel refuses a rule, 113 if the filter cannot
// be installed, and returns only if execve fails.
func confineAndExec(c *confineSpec, bin string, argv []string) {
	runtime.LockOSThread()
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		os.Exit(112)
	}
	if c.landlock {
		if !restrictFS(c.writable) {
			os.Exit(112)
		}
	}
	// The filter: this architecture, then each allowed number, then execve
	// for the step that follows; anything else kills the process. The
	// program itself has no execve unless its list says so.
	type insn struct {
		code   uint16
		jt, jf uint8
		k      uint32
	}
	prog := []insn{{bpfLdAbs, 0, 0, 4}, {bpfJeq, 1, 0, auditArchX8664}, {bpfRet, 0, 0, seccompRetKillProc}, {bpfLdAbs, 0, 0, 0}}
	allow := append([]int64{syscall.SYS_EXECVE}, c.syscalls...)
	for _, n := range allow {
		prog = append(prog, insn{bpfJeq, 0, 1, uint32(n)}, insn{bpfRet, 0, 0, seccompRetAllow})
	}
	prog = append(prog, insn{bpfRet, 0, 0, seccompRetKillProc})
	fprog := struct {
		n uint16
		f *insn
	}{uint16(len(prog)), &prog[0]}
	path, _ := syscall.BytePtrFromString(bin)
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
	if _, _, e := syscall.RawSyscall(sysSeccomp, seccompSetFilter, 0, uintptr(unsafe.Pointer(&fprog))); e != 0 {
		syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 113, 0, 0)
	}
	syscall.RawSyscall(syscall.SYS_EXECVE, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&av[0])), uintptr(unsafe.Pointer(&env[0])))
	syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 114, 0, 0)
}

// restrictFS makes the file system read-only for this process except under
// dir. It reports whether the kernel took the rules.
func restrictFS(dir string) bool {
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
	fd, err := syscall.Open(dir, oPath|syscall.O_CLOEXEC|syscall.O_DIRECTORY, 0)
	if err != nil {
		return false
	}
	// struct landlock_path_beneath_attr is packed: 8 bytes of rights, then
	// a 4-byte descriptor.
	var pb [12]byte
	*(*uint64)(unsafe.Pointer(&pb[0])) = rights
	*(*int32)(unsafe.Pointer(&pb[8])) = int32(fd)
	_, _, e = syscall.RawSyscall6(sysLandlockAddRule, rs, landlockRulePathBeneath, uintptr(unsafe.Pointer(&pb[0])), 0, 0, 0)
	syscall.Close(fd)
	if e != 0 {
		return false
	}
	_, _, e = syscall.RawSyscall(sysLandlockRestrictSelf, rs, 0, 0)
	syscall.Close(int(rs))
	return e == 0
}
