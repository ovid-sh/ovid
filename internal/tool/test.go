package tool

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ovid/internal/check"
	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/module"
)

const testTimeout = 10 * time.Second

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
				l := m.Index[fn.ID]
				if l == nil || !strings.HasSuffix(m.Files[l.Span.File].Abs, "_test.ov") {
					continue
				}
				d := map[string]any{"fact": "error", "code": "bad_test", "id": fn.ID,
					"message": fn.Name + " starts with Test but is not func(io *ovid/io.Cap) i64; skipped", "got": sig}
				if l := m.Index[fn.ID]; l != nil {
					file, a, _, _ := m.Where(l.Span)
					d["file"], d["line"] = file, a.Line
				}
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
			if l := m.Index[t.id]; l != nil {
				file, a, _, _ := m.Where(l.Span)
				r["file"], r["line"] = file, a.Line
			}
			emit(w, r)
		}
		emit(w, map[string]any{"ok": true, "count": len(tests)})
		return ExitOK
	}
	prog := testProgram(m.Prog, tests)
	tmpd, err := os.MkdirTemp("", "ovid-test-")
	if err != nil {
		return fail(w, "write", err.Error(), "")
	}
	defer os.RemoveAll(tmpd)
	bin := filepath.Join(tmpd, "test")
	exe, marks, err := compile.CompileMap(prog)
	if err == nil {
		err = os.WriteFile(bin, exe, 0o755)
	}
	if err != nil {
		return fail(w, "compile", err.Error(), "")
	}
	outPath := filepath.Join(tmpd, "out")
	passed, failed := 0, 0
	for k, t := range tests {
		args := make([]string, k+1)
		for i := range args {
			args[i] = "t"
		}
		of, err := os.Create(outPath)
		if err != nil {
			return fail(w, "write", err.Error(), "")
		}
		t0 := time.Now()
		pr := runProc(bin, args, procIO{stdout: of, stderr: of}, testTimeout)
		ms := time.Since(t0).Milliseconds()
		of.Close()
		out, _ := os.ReadFile(outPath)
		r := map[string]any{"fact": "test", "id": t.id, "ms": ms}
		if l := m.Index[t.id]; l != nil {
			file, a, _, _ := m.Where(l.Span)
			r["file"], r["line"] = file, a.Line
		}
		code := pr.code
		ok := pr.err == nil && pr.exited && code == 0
		switch {
		case pr.err != nil:
			r["error"] = pr.err.Error()
		case pr.timedOut:
			r["signal"] = "timeout"
			r["hint"] = fmt.Sprintf("killed after %s", testTimeout)
		case !pr.exited:
			code = -1
			describeCrash(m, marks, pr, r)
		}
		// The runtime ends a program it could not get memory for with this
		// code and message; no return statement of the test produced it.
		oom := pr.exited && code == compile.ExitOOM && bytes.HasSuffix(out, []byte("out of memory\n"))
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
			if len(s) > 4000 {
				s = s[:4000] + "...(truncated)"
			}
			r["output"] = s
		}
		emit(w, r)
	}
	emit(w, map[string]any{"fact": "summary", "ok": failed == 0, "passed": passed, "failed": failed})
	if failed > 0 {
		return ExitFail
	}
	return ExitOK
}

// isTest reports whether fn has a test's signature, func(io *ovid/io.Cap) i64.
func isTest(fn *ir.Func) bool {
	return strings.HasPrefix(fn.Name, "Test") && len(fn.Params) == 1 && fn.Params[0].Type == "*ovid/io.Cap" && fn.Result == "i64"
}

// returnsOf finds the return statements in a test that could have produced
// exit code: those returning that literal, else every non-literal return.
func returnsOf(m *module.Module, id string, code int) []map[string]any {
	l := m.Index[id]
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
	var body []*ir.Node
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
		body = append(body, &ir.Node{ID: id("st"), Op: "if", Cond: cond,
			Then: []*ir.Node{{ID: id("st"), Op: "return", Val: call}}})
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
