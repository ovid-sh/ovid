//go:build linux && amd64

package tool

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const sysMemfdCreate = 319

// stageInMemory puts exe in an anonymous in-memory file. The child execs
// it by its descriptor's /proc path, so this needs /proc; it returns nil
// where that or memfd_create is missing.
func stageInMemory(exe []byte, name string, fd int) *staged {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		return nil
	}
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil
	}
	r, _, e := syscall.Syscall(sysMemfdCreate, uintptr(unsafe.Pointer(n)), 0, 0)
	if e != 0 {
		return nil
	}
	f := os.NewFile(r, name)
	if _, err := f.Write(exe); err != nil {
		f.Close()
		return nil
	}
	return &staged{path: fmt.Sprintf("/proc/self/fd/%d", fd), extra: f, done: func() { f.Close() }}
}
