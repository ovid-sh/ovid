// Package tool implements the ovid commands. Every command writes JSON lines
// to stdout; the last line is the result and always has "ok".
package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func load(dir string) (*module.Module, error) {
	if dir == "" {
		dir = "."
	}
	return module.Load(dir)
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
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return 0, err
	}
	tmp := out + ".ovid-tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return 0, err
	}
	return len(bin), os.Rename(tmp, out)
}

func Build(dir, out string, w io.Writer) int {
	m, err := load(dir)
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
// prints the errors and exits 125.
func Run(dir string, args []string, w io.Writer) int {
	m, err := load(dir)
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
	if _, err := compileTo(m, m.Prog, bin); err != nil {
		fail(w, "compile", err.Error(), "")
		return ExitBuild
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		if sig := signalOf(ee); sig != "" {
			fmt.Fprintf(os.Stderr, "ovid: program killed by %s\n", sig)
			return 128 + sigNum(ee)
		}
		return ee.ExitCode()
	}
	if err != nil {
		fail(w, "run", err.Error(), "")
		return ExitBuild
	}
	return ExitOK
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
