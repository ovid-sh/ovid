//go:build unix

package module

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// holdLock takes the module lock the way another ovid process would: a
// flock on its own open of ovid.mod. It returns the release.
func holdLock(t *testing.T, dir string) func() {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "ovid.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

func TestLockWaitTimesOut(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "ovid.mod"), []byte("module demo\nentry demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	release := holdLock(t, d)
	told := 0
	start := time.Now()
	unlock, err := LockWait(d, 150*time.Millisecond, func(string) { told++ })
	var te *LockTimeoutError
	if unlock != nil || !errors.As(err, &te) || te.Path != filepath.Join(d, "ovid.mod") {
		t.Fatalf("held lock: unlock %v, err %v", unlock != nil, err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond || told != 1 {
		t.Fatalf("waited %s, reported %d times; want >= 150ms, once", waited, told)
	}
	// Released while another waits: the waiter gets it.
	go func() {
		time.Sleep(100 * time.Millisecond)
		release()
	}()
	told = 0
	unlock, err = LockWait(d, 5*time.Second, func(string) { told++ })
	if err != nil || told != 1 {
		t.Fatalf("after release: %v, reported %d times", err, told)
	}
	unlock()
	// Free: no wait, no report.
	told = 0
	unlock, err = LockWait(d, 0, func(string) { told++ })
	if err != nil || told != 0 {
		t.Fatalf("free lock: %v, reported %d times", err, told)
	}
	unlock()
}

func TestLockTimeoutEnv(t *testing.T) {
	for v, want := range map[string]time.Duration{"": DefaultLockTimeout, "250ms": 250 * time.Millisecond, "0": 0} {
		t.Setenv(LockTimeoutEnv, v)
		if got, err := LockTimeout(); err != nil || got != want {
			t.Errorf("%q: %v %v; want %v", v, got, err, want)
		}
	}
	for _, v := range []string{"10", "soon", "-1s"} {
		t.Setenv(LockTimeoutEnv, v)
		if _, err := LockTimeout(); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}
