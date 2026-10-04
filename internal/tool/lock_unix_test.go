//go:build unix

package tool

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ovid/internal/module"
)

// TestLockTimeout: a writer that cannot get the module lock says it is
// waiting, gives up at the deadline with lock_timeout, and writes nothing
// (#22).
func TestLockTimeout(t *testing.T) {
	src := "package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"
	dir := mkmod(t, demo(src))
	// Another process holding the lock: a flock on its own open of ovid.mod.
	f, err := os.Open(filepath.Join(dir, "ovid.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	t.Setenv(module.LockTimeoutEnv, "200ms")
	op := EditOp{Op: "replace", ID: "st:demo.main:1", Text: "return 1"}
	var b bytes.Buffer
	start := time.Now()
	code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Force: true}, &b)
	rs := lines(t, b.String())
	if code != ExitFail || len(rs) != 2 || rs[0]["fact"] != "waiting" || rs[0]["for"] != "lock" ||
		rs[1]["error"] != "lock_timeout" || rs[1]["ok"] != false {
		t.Fatalf("held lock: %d %s", code, b.String())
	}
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Fatalf("gave up after %s; want the 200ms deadline", waited)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov")); string(got) != src {
		t.Fatalf("written while locked:\n%s", got)
	}
	// rename and move take the same lock.
	b.Reset()
	if code := Rename(dir, "main", "Main", false, &b); code != ExitFail || last(t, b.String())["error"] != "lock_timeout" {
		t.Fatalf("rename: %d %s", code, b.String())
	}
	// A bad override is a usage error, not a silent default.
	t.Setenv(module.LockTimeoutEnv, "soon")
	b.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Force: true}, &b); code != ExitUsage || last(t, b.String())["error"] != "usage" {
		t.Fatalf("bad %s: %d %s", module.LockTimeoutEnv, code, b.String())
	}
	// Once the holder lets go, the edit goes through.
	t.Setenv(module.LockTimeoutEnv, "")
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	b.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Force: true}, &b); code != 0 || len(lines(t, b.String())) != 1 {
		t.Fatalf("released: %d %s", code, b.String())
	}
}
