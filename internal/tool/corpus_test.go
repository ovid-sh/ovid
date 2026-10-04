package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The corpus is tests/ at the repository root: plain Ovid programs whose
// expectations are comments in the source (see tests/README.md). A case is
// a single .ov file, which becomes demo/main.ov of a module named demo, or
// a directory with its own ovid.mod.

// corpusCase is one program of the corpus, ready to load.
type corpusCase struct {
	name  string            // path under tests/, without .ov
	root  string            // module root to check and build
	files map[string]string // source by module-relative path
	shown map[string]string // the same keys, as paths under tests/ for messages
}

// corpus lists the cases of tests/<kind>.
func corpus(t *testing.T, kind string) []corpusCase {
	t.Helper()
	base := filepath.Join(repo(t), "tests", kind)
	ents, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(base, e.Name())
		c := corpusCase{files: map[string]string{}, shown: map[string]string{}}
		switch {
		case e.IsDir():
			c.name, c.root = kind+"/"+e.Name(), p
			err := filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(f, ".ov") {
					return err
				}
				src, err := os.ReadFile(f)
				rel, _ := filepath.Rel(p, f)
				rel = filepath.ToSlash(rel)
				c.files[rel], c.shown[rel] = string(src), "tests/"+c.name+"/"+rel
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		case strings.HasSuffix(e.Name(), ".ov"):
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			c.name = kind + "/" + strings.TrimSuffix(e.Name(), ".ov")
			c.root = mkmod(t, map[string]string{"demo/main.ov": string(src)})
			c.files["demo/main.ov"], c.shown["demo/main.ov"] = string(src), "tests/"+kind+"/"+e.Name()
		default:
			continue
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		t.Fatalf("no cases in %s", base)
	}
	return cases
}

// sortedFiles returns the case's module-relative paths in order.
func (c *corpusCase) sortedFiles() []string {
	var rels []string
	for rel := range c.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	return rels
}

// runWant is what a tests/run case expects of its program.
type runWant struct {
	exit   int
	stdout string
	args   []string
}

// directive returns the text after "// key:" when line is that comment.
func directive(line, key string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "// "+key+":")
	return strings.TrimSpace(rest), ok
}

func (c *corpusCase) runWant() (runWant, error) {
	var w runWant
	exitAt := ""
	for _, rel := range c.sortedFiles() {
		for i, line := range strings.Split(c.files[rel], "\n") {
			at := fmt.Sprintf("%s:%d", c.shown[rel], i+1)
			if v, ok := directive(line, "exit"); ok {
				n, err := strconv.Atoi(v)
				if err != nil {
					return w, fmt.Errorf("%s: exit wants a number, got %q", at, v)
				}
				if exitAt != "" {
					return w, fmt.Errorf("%s: a second exit, after %s", at, exitAt)
				}
				w.exit, exitAt = n, at
			} else if v, ok := directive(line, "stdout"); ok {
				s, err := strconv.Unquote(v)
				if err != nil {
					return w, fmt.Errorf("%s: stdout wants a quoted string, got %s", at, v)
				}
				w.stdout += s
			} else if v, ok := directive(line, "args"); ok {
				w.args = append(w.args, strings.Fields(v)...)
			}
		}
	}
	return w, nil
}

// TestCorpusRun builds every program of tests/run and, where the host can
// execute the result, checks its exit code and stdout.
func TestCorpusRun(t *testing.T) {
	for _, c := range corpus(t, "run") {
		t.Run(c.name, func(t *testing.T) {
			want, err := c.runWant()
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(t.TempDir(), "prog")
			var b bytes.Buffer
			if code := Build(c.root, bin, &b); code != 0 {
				t.Fatalf("tests/%s does not build:\n%s", c.name, b.String())
			}
			if !canExec {
				t.Skipf("built only: %s/%s cannot execute linux/amd64 binaries", runtime.GOOS, runtime.GOARCH)
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(bin, want.args...)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			exit := 0
			if err := cmd.Run(); err != nil {
				ee, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				exit = ee.ExitCode()
			}
			var diff []string
			if exit != want.exit {
				diff = append(diff, fmt.Sprintf("exit: want %d, got %d", want.exit, exit))
			}
			if stdout.String() != want.stdout {
				diff = append(diff, fmt.Sprintf("stdout: want %q, got %q", want.stdout, stdout.String()))
			}
			if len(diff) > 0 {
				if stderr.Len() > 0 {
					diff = append(diff, fmt.Sprintf("stderr: %q", stderr.String()))
				}
				t.Fatalf("tests/%s:\n  %s", c.name, strings.Join(diff, "\n  "))
			}
		})
	}
}

// wantErr is one "// error: code [col]" comment: a diagnostic with that
// code is expected on the comment's own line.
type wantErr struct {
	file string // module-relative
	line int
	col  int // 0: any
	code string
}

var errDirective = regexp.MustCompile(`// error: ([a-z_]+)(?: (\d+))?\s*$`)

func (c *corpusCase) wantErrs() []wantErr {
	var ws []wantErr
	for _, rel := range c.sortedFiles() {
		for i, line := range strings.Split(c.files[rel], "\n") {
			if m := errDirective.FindStringSubmatch(line); m != nil {
				col, _ := strconv.Atoi(m[2])
				ws = append(ws, wantErr{file: rel, line: i + 1, col: col, code: m[1]})
			}
		}
	}
	return ws
}

// TestCorpusFail checks every program of tests/fail: the diagnostics must be
// exactly the ones its "// error:" comments name.
func TestCorpusFail(t *testing.T) {
	for _, c := range corpus(t, "fail") {
		t.Run(c.name, func(t *testing.T) {
			wants := c.wantErrs()
			if len(wants) == 0 {
				t.Fatalf("tests/%s has no // error: comment", c.name)
			}
			var b bytes.Buffer
			Check(c.root, false, &b)
			type diag struct {
				Fact, Code, Message, File string
				Line, Col                 int
			}
			var got []diag
			for _, ln := range strings.Split(strings.TrimSpace(b.String()), "\n") {
				var d diag
				if err := json.Unmarshal([]byte(ln), &d); err != nil {
					t.Fatalf("not JSON: %q", ln)
				}
				if d.Fact == "truncated" {
					t.Fatalf("tests/%s: more than %d errors", c.name, maxErrors)
				}
				if d.Fact == "error" {
					// Diagnostics name files relative to the working
					// directory; make them module-relative like wants.
					if abs, err := filepath.Abs(d.File); err == nil {
						if rel, err := filepath.Rel(c.root, abs); err == nil && c.files[filepath.ToSlash(rel)] != "" {
							d.File = filepath.ToSlash(rel)
						}
					}
					got = append(got, d)
				}
			}
			var diff []string
			met := make([]bool, len(wants))
			for _, d := range got {
				found := false
				for i, w := range wants {
					// A diagnostic without a position matches by code alone.
					here := d.Line == 0 || (d.Line == w.line && d.File == w.file)
					if !met[i] && d.Code == w.code && here && (w.col == 0 || d.Line == 0 || d.Col == w.col) {
						met[i], found = true, true
						break
					}
				}
				if !found {
					at := d.File
					if shown, ok := c.shown[d.File]; ok {
						at = shown
					}
					diff = append(diff, fmt.Sprintf("unexpected: %s:%d:%d %s: %s", at, d.Line, d.Col, d.Code, d.Message))
				}
			}
			for i, w := range wants {
				if !met[i] {
					col := ""
					if w.col != 0 {
						col = fmt.Sprintf(":%d", w.col)
					}
					diff = append(diff, fmt.Sprintf("missing:    %s:%d%s %s", c.shown[w.file], w.line, col, w.code))
				}
			}
			if len(diff) > 0 {
				t.Fatalf("tests/%s:\n  %s", c.name, strings.Join(diff, "\n  "))
			}
		})
	}
}
