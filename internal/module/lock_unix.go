//go:build unix

package module

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockWait takes an exclusive lock on the module containing dir, held until
// unlock is called. Every ovid command that writes source holds it from
// loading the module to writing, so concurrent writers run one after
// another instead of losing each other's changes. The lock is a flock on
// ovid.mod, so no lock file is left behind.
//
// The lock is tried without blocking and retried until timeout has passed;
// then LockWait fails with a *LockTimeoutError. If the first try finds the
// lock held, waiting is called once with the path of ovid.mod, so the
// caller can say why it is not answering.
func LockWait(dir string, timeout time.Duration, waiting func(path string)) (unlock func(), err error) {
	root, err := Find(dir)
	if err != nil {
		return nil, err
	}
	p := filepath.Join(root, "ovid.mod")
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	pause := time.Millisecond
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err == syscall.EINTR {
			continue
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, err
		}
		waited := time.Since(start)
		if waited >= timeout {
			f.Close()
			return nil, &LockTimeoutError{Path: p, Waited: waited.Round(time.Millisecond)}
		}
		if waiting != nil {
			waiting(p)
			waiting = nil
		}
		time.Sleep(min(pause, timeout-waited))
		pause = min(2*pause, 50*time.Millisecond)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
