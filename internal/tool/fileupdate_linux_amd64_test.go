package tool

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFileLock: a write lock ovid/io.Lock takes is a POSIX record lock
// another process sees and respects (as sqlite3 does), until it is given
// back; a lock another process holds makes Lock fail without waiting, or,
// asked to wait, block until it is given back.
func TestFileLock(t *testing.T) {
	bin := buildUpdater(t)
	p := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// probe asks the kernel who holds a lock that a write lock on the whole
	// file would conflict with: F_UNLCK when no one does.
	probe := func() syscall.Flock_t {
		t.Helper()
		fl := syscall.Flock_t{Type: syscall.F_WRLCK}
		if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &fl); err != nil {
			t.Fatal(err)
		}
		return fl
	}

	cmd := exec.Command(bin, "l", p)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(out)
	if line, err := r.ReadString('\n'); line != "locked\n" {
		t.Fatalf("read %q, %v", line, err)
	}
	if fl := probe(); fl.Type != syscall.F_WRLCK || int(fl.Pid) != cmd.Process.Pid {
		t.Fatalf("while held: type %d by pid %d, want F_WRLCK by %d", fl.Type, fl.Pid, cmd.Process.Pid)
	}
	in.Close()
	if line, err := r.ReadString('\n'); line != "unlocked\n" {
		t.Fatalf("read %q, %v", line, err)
	}
	if fl := probe(); fl.Type != syscall.F_UNLCK {
		t.Fatalf("after unlock: type %d by pid %d", fl.Type, fl.Pid)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	// This process holds a read lock: the program's write lock is refused
	// at once (exit 3) instead of waiting.
	fl := syscall.Flock_t{Type: syscall.F_RDLCK}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &fl); err != nil {
		t.Fatal(err)
	}
	busy := exec.Command(bin, "l", p)
	busy.Stdin = nil
	err = busy.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("lock held elsewhere: %v", err)
	}

	// Asked to wait, the program blocks in F_SETLKW: the kernel lists it as
	// a waiter in /proc/locks ("->" before the entry). Once this process
	// gives its lock back, the program takes the lock and says so.
	waiter := exec.Command(bin, "l", p, "wait")
	win, err := waiter.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	wout, err := waiter.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	defer waiter.Process.Kill()
	blocked := fmt.Sprintf(" WRITE %d ", waiter.Process.Pid)
	for deadline := time.Now().Add(10 * time.Second); ; {
		locks, err := os.ReadFile("/proc/locks")
		if err != nil {
			t.Skipf("no /proc/locks to see the waiter in: %v", err)
		}
		if waiting(string(locks), blocked) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the program never waited for the lock:\n%s", locks)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fl = syscall.Flock_t{Type: syscall.F_UNLCK}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &fl); err != nil {
		t.Fatal(err)
	}
	wr := bufio.NewReader(wout)
	if line, err := wr.ReadString('\n'); line != "locked\n" {
		t.Fatalf("after the wait, read %q, %v", line, err)
	}
	win.Close()
	if err := waiter.Wait(); err != nil {
		t.Fatal(err)
	}
}

// waiting reports whether /proc/locks text has a blocked request ("->")
// whose line holds entry.
func waiting(locks, entry string) bool {
	for _, l := range strings.Split(locks, "\n") {
		if strings.Contains(l, "->") && strings.Contains(l, entry) {
			return true
		}
	}
	return false
}
