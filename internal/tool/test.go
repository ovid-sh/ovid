package tool

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"ovid/internal/check"
	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/module"
)

// testTimeout is how long one test may run. A variable so that a test of
// this package can shorten it.
var testTimeout = 10 * time.Second

// maxTestOutput is how much of a test's output its record keeps.
const maxTestOutput = 4000

const testPkg = "ovid/testmain"

type testFn struct {
	pkg  string
	name string
	id   string
	span ir.Span
}

// Test runs every module func named Test* with signature
// (io *ovid/io.Cap) i64. Zero passes; anything else fails. Each test runs in
// its own process so a crash only fails that test.
func Test(dir, filter string, list bool, w io.Writer) int {
	return TestWith(dir, TestOpts{Filter: filter, List: list}, w)
}

// TestOpts changes how test runs the tests.
type TestOpts struct {
	Filter string // only tests whose name or id contains it
	List   bool   // name the tests and run none
	// NoConfine runs the tests with the caller's own rights. By default, on
	// Linux x86-64, each runs under a seccomp filter of the program's own
	// system calls and, where the kernel has Landlock, a file system it can
	// write only in one fresh directory, its working directory.
	NoConfine bool
}

// TestWith is Test with options.
func TestWith(dir string, o TestOpts, w io.Writer) int {
	filter, list := o.Filter, o.List
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	c := runCheck(m)
	if len(c.diags) > 0 {
		c.writeDiags(w)
		emit(w, map[string]any{"fact": "summary", "ok": false, "errors": len(c.diags), "hint": "fix check errors before tests run"})
		return ExitFail
	}
	var tests []testFn
	for _, pkg := range m.Prog.Packages {
		if m.Std[pkg.Path] {
			continue
		}
		for i := range pkg.Funcs {
			fn := &pkg.Funcs[i]
			if !strings.HasPrefix(fn.Name, "Test") || (filter != "" && !strings.Contains(fn.Name, filter) && !strings.Contains(fn.ID, filter)) {
				continue
			}
			sig := check.Signature(pkg.Path, fn)
			if !isTest(fn) {
				// Outside _test.ov files a Test-named helper (asm's TestRaxRax,
				// the x86 TEST instruction) is not meant as a test.
				if !strings.HasSuffix(m.Files[fn.Span.File].Abs, "_test.ov") {
					continue
				}
				d := map[string]any{"fact": "error", "code": "bad_test", "id": fn.ID,
					"message": fn.Name + " starts with Test but is not func(io *ovid/io.Cap) i64; skipped", "got": sig}
				file, a, _, _ := m.Where(fn.Span)
				d["file"], d["line"] = file, a.Line
				emit(w, d)
				continue
			}
			tests = append(tests, testFn{pkg: pkg.Path, name: fn.Name, id: fn.ID, span: fn.Span})
		}
	}
	sort.SliceStable(tests, func(i, j int) bool { return tests[i].id < tests[j].id })
	if len(tests) == 0 {
		emit(w, map[string]any{"fact": "summary", "ok": true, "passed": 0, "failed": 0,
			"hint": "no tests; write `func TestX(io *ovid/io.Cap) i64 { ... return 0 }` in any package"})
		return ExitOK
	}
	if list {
		for _, t := range tests {
			r := map[string]any{"fact": "test", "id": t.id}
			file, a, _, _ := m.Where(t.span)
			r["file"], r["line"] = file, a.Line
			emit(w, r)
		}
		emit(w, map[string]any{"ok": true, "count": len(tests)})
		return ExitOK
	}
	prog := testProgram(m.Prog, tests)
	out, err := compile.CompileAll(prog)
	if err != nil {
		return fail(w, "compile", err.Error(), "")
	}
	exe, marks := out.Bin, out.Marks
	var confine *confineSpec
	if !o.NoConfine && confineSupported {
		confine = newConfine(out.Syscalls, writableDir(), "test")
		// Whatever happens next, an empty writable directory is not left.
		defer confine.keepWritable()
		if err := confine.unenforceable(); err != nil {
			return fail(w, "run", err.Error(), tmpHint+"; or --no-confine")
		}
	}
	// A missing temporary directory is not fatal yet: stage may still hold
	// the program in memory.
	tmpd, terr := os.MkdirTemp("", "ovid-test-")
	if terr == nil {
		defer os.RemoveAll(tmpd)
	}
	// fd 3 is the returned mark; the program, if held in memory, is fd 4.
	st, err := stage(exe, tmpd, "test", returnedFD+1)
	if terr != nil && err != nil {
		err = terr
	}
	if err != nil {
		return fail(w, "run", err.Error(), tmpHint)
	}
	defer st.done()
	passed, failed := 0, 0
	for k, t := range tests {
		args := make([]string, k+1)
		for i := range args {
			args[i] = "t"
		}
		// Output and the returned mark come back through pipes, cut at what
		// the record needs: a test may write to either without end.
		oc, err := newPipeCapture(maxTestOutput + 1)
		if err != nil {
			return fail(w, "run", err.Error(), "")
		}
		rc, err := newPipeCapture(1)
		if err != nil {
			oc.finish()
			return fail(w, "run", err.Error(), "")
		}
		t0 := time.Now()
		pio := procIO{stdout: oc.w, stderr: oc.w, extra: []*os.File{rc.w}, argv0: "test", confine: confine}
		if st.extra != nil {
			pio.extra = append(pio.extra, st.extra)
		}
		pr := runProc(st.path, args, pio, testTimeout)
		ms := time.Since(t0).Milliseconds()
		out, outBytes := oc.finish()
		_, retBytes := rc.finish()
		if pr.err != nil {
			// The program could not be started: that is the environment's
			// doing and the same for every test, so it is one failure of
			// the request, not a failed test.
			return fail(w, "run", pr.err.Error(), tmpHint)
		}
		returned := retBytes > 0
		r := map[string]any{"fact": "test", "id": t.id, "ms": ms}
		file, a, _, _ := m.Where(t.span)
		r["file"], r["line"] = file, a.Line
		code := pr.code
		ok := pr.exited && code == 0
		switch {
		case pr.timedOut:
			r["signal"] = "timeout"
			r["hint"] = fmt.Sprintf("killed after %s", testTimeout)
		case !pr.exited:
			code = -1
			describeCrash(m, exe, marks, pr, confine != nil, r)
		}
		// The runtime ends a program it could not get memory for with this
		// code. The test did not return it: the wrapper marks every return.
		oom := pr.exited && code == compile.ExitOOM && !returned
		if oom {
			r["error"] = "out_of_memory"
			r["hint"] = "the kernel refused the program memory: an Alloc too large to map, or a host or limit too small for the heap's first 128 MiB region"
		}
		r["ok"] = ok
		if !ok {
			r["exit"] = code
			if r["signal"] == nil && !oom {
				if rs := returnsOf(m, t.id, code); len(rs) > 0 {
					r["returned_by"] = rs
				}
			}
			failed++
		} else {
			passed++
		}
		if len(out) > 0 {
			s := string(out)
			if len(s) > maxTestOutput {
				s = s[:maxTestOutput] + "...(truncated)"
				r["output_bytes"] = outBytes
			}
			r["output"] = s
		}
		emit(w, r)
	}
	sum := map[string]any{"fact": "summary", "ok": failed == 0, "passed": passed, "failed": failed}
	if confine != nil {
		sum["confined"] = confine.applied()
		if dir := confine.keepWritable(); dir != "" {
			sum["writable"] = dir
		}
	}
	// A test is a program that can write files, its own module's among
	// them. What was tested is then no longer what is on disk.
	changed := moduleChanged(m, sum)
	if changed {
		sum["ok"] = false
	}
	emit(w, sum)
	if failed > 0 || changed {
		return ExitFail
	}
	return ExitOK
}

// moduleChanged reports whether the module on disk differs from m, which
// was loaded before a program was run, and if so records in r which files,
// and the revision before and, when the module still loads, after.
func moduleChanged(m *module.Module, r map[string]any) bool {
	files, err := m.ChangedOnDisk()
	if len(files) == 0 && err == nil {
		return false
	}
	if files == nil {
		files = []string{}
	}
	r["module_changed"], r["changed_files"], r["revision_before"] = true, files, m.Revision()
	if err != nil {
		// The directory could not be read through, so nothing shows the
		// module is unchanged: say it changed, and why the list is short.
		r["scan_error"] = err.Error()
	}
	// Read again as m was (run leaves out the test files), or the two
	// revisions would cover different files. Left out if it no longer loads.
	if now, err := m.Reload(); err == nil {
		r["revision_after"] = now.Revision()
	}
	r["hint"] = "the module on disk is no longer the one that was loaded: the program wrote to it, or it was edited meanwhile; look at changed_files before relying on this result"
	return true
}

// isTest reports whether fn has a test's signature, func(io *ovid/io.Cap) i64.
func isTest(fn *ir.Func) bool {
	return strings.HasPrefix(fn.Name, "Test") && len(fn.Params) == 1 && fn.Params[0].Type == "*ovid/io.Cap" && fn.Result == "i64" && fn.Result2 == ""
}

// returnsOf finds the return statements in a test that could have produced
// exit code: those returning that literal, else every non-literal return.
func returnsOf(m *module.Module, id string, code int) []map[string]any {
	l := m.Index()[id]
	if l == nil {
		return nil
	}
	var lit, other []*ir.Node
	var walk func(ns []*ir.Node)
	walk = func(ns []*ir.Node) {
		for _, n := range ns {
			if n.Op == "return" && n.Val != nil {
				if n.Val.Op == "int" {
					if n.Val.Int&0xff == int64(code) {
						lit = append(lit, n)
					}
				} else {
					other = append(other, n)
				}
			}
			walk(n.Then)
			walk(n.Else)
			walk(n.Body)
		}
	}
	walk(l.Node.(*ir.Func).Body)
	if len(lit) == 0 {
		lit = other
	}
	var out []map[string]any
	for _, n := range lit {
		_, a, _, src := m.Where(n.Span)
		out = append(out, map[string]any{"id": n.ID, "line": a.Line, "source": src})
	}
	return out
}

// returnedFD is where the test wrapper writes one byte when a test returns,
// so an exit code that came from the test's own return can be told from the
// same code set by the runtime (out of memory).
const returnedFD = 3

// testProgram adds a package whose main runs test k when it gets k+1 args.
func testProgram(p *ir.Program, tests []testFn) *ir.Program {
	n := 0
	id := func(kind string) string {
		n++
		return fmt.Sprintf("%s:%s.main:%d", kind, testPkg, n)
	}
	pkg := ir.Package{ID: "pkg:" + testPkg, Path: testPkg}
	imported := map[string]bool{"ovid/io": true}
	pkg.Imports = append(pkg.Imports, ir.Import{ID: "im:" + testPkg + ":ovid/io", Path: "ovid/io"})
	body := []*ir.Node{{ID: id("st"), Op: "var", Name: "r", Type: "i64",
		Val: &ir.Node{ID: id("ex"), Op: "int", ValK: 1, Int: 0}}}
	for k, t := range tests {
		if !imported[t.pkg] {
			imported[t.pkg] = true
			pkg.Imports = append(pkg.Imports, ir.Import{ID: "im:" + testPkg + ":" + t.pkg, Path: t.pkg})
		}
		cond := &ir.Node{ID: id("ex"), Op: "eq",
			Left:  &ir.Node{ID: id("ex"), Op: "field", Name: "argc", Base: &ir.Node{ID: id("ex"), Op: "name", Name: "io"}},
			Right: &ir.Node{ID: id("ex"), Op: "int", ValK: 1, Int: int64(k + 2)}}
		call := &ir.Node{ID: id("ex"), Op: "call", Pkg: t.pkg, Func: t.name,
			Args: []*ir.Node{{ID: id("ex"), Op: "name", Name: "io"}}}
		// r = T(io); ovid/io.Write(returnedFD, strptr("r"), 1); return r
		mark := &ir.Node{ID: id("ex"), Op: "call", Pkg: "ovid/io", Func: "Write", Args: []*ir.Node{
			{ID: id("ex"), Op: "int", ValK: 1, Int: returnedFD},
			{ID: id("ex"), Op: "strptr", ValK: 3, Str: "r"},
			{ID: id("ex"), Op: "int", ValK: 1, Int: 1}}}
		body = append(body, &ir.Node{ID: id("st"), Op: "if", Cond: cond, Then: []*ir.Node{
			{ID: id("st"), Op: "assign", Name: "r", Val: call},
			{ID: id("st"), Op: "expr", Val: mark},
			{ID: id("st"), Op: "return", Val: &ir.Node{ID: id("ex"), Op: "name", Name: "r"}}}})
	}
	body = append(body, &ir.Node{ID: id("st"), Op: "return", Val: &ir.Node{ID: id("ex"), Op: "int", ValK: 1, Int: 99}})
	pkg.Funcs = []ir.Func{{ID: "fn:" + testPkg + ".main", Name: "main",
		Params: []ir.Param{{ID: "pa:" + testPkg + ".main.io", Name: "io", Type: "*ovid/io.Cap"}},
		Result: "i64", Body: body}}
	q := *p
	q.Entry = testPkg
	q.Packages = append(append([]ir.Package{}, p.Packages...), pkg)
	return &q
}
