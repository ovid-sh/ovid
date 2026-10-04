package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
			if err != nil && task.Goal.Xfail == "" {
				t.Fatalf("solution.sh: %v\n%s", err, out)
			}
			bad = Grade(ovid, work, task.Goal)
			switch {
			case task.Goal.Xfail != "" && len(bad) == 0:
				t.Fatalf("the solution now meets the goal: %s looks fixed; drop xfail from goal.json", task.Goal.Xfail)
			case task.Goal.Xfail != "":
				t.Logf("fails as expected (%s): %s", task.Goal.Xfail, strings.Join(bad, "; "))
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
