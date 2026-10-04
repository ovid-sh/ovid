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
		if is.At != nil {
			var a, b module.Pos
			d.File, a, b, d.Source = m.Where(*is.At)
			d.Line, d.Col, d.EndLine, d.EndCol = a.Line, a.Col, b.Line, b.Col
		} else {
			m.Locate(&d)
		}
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
	err = module.ReplaceFile(out, bin, 0o755)
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
// the program's; the exit code is the program's. If the build fails, or
// the program cannot be placed or started, ovid prints the errors and
// exits 125. If the program is killed by a signal,
// ovid writes one JSON line to stderr saying which, and for a fault the
// statement and calls it died in, and exits 128 + the signal number.
func Run(dir string, args []string, w io.Writer) int {
	return RunWith(dir, args, RunOpts{}, w)
}

// RunOpts changes how run runs the program.
type RunOpts struct {
	// JSON captures the program's output and reports how it ended as one
	// last line on w, in place of passing stdio and the exit code through.
	JSON bool
	// Timeout ends the program after this long; 0 is no limit.
	Timeout time.Duration
	// MaxOutput is how many bytes of stdout, and of stderr, the JSON record
	// keeps; 0 is DefaultRunOutput.
	MaxOutput int
}

// DefaultRunOutput is how much of each output stream run --json keeps.
const DefaultRunOutput = 64 << 10

// ExitTimeout is run's exit code when its --timeout ended the program
// (the code timeout(1) uses).
const ExitTimeout = 124

// RunWith is Run with options. With JSON, the last line on w is
//
//	{"ok":true,"exit":N,"ms":N,"stdout":S,"stderr":S}
//
// and ok says the program was built and started, whatever its exit code;
// ovid then exits 0. A death by signal or timeout reads "signal" in place
// of "exit", with the crash fields. Output past MaxOutput is cut and
// "truncated" is true, with "stdout_bytes" and "stderr_bytes" the full sizes.
func RunWith(dir string, args []string, o RunOpts, w io.Writer) int {
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
	exe, marks, err := compile.CompileMap(m.Prog)
	if err != nil {
		fail(w, "compile", err.Error(), "")
		return ExitBuild
	}
	// A missing temporary directory is not fatal yet: stage may still hold
	// the program in memory.
	tmpd, terr := os.MkdirTemp("", "ovid-run-")
	if terr == nil {
		defer os.RemoveAll(tmpd)
	}
	name := filepath.Base(m.Name)
	st, err := stage(exe, tmpd, name, 3)
	if terr != nil && err != nil {
		err = terr
	}
	if err != nil {
		fail(w, "run", err.Error(), tmpHint)
		return ExitBuild
	}
	defer st.done()
	pio := procIO{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, argv0: name}
	if st.extra != nil {
		pio.extra = []*os.File{st.extra}
	}
	var outc, errc *pipeCapture
	if o.JSON {
		if o.MaxOutput <= 0 {
			o.MaxOutput = DefaultRunOutput
		}
		if outc, err = newPipeCapture(o.MaxOutput); err == nil {
			if errc, err = newPipeCapture(o.MaxOutput); err != nil {
				outc.finish()
			}
		}
		if err != nil {
			fail(w, "run", err.Error(), "")
			return ExitBuild
		}
		pio.stdout, pio.stderr = outc.w, errc.w
	}
	// ^C goes to the program; ovid stays to report how it ended. Caught,
	// not ignored, so the program gets the default action.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	t0 := time.Now()
	pr := runProc(st.path, args, pio, o.Timeout)
	ms := time.Since(t0).Milliseconds()
	if o.JSON {
		stdout, outN := outc.finish()
		stderr, errN := errc.finish()
		if pr.err != nil {
			fail(w, "run", pr.err.Error(), tmpHint)
			return ExitBuild
		}
		r := map[string]any{"ok": true, "ms": ms}
		switch {
		case pr.exited:
			r["exit"] = pr.code
		case pr.timedOut:
			r["signal"] = "timeout"
			r["hint"] = fmt.Sprintf("killed after %s", o.Timeout)
		default:
			describeCrash(m, exe, marks, pr, r)
		}
		cut := func(key string, b []byte, n int64) {
			if n > int64(len(b)) { // more was written than was kept
				r["truncated"], r[key+"_bytes"] = true, n
			}
			r[key] = string(b)
		}
		cut("stdout", stdout, outN)
		cut("stderr", stderr, errN)
		emit(w, r)
		return ExitOK
	}
	switch {
	case pr.err != nil:
		fail(w, "run", pr.err.Error(), tmpHint)
		return ExitBuild
	case pr.exited:
		return pr.code
	}
	r := map[string]any{"ok": false, "error": "killed", "exit": 128 + int(pr.signal)}
	describeCrash(m, exe, marks, pr, r)
	if pr.timedOut {
		r["signal"], r["exit"] = "timeout", ExitTimeout
		r["hint"] = fmt.Sprintf("killed after %s", o.Timeout)
		emit(os.Stderr, r)
		return ExitTimeout
	}
	emit(os.Stderr, r)
	return 128 + int(pr.signal)
}

// Dump prints the program tree as JSON. A string literal that is not valid
// UTF-8 is written as value_hex, so the dump is valid JSON and loses nothing.
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
