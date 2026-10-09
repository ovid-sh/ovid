// Package agent holds the agent exercise: tasks an agent is given, each a
// starting directory and a goal a program can verify. See README.md.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Task is one directory under tests/agent.
type Task struct {
	Name     string
	Dir      string
	Prompts  []string // task.md, then task.b.md for a second agent run at the same time
	Goal     Goal
	Solution string // solution.sh, or "" if there is none
}

// Goal is goal.json: what must hold of the work directory afterwards. The
// grader never trusts the agent's account of what it did.
type Goal struct {
	Root     string            `json:"root"`     // the module, relative to the work dir
	Check    bool              `json:"check"`    // ovid check reports no errors
	Tests    []string          `json:"tests"`    // ovid test passes, and runs at least these
	Inject   map[string]string `json:"inject"`   // test files the grader adds to a copy of the module
	Runs     []Run             `json:"runs"`     // the built program, run with these inputs
	Contains []Match           `json:"contains"` // regexps the module's .ov text must match
	Lacks    []Match           `json:"lacks"`    // and must not
	// StartPasses marks a task whose start already meets the goal: the
	// task is to leave it so (a replayed request, for one).
	StartPasses bool `json:"start_passes"`
	// Xfail marks a task whose reference solution fails today because of
	// a known bug. The deterministic test then requires exactly that
	// failure, so that a fix is noticed and the mark removed, and any other
	// failure is still one.
	Xfail *Xfail `json:"xfail"`
}

// Xfail is the bug and the problems Grade reports because of it.
type Xfail struct {
	Issue    string   `json:"issue"`
	Problems []string `json:"problems"`
}

// Run is one execution of the built program, in a fresh directory that
// holds Files (by slash-separated path; directories are made as needed).
type Run struct {
	Args   []string          `json:"args"`
	Stdin  string            `json:"stdin"`
	Files  map[string]string `json:"files"`
	Stdout string            `json:"stdout"`
	// Stderr, when set, is a regexp standard error must match.
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
	// After is what the run must leave in its directory: each path's
	// content, or null for a path that must not exist.
	After map[string]*string `json:"after"`
}

// Match is a regexp over the module's .ov files (or one, by module-relative
// path). Count, when set, is the exact number of matches wanted.
type Match struct {
	File  string `json:"file"`
	Re    string `json:"re"`
	Count *int   `json:"count"`
}

// Load reads every task under dir, in name order.
func Load(dir string) ([]Task, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ts []Task
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(dir, e.Name())
		if e.Name() == "run" {
			continue // the harness, not a task
		}
		g, err := os.ReadFile(filepath.Join(d, "goal.json"))
		if err != nil {
			return nil, err
		}
		t := Task{Name: e.Name(), Dir: d}
		dec := json.NewDecoder(bytes.NewReader(g))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t.Goal); err != nil {
			return nil, fmt.Errorf("%s/goal.json: %v", e.Name(), err)
		}
		for _, f := range []string{"task.md", "task.b.md"} {
			p, err := os.ReadFile(filepath.Join(d, f))
			if err == nil {
				t.Prompts = append(t.Prompts, string(p))
			} else if f == "task.md" {
				return nil, fmt.Errorf("%s: %v", e.Name(), err)
			}
		}
		if err := loadInject(filepath.Join(d, "inject"), &t.Goal); err != nil {
			return nil, fmt.Errorf("%s: %v", e.Name(), err)
		}
		if _, err := os.Stat(filepath.Join(d, "solution.sh")); err == nil {
			t.Solution = filepath.Join(d, "solution.sh")
		}
		ts = append(ts, t)
	}
	return ts, nil
}

// loadInject adds the files under dir, a task's inject/, to the goal's
// Inject by module-relative path. Kept as files, a test is easy to read
// and to swap for another spelling of the same checks.
func loadInject(dir string, g *Goal) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if _, dup := g.Inject[rel]; dup {
			return fmt.Errorf("inject/%s is also in goal.json", rel)
		}
		if g.Inject == nil {
			g.Inject = map[string]string{}
		}
		g.Inject[rel] = string(b)
		return nil
	})
}

// Setup fills work, which must exist: first with the directory of this
// repository that the task's start_from file names, if it has one (less
// its bin/, which holds build output), then with the task's start/ (if
// any), whose files win. start_from lets a task begin from a large module
// the repository already has, such as prog/, without a copy of it.
func (t Task) Setup(work string) error {
	if b, err := os.ReadFile(filepath.Join(t.Dir, "start_from")); err == nil {
		from := filepath.Join(t.Dir, "..", "..", "..", filepath.FromSlash(strings.TrimSpace(string(b))))
		if err := copyTree(from, work); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(work, "bin")); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	src := filepath.Join(t.Dir, "start")
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	return copyTree(src, work)
}

// CanRun reports whether built programs execute here; elsewhere Grade
// skips Runs and says so.
var CanRun = runtime.GOOS == "linux" && runtime.GOARCH == "amd64"

// Grade checks the goal against work with the ovid binary at ovid and
// returns what fails; none means the goal is met. The module is copied
// first, so grading never changes what the agent left.
func Grade(ovid, work string, g Goal) []string {
	var bad []string
	badf := func(f string, a ...any) { bad = append(bad, fmt.Sprintf(f, a...)) }
	tmp, err := os.MkdirTemp("", "ovid-grade-")
	if err != nil {
		return []string{err.Error()}
	}
	defer os.RemoveAll(tmp)
	root := filepath.Join(tmp, "m")
	if err := copyTree(filepath.Join(work, g.Root), root); err != nil {
		return []string{"no module: " + err.Error()}
	}
	if _, err := os.Stat(filepath.Join(root, "ovid.mod")); err != nil {
		return []string{"no ovid.mod in " + filepath.Join(work, g.Root)}
	}

	src := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ov") {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			src[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	match := func(m Match) (int, error) {
		re, err := regexp.Compile("(?m)" + m.Re)
		if err != nil {
			return 0, err
		}
		n := 0
		for f, s := range src {
			if m.File == "" || m.File == f {
				n += len(re.FindAllStringIndex(s, -1))
			}
		}
		return n, nil
	}
	for _, m := range g.Contains {
		n, err := match(m)
		switch {
		case err != nil:
			badf("bad regexp %q: %v", m.Re, err)
		case m.Count != nil && n != *m.Count:
			badf("%q matches %d times, want %d", m.Re, n, *m.Count)
		case m.Count == nil && n == 0:
			badf("nothing matches %q", m.Re)
		}
	}
	for _, m := range g.Lacks {
		if n, err := match(m); err != nil {
			badf("bad regexp %q: %v", m.Re, err)
		} else if n > 0 {
			badf("%q still matches (%d times)", m.Re, n)
		}
	}

	if g.Check {
		if out, code := ovidRun(ovid, root, "check"); code != 0 {
			badf("ovid check exits %d: %s", code, clip(out))
		}
	}
	if len(g.Tests) > 0 || len(g.Inject) > 0 {
		for f, s := range g.Inject {
			p := filepath.Join(root, filepath.FromSlash(f))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				badf("inject %s: %v", f, err)
			}
			if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
				badf("inject %s: %v", f, err)
			}
		}
		if !CanRun {
			badf("tests not run: built programs execute only on linux/amd64")
		} else {
			out, code := ovidRun(ovid, root, "test")
			if code != 0 {
				badf("ovid test exits %d: %s", code, clip(out))
			}
			passed := map[string]bool{}
			for _, ln := range strings.Split(out, "\n") {
				var f struct {
					Fact, ID string
					OK       bool
				}
				if json.Unmarshal([]byte(ln), &f) == nil && f.Fact == "test" && f.OK {
					passed[f.ID[strings.LastIndex(f.ID, ".")+1:]] = true
				}
			}
			for _, name := range g.Tests {
				if !passed[name] {
					badf("test %s did not run and pass", name)
				}
			}
		}
	}
	if len(g.Runs) > 0 {
		exe := filepath.Join(tmp, "prog")
		if out, code := ovidRun(ovid, root, "build", "-o", exe); code != 0 {
			badf("ovid build exits %d: %s", code, clip(out))
		} else if !CanRun {
			badf("runs skipped: built programs execute only on linux/amd64")
		} else {
			for i, r := range g.Runs {
				if msg := run(exe, filepath.Join(tmp, fmt.Sprintf("run%d", i)), r); msg != "" {
					badf("run %d %q: %s", i, r.Args, msg)
				}
			}
		}
	}
	return bad
}

func run(exe, dir string, r Run) string {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err.Error()
	}
	for f, s := range r.Files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err.Error()
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			return err.Error()
		}
	}
	cmd := exec.Command(exe, r.Args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(r.Stdin)
	out, errb := &capped{}, &capped{}
	cmd.Stdout, cmd.Stderr = out, errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	go func() { done <- cmd.Wait() }()
	var msgs []string
	select {
	case <-done:
		if code := cmd.ProcessState.ExitCode(); code != r.Exit {
			msgs = append(msgs, fmt.Sprintf("exit %d, want %d", code, r.Exit))
		}
	case <-time.After(runTimeout):
		cmd.Process.Kill()
		<-done
		msgs = append(msgs, fmt.Sprintf("timed out after %v", runTimeout))
	}
	if out.over {
		msgs = append(msgs, fmt.Sprintf("stdout over %d bytes", outLimit))
	} else if out.String() != r.Stdout {
		msgs = append(msgs, fmt.Sprintf("stdout %q, want %q", cut(out.String()), r.Stdout))
	}
	if r.Stderr != "" {
		if re, err := regexp.Compile(r.Stderr); err != nil {
			msgs = append(msgs, fmt.Sprintf("bad regexp %q: %v", r.Stderr, err))
		} else if !re.MatchString(errb.String()) {
			msgs = append(msgs, fmt.Sprintf("stderr %q does not match %q", cut(errb.String()), r.Stderr))
		}
	}
	var after []string
	for f := range r.After {
		after = append(after, f)
	}
	sort.Strings(after)
	for _, f := range after {
		p := filepath.Join(dir, filepath.FromSlash(f))
		want := r.After[f]
		if want == nil {
			if _, err := os.Lstat(p); err == nil {
				msgs = append(msgs, fmt.Sprintf("%s exists, want none", f))
			}
			continue
		}
		b, err := os.ReadFile(p)
		switch {
		case err != nil:
			msgs = append(msgs, fmt.Sprintf("%s: %v", f, err))
		case string(b) != *want:
			msgs = append(msgs, fmt.Sprintf("%s holds %q, want %q", f, cut(string(b)), *want))
		}
	}
	return strings.Join(msgs, "; ")
}

// Limits on what an agent's program can make the grader do; vars so the
// tests can shorten them.
var (
	runTimeout  = 10 * time.Second // per execution of the built program
	ovidTimeout = 2 * time.Minute  // per ovid command; ovid test runs the agent's tests
	outLimit    = 1 << 20          // bytes of output kept from either
)

// ovidRun runs an ovid command on root and returns its output and exit
// code, or -1 with a note if it runs past ovidTimeout.
func ovidRun(ovid, root string, args ...string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), ovidTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ovid, append([]string{args[0], "-C", root}, args[1:]...)...)
	out := &capped{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Run()
	if ctx.Err() != nil {
		return fmt.Sprintf("timed out after %v", ovidTimeout), -1
	}
	return out.String(), cmd.ProcessState.ExitCode()
}

// capped keeps the first outLimit bytes written to it and notes the rest,
// so a program printing in a loop cannot exhaust memory. The buffer is a
// field, not embedded: an embedded one's ReadFrom would let io.Copy
// bypass Write.
type capped struct {
	buf  bytes.Buffer
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := outLimit - c.buf.Len(); len(p) > room {
		c.over = true
		c.buf.Write(p[:max(room, 0)])
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }

func clip(s string) string { return cut(strings.TrimSpace(s)) }

func cut(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// Snapshot returns every file under dir by slash path, for diffs.
func Snapshot(dir string) map[string]string {
	m := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			m[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return m
}

// Changed lists the files that differ between two snapshots.
func Changed(a, b map[string]string) []string {
	var out []string
	for f, s := range b {
		if a[f] != s {
			out = append(out, f)
		}
	}
	for f := range a {
		if _, ok := b[f]; !ok {
			out = append(out, f+" (deleted)")
		}
	}
	sort.Strings(out)
	return out
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}
