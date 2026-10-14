package tool

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCrashInLoopCondition: a loop's condition is emitted below its body,
// and a fault in it is still reported at the while, not at the statement
// the body ended with.
func TestRunCrashInLoopCondition(t *testing.T) {
	// Crash sites need ptrace, and the program is a linux/amd64 binary.
	needExec(t)
	dir := mkmod(t, demo(`package demo

import ovid/io

func Count(p i64) i64 {
  var n i64 = 0
  while load64(p) != 0 {
    n = n + 1
    p = 0
  }
  return n
}

func main(io *ovid/io.Cap) i64 {
  var cell i64 = ovid/io.Alloc(io, 8)
  store64(cell, 1)
  return Count(cell)
}
`))
	stdout, stderr := filepath.Join(dir, "out"), filepath.Join(dir, "err")
	code := withStdio(t, stdout, stderr, func() int { return Run(dir, nil, io.Discard) })
	if code != 128+11 {
		t.Fatalf("exit %d", code)
	}
	errb, _ := os.ReadFile(stderr)
	r := last(t, string(errb))
	at, _ := r["at"].(map[string]any)
	if src, _ := at["source"].(string); r["fault_addr"] != "0x0" || !strings.Contains(src, "while load64(p) != 0") {
		t.Fatalf("the fault is reported at %v:\n%s", at["source"], errb)
	}
}
