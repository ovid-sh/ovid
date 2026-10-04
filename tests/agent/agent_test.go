package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestTasks checks the exercise itself, without a model: each task's goal
// fails on its start (unless the start is meant to pass), and its
// reference solution, run with this ovid, meets it. A solution that fails
// because of a known bug is marked xfail and must keep failing until the
// bug is fixed.
func TestTasks(t *testing.T) {
	ovid := buildOvid(t)
	ts, err := Load(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) == 0 {
		t.Fatal("no tasks")
	}
	for _, task := range ts {
		t.Run(task.Name, func(t *testing.T) {
			if task.Solution == "" {
				t.Fatal("no solution.sh")
			}
			work := t.TempDir()
			if err := task.Setup(work); err != nil {
				t.Fatal(err)
			}
			bad := Grade(ovid, work, task.Goal)
			if !CanRun {
				t.Skip("grading runs programs; they execute only on linux/amd64")
			}
			if task.Goal.StartPasses && len(bad) > 0 {
				t.Fatalf("start should meet the goal: %s", strings.Join(bad, "\n"))
			}
			if !task.Goal.StartPasses && len(bad) == 0 {
				t.Fatal("start already meets the goal")
			}
			cmd := exec.Command("sh", task.Solution)
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(ovid)+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("solution.sh: %v\n%s", err, out)
			}
			bad = Grade(ovid, work, task.Goal)
			x := task.Goal.Xfail
			switch {
			case x != nil && len(bad) == 0:
				t.Fatalf("the solution now meets the goal: %s looks fixed; drop xfail from goal.json", x.Issue)
			case x != nil && !slices.Equal(bad, x.Problems):
				t.Fatalf("fails, but not as %s does:\n%s\nwant:\n%s", x.Issue, strings.Join(bad, "\n"), strings.Join(x.Problems, "\n"))
			case x != nil:
				t.Logf("fails as expected (%s): %s", x.Issue, strings.Join(bad, "; "))
			case len(bad) > 0:
				t.Fatalf("solution does not meet the goal:\n%s\nsolution output:\n%s", strings.Join(bad, "\n"), out)
			}
		})
	}
}

// buildOvid builds cmd/ovid into a temp dir and returns its path.
func buildOvid(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ovid")
	cmd := exec.Command("go", "build", "-o", bin, "ovid/cmd/ovid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestGradeBounds checks that a program printing forever and a test that
// never returns are failures, not a hung or exhausted grader.
func TestGradeBounds(t *testing.T) {
	if !CanRun {
		t.Skip("grading runs programs; they execute only on linux/amd64")
	}
	ovid := buildOvid(t)
	defer func(r, o time.Duration, l int) { runTimeout, ovidTimeout, outLimit = r, o, l }(runTimeout, ovidTimeout, outLimit)
	runTimeout, ovidTimeout, outLimit = 500*time.Millisecond, 500*time.Millisecond, 1000
	work := t.TempDir()
	write := func(f, s string) {
		p := filepath.Join(work, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ovid.mod", "module loop\nentry loop\n")
	write("loop/main.ov", `package loop

import ovid/io

func main(io *ovid/io.Cap) i64 {
  while true {
    ovid/io.Print(strptr("y\n"))
  }
  return 0
}
`)
	write("loop/main_test.ov", `package loop

import ovid/io

func TestForever(io *ovid/io.Cap) i64 {
  var n i64 = 0
  while true {
    n = n + 1
  }
  return 0
}
`)
	bad := strings.Join(Grade(ovid, work, Goal{Runs: []Run{{Stdout: "y\n"}}, Tests: []string{"TestForever"}}), "\n")
	for _, want := range []string{"ovid test exits -1: timed out", "stdout over"} {
		if !strings.Contains(bad, want) {
			t.Errorf("want %q in:\n%s", want, bad)
		}
	}
}
