package tool

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const runJSONProg = `package demo

import ovid/io

type P struct {
  v i64
}

func main(io *ovid/io.Cap) i64 {
  ovid/io.Print(strptr("out\n"))
  ovid/io.Eprint(strptr("{\"ok\":false}\n"))
  if ovid/io.Argc(io) == 2 {
    while true {
      ovid/io.Print(strptr("0123456789\n"))
    }
  }
  if ovid/io.Argc(io) == 3 {
    var p *P = 0 as *P
    return p.v
  }
  return 3
}
`

// runJSON runs ovid run --json and returns its lines and exit code.
func runJSON(t *testing.T, dir string, o RunOpts, args ...string) ([]map[string]any, int) {
	t.Helper()
	o.JSON = true
	var b bytes.Buffer
	code := RunWith(dir, args, o, &b)
	return lines(t, b.String()), code
}

// TestRunJSON: with --json the last line alone says how the run went. The
// program's exit code, a signal, a timeout, and a build that failed are
// each told apart, and the program's own output cannot be taken for a
// record, whatever it prints.
func TestRunJSON(t *testing.T) {
	needExec(t)
	dir := mkmod(t, demo(runJSONProg))

	// An exit code is the program's; ovid itself succeeds.
	rs, code := runJSON(t, dir, RunOpts{})
	if r := rs[len(rs)-1]; code != ExitOK || len(rs) != 1 || r["ok"] != true || r["exit"] != float64(3) ||
		r["stdout"] != "out\n" || r["stderr"] != "{\"ok\":false}\n" || r["signal"] != nil || r["truncated"] != nil {
		t.Fatalf("exit %d: %v", code, rs)
	}

	// A program that never stops is ended by the timeout; its output is
	// cut at the limit and its size counted.
	rs, code = runJSON(t, dir, RunOpts{Timeout: 300 * time.Millisecond, MaxOutput: 30}, "loop")
	r := rs[len(rs)-1]
	if code != ExitOK || r["ok"] != true || r["signal"] != "timeout" || r["exit"] != nil || r["truncated"] != true ||
		len(r["stdout"].(string)) != 30 || r["stdout_bytes"].(float64) <= 30 || r["stderr_bytes"] != nil {
		t.Fatalf("exit %d: %v", code, r)
	}

	// The largest limit there is keeps everything and reports no cut.
	rs, code = runJSON(t, dir, RunOpts{MaxOutput: math.MaxInt})
	if r := rs[len(rs)-1]; code != ExitOK || r["stdout"] != "out\n" || r["stderr"] != "{\"ok\":false}\n" || r["truncated"] != nil {
		t.Fatalf("exit %d: %v", code, r)
	}
	// A limit the output exactly fills is not a cut either.
	rs, _ = runJSON(t, dir, RunOpts{MaxOutput: 4})
	if r := rs[len(rs)-1]; r["stdout"] != "out\n" || r["stdout_bytes"] != nil || r["truncated"] != true || r["stderr_bytes"] != float64(13) {
		t.Fatalf("%v", r)
	}

	// A fault is a signal, traced to its statement where the host can.
	rs, code = runJSON(t, dir, RunOpts{}, "a", "b")
	r = rs[len(rs)-1]
	if code != ExitOK || r["ok"] != true || r["signal"] != "segmentation fault" || r["exit"] != nil || r["stdout"] != "out\n" {
		t.Fatalf("exit %d: %v", code, r)
	}
	if at, _ := r["at"].(map[string]any); at != nil && at["line"] != float64(19) {
		t.Fatalf("at: %v", at)
	}

	// A module that does not check never runs: diagnostics, then ok false.
	bad := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return true\n}\n"))
	rs, code = runJSON(t, bad, RunOpts{})
	r = rs[len(rs)-1]
	if code != ExitBuild || r["ok"] != false || r["errors"] != float64(1) || r["exit"] != nil || rs[0]["code"] != "type_mismatch" {
		t.Fatalf("exit %d: %v", code, rs)
	}
}

// TestRunJSONOutputIsValid: output that is not UTF-8 still yields one line
// of valid JSON.
func TestRunJSONOutputIsValid(t *testing.T) {
	needExec(t)
	dir := mkmod(t, demo(`package demo

import ovid/io

func main(io *ovid/io.Cap) i64 {
  var p i64 = ovid/io.Alloc(io, 8)
  store8(p, 255)
  store8(p + 1, 10)
  store8(p + 2, 34)
  ovid/io.Stdout(p, 3)
  return 0
}
`))
	var b bytes.Buffer
	if code := RunWith(dir, nil, RunOpts{JSON: true}, &b); code != ExitOK {
		t.Fatalf("exit %d: %s", code, b.String())
	}
	var r map[string]any
	if strings.Count(b.String(), "\n") != 1 || json.Unmarshal(b.Bytes(), &r) != nil || r["exit"] != float64(0) {
		t.Fatalf("%q", b.String())
	}
}

// TestRunTimeoutPlain: without --json the timeout still ends the program;
// run says so on stderr and exits 124.
func TestRunTimeoutPlain(t *testing.T) {
	needExec(t)
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  while true {\n  }\n  return 0\n}\n"))
	stderr := filepath.Join(t.TempDir(), "err")
	code := withStdio(t, os.DevNull, stderr, func() int {
		return RunWith(dir, nil, RunOpts{Timeout: 200 * time.Millisecond}, os.Stdout)
	})
	raw, _ := os.ReadFile(stderr)
	r := last(t, string(raw))
	if code != ExitTimeout || r["ok"] != false || r["error"] != "killed" || r["signal"] != "timeout" || r["exit"] != float64(ExitTimeout) {
		t.Fatalf("exit %d: %s", code, raw)
	}
}
