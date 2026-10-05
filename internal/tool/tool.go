// Package tool implements the ovid commands. Every command writes JSON lines
// to stdout; the last line is the result and always has "ok".
package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

// lockModule takes the module's write lock (see module.LockWait), waiting
// at most module.LockTimeout. While it waits it writes one "waiting" fact,
// so a command blocked by another writer does not look hung. If the lock
// cannot be had it reports why and returns a nil unlock and the exit code.
func lockModule(dir string, w io.Writer) (unlock func(), code int) {
	if dir == "" {
		dir = "."
	}
	timeout, err := module.LockTimeout()
	if err != nil {
		fail(w, "usage", err.Error(), "unset it to wait the default "+module.DefaultLockTimeout.String())
		return nil, ExitUsage
	}
	unlock, err = module.LockWait(dir, timeout, func(path string) {
		emit(w, map[string]any{"fact": "waiting", "for": "lock", "file": path, "timeout_ms": timeout.Milliseconds(),
			"message": "another ovid command is writing this module; waiting up to " + timeout.String() + " for its lock"})
	})
	var te *module.LockTimeoutError
	switch {
	case errors.As(err, &te):
		emit(w, map[string]any{"ok": false, "error": "lock_timeout", "message": te.Error() + "; nothing was written",
			"file": te.Path, "waited_ms": te.Waited.Milliseconds(),
			"hint": "run the command again; if no other ovid is running, find the holder of the lock (lsof " + te.Path + "). " +
				module.LockTimeoutEnv + "=30s waits longer"})
		return nil, ExitFail
	case err != nil:
		return nil, fail(w, "load", err.Error(), "")
	}
	return unlock, ExitOK
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
	c.res = check.Errors(m.Prog)
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
			if l := m.Index()[fmt.Sprint(f["id"])]; l != nil {
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

// compileTo compiles p and writes the program to out.
func compileTo(m *module.Module, p *ir.Program, out string) (*compile.Output, error) {
	o, err := compile.CompileAll(p)
	if err != nil {
		return nil, err
	}
	return o, module.ReplaceFile(out, o.Bin, 0o755)
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
	o, err := compileTo(m, m.Prog, out)
	if err != nil {
		return fail(w, "compile", err.Error(), "")
	}
	// syscalls: every system call the program can make, so that a sandbox
	// can allow those and no others.
	r := map[string]any{"ok": true, "output": out, "bytes": len(o.Bin), "syscalls": o.Syscalls}
	if o.SyscallsUnknown > 0 {
		r["syscalls_unknown"] = o.SyscallsUnknown
	}
	emit(w, r)
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
// The dump is one document and is not paged: pkg, if set, limits it to one
// package, and out, if set, sends it to a file (or a device) and prints a
// one-line receipt in its place.
func Dump(dir, pkg, out string, w io.Writer) int {
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
	prog := *m.Prog
	prog.Revision = m.Revision()
	if pkg != "" {
		var keep []ir.Package
		var paths []string
		for _, p := range prog.Packages {
			paths = append(paths, p.Path)
			if p.Path == pkg {
				keep = append(keep, p)
			}
		}
		if len(keep) == 0 {
			return fail(w, "not_found", "no package "+pkg, "packages: "+strings.Join(paths, ", "))
		}
		prog.Packages = keep
	}
	raw, err := ir.Marshal(&prog)
	if err != nil {
		return fail(w, "dump", err.Error(), "")
	}
	if out == "" {
		w.Write(raw)
		return ExitOK
	}
	// Written in place, not by rename: out may be a device.
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err == nil {
		if _, err = f.Write(raw); err == nil {
			// Only what has storage behind it can be synced: a character
			// device (/dev/null) or a FIFO answers EINVAL to a sync that
			// has nothing to do.
			if st, serr := f.Stat(); serr == nil && (st.Mode().IsRegular() || st.Mode()&(os.ModeDevice|os.ModeCharDevice) == os.ModeDevice) {
				err = f.Sync()
			}
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return fail(w, "write", err.Error(), "")
	}
	emit(w, map[string]any{"ok": true, "output": out, "bytes": len(raw), "revision": prog.Revision})
	return ExitOK
}
