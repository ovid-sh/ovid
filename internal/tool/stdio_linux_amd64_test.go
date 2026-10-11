//go:build linux && amd64

package tool

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// stdioProg does what its first argument names, then says "after" on
// stderr and exits 5: a write that cannot fail and did fail never gets
// there.
const stdioProg = `package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var m bytes = ovid/io.Arg(io, 1)
  var c i64 = m[0]
  if c == 112 {
    ovid/io.Print(io, "x\n")
  } else if c == 105 {
    ovid/io.PrintInt(io, 7)
  } else if c == 101 {
    ovid/io.Eprint(io, "x\n")
  } else if c == 110 {
    ovid/io.EprintInt(io, 7)
  } else if c == 119 {
    var e error = ovid/io.Write(ovid/io.Stdout(io), "x\n")
    if e != 0 {
      ovid/io.Eprint(io, ovid/io.ErrText(e))
      return 3
    }
  } else if c == 108 {
    while true {
      ovid/io.Print(io, "y\n")
    }
  }
  ovid/io.Eprint(io, "after\n")
  return 5
}
`

// stdioBins builds stdioProg with both compilers.
func stdioBins(t *testing.T) []string {
	t.Helper()
	dir := mkmod(t, demo(stdioProg))
	goBin := mustBuild(t, dir)
	g1 := mustBuild(t, filepath.Join(repo(t), "prog"))
	selfBin := filepath.Join(t.TempDir(), "self")
	if out, code := run(t, g1, "build", dir, "-o", selfBin, "--std", filepath.Join(repo(t), "std")); code != 0 {
		t.Fatalf("self-hosted build %d: %s", code, out)
	}
	return []string{goBin, selfBin}
}

// shRun runs script under sh with $0 the program and $1 the mode, and
// returns its stdout, its stderr, and its exit code.
func shRun(t *testing.T, script, bin, mode string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script, bin, mode)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

// TestStdioCannotFail: Print, PrintInt, Eprint, and EprintInt return 0 or
// end the program with EXIT_IO (74) when the write fails, a full device or
// a closed descriptor, in both compilers' output. Write on Stdout(io)
// returns the error instead.
func TestStdioCannotFail(t *testing.T) {
	needExec(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to redirect with")
	}
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("no /dev/full")
	}
	const outFailed = "ovid/io: write to standard output failed\n"
	cases := []struct {
		script, mode   string
		stdout, stderr string
		exit           int
	}{
		// Unredirected, every call succeeds and the program goes on.
		{`exec "$0" "$1"`, "p", "x\n", "after\n", 5},
		{`exec "$0" "$1"`, "i", "7", "after\n", 5},
		{`exec "$0" "$1"`, "e", "", "x\nafter\n", 5},
		{`exec "$0" "$1"`, "n", "", "7after\n", 5},
		{`exec "$0" "$1"`, "w", "x\n", "after\n", 5},
		// Standard output full or closed: exit 74 and one line on stderr.
		{`exec "$0" "$1" >/dev/full`, "p", "", outFailed, 74},
		{`exec "$0" "$1" >/dev/full`, "i", "", outFailed, 74},
		{`exec "$0" "$1" >&-`, "p", "", outFailed, 74},
		{`exec "$0" "$1" >&-`, "i", "", outFailed, 74},
		// Standard error full or closed: exit 74, and the line is lost.
		{`exec "$0" "$1" 2>/dev/full`, "e", "", "", 74},
		{`exec "$0" "$1" 2>/dev/full`, "n", "", "", 74},
		{`exec "$0" "$1" 2>&-`, "e", "", "", 74},
		{`exec "$0" "$1" 2>&-`, "n", "", "", 74},
		// Write on a handle is fallible, as on any file.
		{`exec "$0" "$1" >/dev/full`, "w", "", "no space left on device", 3},
	}
	for _, bin := range stdioBins(t) {
		for _, c := range cases {
			stdout, stderr, code := shRun(t, c.script, bin, c.mode)
			if stdout != c.stdout || stderr != c.stderr || code != c.exit {
				t.Errorf("%s: %s %s: exit %d, stdout %q, stderr %q; want %d, %q, %q",
					filepath.Base(bin), c.script, c.mode, code, stdout, stderr, c.exit, c.stdout, c.stderr)
			}
		}
	}
}

// sigpipeIgnored reports whether this process inherited SIGPIPE ignored,
// which its children inherit in turn.
func sigpipeIgnored(t *testing.T) bool {
	t.Helper()
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skip("no /proc/self/status")
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "SigIgn:"); ok {
			var mask uint64
			for _, r := range strings.TrimSpace(v) {
				mask = mask<<4 | uint64(strings.IndexRune("0123456789abcdef", r))
			}
			return mask&(1<<(uint(syscall.SIGPIPE)-1)) != 0
		}
	}
	t.Skip("no SigIgn in /proc/self/status")
	return false
}

// TestStdioClosedPipe: prog | head -1. The reader goes away, and the
// kernel ends the writer by SIGPIPE before the write returns, silently, as
// it does cat (run reports the signal, 141): Ovid leaves SIGPIPE alone. Started with
// SIGPIPE ignored, the write fails with EPIPE instead and the program ends
// with EXIT_IO.
func TestStdioClosedPipe(t *testing.T) {
	needExec(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to set the disposition with")
	}
	inherited := sigpipeIgnored(t)
	for _, bin := range stdioBins(t) {
		cmd := exec.Command(bin, "l")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(out).ReadString('\n')
		if err != nil || line != "y\n" {
			t.Fatalf("%s: first line %q, %v", bin, line, err)
		}
		out.Close()
		err = cmd.Wait()
		ee, _ := err.(*exec.ExitError)
		if ee == nil {
			t.Fatalf("%s: %v after its reader closed, want it ended", bin, err)
		}
		ws := ee.Sys().(syscall.WaitStatus)
		if inherited {
			if ws.ExitStatus() != 74 || stderr.String() != "ovid/io: write to standard output failed\n" {
				t.Errorf("%s with SIGPIPE inherited ignored: %v, stderr %q; want exit 74", bin, err, stderr.String())
			}
		} else if !ws.Signaled() || ws.Signal() != syscall.SIGPIPE || stderr.Len() != 0 {
			t.Errorf("%s: %v, stderr %q; want killed by SIGPIPE, silently", bin, err, stderr.String())
		}

		// What a shell shows: head's line and status, nothing on stderr.
		stdout, errOut, code := shRun(t, `"$0" "$1" | head -1`, bin, "l")
		if stdout != "y\n" || code != 0 || (!inherited && errOut != "") {
			t.Errorf("%s | head -1: exit %d, stdout %q, stderr %q", bin, code, stdout, errOut)
		}

		// SIGPIPE ignored: EPIPE reaches Print, which ends the program.
		stdout, errOut, code = shRun(t, `trap "" PIPE; { "$0" "$1"; echo "exit $?" >&2; } | head -1 >/dev/null`, bin, "l")
		if stdout != "" || errOut != "ovid/io: write to standard output failed\nexit 74\n" || code != 0 {
			t.Errorf("%s with SIGPIPE ignored: exit %d, stdout %q, stderr %q", bin, code, stdout, errOut)
		}
	}
}
