//go:build unix

package module

import (
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes an exclusive lock on the module containing dir, held until
// unlock is called. Every ovid command that writes source holds it from
// loading the module to writing, so concurrent writers run one after
// another instead of losing each other's changes. The lock is a flock on
// ovid.mod, so no lock file is left behind.
func Lock(dir string) (unlock func(), err error) {
	root, err := Find(dir)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(root, "ovid.mod"))
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
