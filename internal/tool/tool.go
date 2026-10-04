// Package tool implements the ovid commands. Every command writes JSON lines
// to stdout; the last line is the result and always has "ok".
package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"ovid/internal/check"
	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/module"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitFail  = 1  // the program has errors, or the request failed
	ExitStale = 2  // an edit named a hash or revision that has moved on
	ExitUsage = 64 // bad command line
	ExitBuild = 125
)

// maxErrors caps error lines; the summary still counts all of them.
const maxErrors = 40

func emit(w io.Writer, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	w.Write(buf.Bytes())
}

func fail(w io.Writer, code, msg, hint string) int {
	r := map[string]any{"ok": false, "error": code, "message": msg}
	if hint != "" {
		r["hint"] = hint
	}
	emit(w, r)
	return ExitFail
}

// checked is a loaded module with its check result.
type checked struct {
	m     *module.Module
	res   *check.Result
	diags []module.Diag
}

// lockModule takes the module's write lock; see module.Lock.
func lockModule(dir string) (func(), error) {
	if dir == "" {
		dir = "."
	}
	return module.Lock(dir)
}

func load(dir string) (*module.Module, error) {
	if dir == "" {
		dir = "."
	}
	return module.Load(dir)
}

// loadBuild is load without _test.ov files.
func loadBuild(dir string) (*module.Module, error) {
	if dir == "" {
		dir = "."
	}
	return module.LoadBuild(dir)
}

func runCheck(m *module.Module) *checked {
	c := &checked{m: m}
	c.diags = append(c.diags, m.Errors...)
	if len(m.Errors) > 0 {
		// Do not typecheck a tree with holes; every error would cascade.
		c.res = &check.Result{Types: map[string]string{}}
		return c
	}
	c.res = check.Run(m.Prog)
	for _, is := range c.res.Issues {
		d := module.Diag{Fact: "error", Code: is.Code, Message: is.Message, ID: is.ID,
			Expected: is.Expected, Got: is.Got, Hint: is.Hint}
		m.Locate(&d)
		c.diags = append(c.diags, d)
	}
	return c
}

func (c *checked) writeDiags(w io.Writer) {
	for i, d := range c.diags {
		if i == maxErrors {
			emit(w, map[string]any{"fact": "truncated", "more": len(c.diags) - maxErrors})
			break
		}
		emit(w, d)
	}
}

// Check prints errors and a summary. facts adds declaration facts.
func Check(dir string, facts bool, w io.Writer) int {
	t0 := time.Now()
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	c := runCheck(m)
	c.writeDiags(w)
	if facts {
		for _, f := range c.res.Facts {
			if l := m.Index[fmt.Sprint(f["id"])]; l != nil {
				file, a, _, _ := m.Where(l.Span)
				f["file"], f["line"] = file, a.Line
			}
			emit(w, f)
		}
	}
	emit(w, map[string]any{"fact": "summary", "ok": len(c.diags) == 0, "errors": len(c.diags),
		"packages": len(m.Prog.Packages), "funcs": c.res.Funcs, "revision": m.Revision(),
		"ms": time.Since(t0).Milliseconds()})
	if len(c.diags) > 0 {
		return ExitFail
	}
	return ExitOK
}

// DefaultOut is where build writes when -o is not given.
func DefaultOut(m *module.Module) string {
	return filepath.Join(m.Root, "bin", filepath.Base(m.Name))
}

func compileTo(m *module.Module, p *ir.Program, out string) (int, error) {
	bin, err := compile.Compile(p)
	if err != nil {
		return 0, err
	}
	_, err = module.WriteFiles(map[string][]byte{out: bin}, 0o755)
	return len(bin), err
}

func Build(dir, out string, w io.Writer) int {
	m, err := loadBuild(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	c := runCheck(m)
	if len(c.diags) > 0 {
		c.writeDiags(w)
		emit(w, map[string]any{"ok": false, "errors": len(c.diags)})
		return ExitFail
	}
	if out == "" {
		out = DefaultOut(m)
	}
	n, err := compileTo(m, m.Prog, out)
	if err != nil {
		return fail(w, "compile", err.Error(), "")
	}
	emit(w, map[string]any{"ok": true, "output": out, "bytes": n})
	return ExitOK
}

// Run builds to a temporary file and runs it with the given args. stdio is
// the program's; the exit code is the program's. If the build fails, ovid
// prints the errors and exits 125. If the program is killed by a signal,
// ovid writes one JSON line to stderr saying which, and for a fault the
// statement and calls it died in, and exits 128 + the signal number.
func Run(dir string, args []string, w io.Writer) int {
	m, err := loadBuild(dir)
	if err != nil {
		fail(w, "load", err.Error(), "")
		return ExitBuild
	}
	c := runCheck(m)
	if len(c.diags) > 0 {
		c.writeDiags(w)
		emit(w, map[string]any{"ok": false, "errors": len(c.diags)})
		return ExitBuild
	}
	tmpd, err := os.MkdirTemp("", "ovid-run-")
	if err != nil {
		fail(w, "write", err.Error(), "")
		return ExitBuild
	}
	defer os.RemoveAll(tmpd)
	bin := filepath.Join(tmpd, filepath.Base(m.Name))
	exe, marks, err := compile.CompileMap(m.Prog)
	if err == nil {
		_, err = module.WriteFiles(map[string][]byte{bin: exe}, 0o755)
	}
	if err != nil {
		fail(w, "compile", err.Error(), "")
		return ExitBuild
	}
	// ^C goes to the program; ovid stays to report how it ended. Caught,
	// not ignored, so the program gets the default action.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	pr := runProc(bin, args, procIO{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}, 0)
	switch {
	case pr.err != nil:
		fail(w, "run", pr.err.Error(), "")
		return ExitBuild
	case pr.exited:
		return pr.code
	}
	r := map[string]any{"ok": false, "error": "killed", "exit": 128 + int(pr.signal)}
	describeCrash(m, marks, pr, r)
	emit(os.Stderr, r)
	return 128 + int(pr.signal)
}

// Dump prints the program tree as JSON (the format the self-hosted CLI reads).
func Dump(dir string, w io.Writer) int {
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	if len(m.Errors) > 0 {
		c := &checked{m: m, diags: m.Errors}
		c.writeDiags(w)
		emit(w, map[string]any{"ok": false, "errors": len(m.Errors)})
		return ExitFail
	}
	m.Prog.Revision = m.Revision()
	raw, err := ir.Marshal(m.Prog)
	if err != nil {
		return fail(w, "dump", err.Error(), "")
	}
	w.Write(raw)
	return ExitOK
}
