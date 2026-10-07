package tool

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestTestBoundsOutput: a test may print, or write to the returned mark's
// descriptor, without end. ovid test keeps the start of the output, counts
// the rest, and still ends with its summary.
func TestTestBoundsOutput(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("ovid programs are linux/amd64 binaries")
	}
	old := testTimeout
	testTimeout = 300 * time.Millisecond
	defer func() { testTimeout = old }()
	dir := mkmod(t, demo(`package demo

import ovid/io

func TestA_Big(io *ovid/io.Cap) i64 {
  var i i64 = 0
  while i < 100000 {
    ovid/io.Print("0123456789\n")
    i = i + 1
  }
  return 0
}

func TestB_Flood(io *ovid/io.Cap) i64 {
  while true {
    ovid/io.Print("0123456789\n")
  }
  return 0
}

func TestC_FloodMark(io *ovid/io.Cap) i64 {
  while true {
    ovid/io.Write(3, bytes(strptr("0123456789\n"), 11))
  }
  return 0
}

func TestD_Small(io *ovid/io.Cap) i64 {
  ovid/io.Print("hi\n")
  return 1
}

func main(io *ovid/io.Cap) i64 {
  return 0
}
`))
	var b bytes.Buffer
	if code := Test(dir, "", false, &b); code != ExitFail {
		t.Fatalf("exit %d:\n%.2000s", code, b.String())
	}
	if b.Len() > 4*(maxTestOutput+1000) {
		t.Fatalf("%d bytes of records for four tests", b.Len())
	}
	rs := lines(t, b.String())
	if len(rs) != 5 {
		t.Fatalf("want four tests and a summary, got %d lines", len(rs))
	}
	cut := func(r map[string]any) bool {
		s, _ := r["output"].(string)
		return len(s) == maxTestOutput+len("...(truncated)") && strings.HasSuffix(s, "...(truncated)")
	}
	// A passing test that printed 1.1 MB: cut, and the size reported.
	if r := rs[0]; r["ok"] != true || !cut(r) || r["output_bytes"] != float64(1100000) {
		t.Fatalf("big: ok %v, output_bytes %v, %d chars", r["ok"], r["output_bytes"], len(r["output"].(string)))
	}
	// One that never stops printing: timed out, cut, and counted.
	if r := rs[1]; r["signal"] != "timeout" || !cut(r) || r["output_bytes"].(float64) <= maxTestOutput {
		t.Fatalf("flood: signal %v, output_bytes %v", r["signal"], r["output_bytes"])
	}
	// One that floods the returned mark instead: timed out, no output.
	if r := rs[2]; r["signal"] != "timeout" || r["output"] != nil {
		t.Fatalf("flood of fd 3: %v", r)
	}
	// Short output is whole and carries no count.
	if r := rs[3]; r["output"] != "hi\n" || r["output_bytes"] != nil || r["returned_by"] == nil {
		t.Fatalf("small: %v", r)
	}
	if r := rs[4]; r["fact"] != "summary" || r["passed"] != float64(1) || r["failed"] != float64(3) {
		t.Fatalf("summary: %v", r)
	}
}

// TestPipeCaptureKeepsOnlyItsLimit: what is past the limit is counted and
// not held, however much is written.
func TestPipeCaptureKeepsOnlyItsLimit(t *testing.T) {
	c, err := newPipeCapture(10)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("abcdefgh"), 1<<13) // 64 KiB
	for i := 0; i < 64; i++ {
		if _, err := c.w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	kept, n := c.finish()
	if string(kept) != "abcdefghab" || cap(kept) > 1024 || n != 64*int64(len(chunk)) {
		t.Fatalf("kept %d bytes (cap %d) of %d", len(kept), cap(kept), n)
	}
}
