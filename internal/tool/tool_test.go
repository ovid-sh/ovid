package tool

import (
	"bytes"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"ovid/internal/check"
	"ovid/internal/module"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// repo is the repository root, found from this package's directory.
func repo(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// module writes a module with the given files (paths relative to its root).
// The entry is demo unless ovid.mod is given.
func mkmod(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["ovid.mod"]; !ok {
		files["ovid.mod"] = "module demo\nentry demo\n"
	}
	for rel, src := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// withProg copies packages from prog/ into files.
func withProg(t *testing.T, files map[string]string, pkgs ...string) map[string]string {
	t.Helper()
	for _, pkg := range pkgs {
		d := filepath.Join(repo(t), "prog", pkg)
		ents, err := os.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".ov") && !strings.HasSuffix(e.Name(), "_test.ov") {
				b, err := os.ReadFile(filepath.Join(d, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				files[pkg+"/"+e.Name()] = string(b)
			}
		}
	}
	return files
}

// lines decodes JSON-lines output.
func lines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rs []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if ln == "" {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Fatalf("not JSON: %q", ln)
		}
		rs = append(rs, r)
	}
	return rs
}

func last(t *testing.T, out string) map[string]any {
	rs := lines(t, out)
	if len(rs) == 0 {
		t.Fatal("no output")
	}
	return rs[len(rs)-1]
}

// canExec is whether this host can execute the linux/amd64 binaries the
// compiler emits.
var canExec = runtime.GOOS == "linux" && runtime.GOARCH == "amd64"

// needExec skips the rest of a test on a host that cannot execute emitted
// binaries. What the test did before the call (build, check, edit) still ran.
func needExec(t *testing.T) {
	t.Helper()
	if !canExec {
		t.Skipf("%s/%s cannot execute linux/amd64 binaries", runtime.GOOS, runtime.GOARCH)
	}
}

// run executes an emitted binary.
func run(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	needExec(t)
	out, err := exec.Command(bin, args...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// buildRun builds a module and runs the binary.
func buildRun(t *testing.T, files map[string]string, args ...string) (string, int) {
	t.Helper()
	dir := mkmod(t, files)
	bin := filepath.Join(dir, "bin", "demo")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build %d:\n%s", code, b.String())
	}
	return run(t, bin, args...)
}

func demo(src string) map[string]string {
	return map[string]string{"demo/main.ov": src}
}

// TestMemWords: Eq and Copy work a word at a time; check every length around
// the word size, a difference in each byte, and an overlapping copy.
func TestMemWords(t *testing.T) {
	src := demo(`package demo
import ovid/io
import ovid/mem
func main(io *ovid/io.Cap) i64 {
  var a i64 = ovid/io.Alloc(io, 64)
  var b i64 = ovid/io.Alloc(io, 64)
  var n i64 = 0
  while n <= 20 {
    var i i64 = 0
    while i < n {
      store8(a + i, 65 + i)
      store8(b + i, 0)
      i = i + 1
    }
    store8(b + n, 7)
    if ovid/mem.Copy(b, a, n) != n {
      return 1
    }
    if load8(b + n) != 7 {
      return 2
    }
    if !ovid/mem.Eq(a, n, b, n) {
      return 3
    }
    i = 0
    while i < n {
      store8(b + i, 0)
      if ovid/mem.Eq(a, n, b, n) {
        return 4
      }
      store8(b + i, 65 + i)
      i = i + 1
    }
    n = n + 1
  }
  if ovid/mem.Eq(a, 9, b, 10) {
    return 5
  }
  // dst three bytes past src: the first three bytes repeat.
  ovid/mem.Copy(a, strptr("abcdefghijklmnop"), 16)
  ovid/mem.Copy(a + 3, a, 13)
  if !ovid/mem.Eq(a, 16, strptr("abcabcabcabcabca"), 16) {
    return 6
  }
  // dst before src: a plain move down.
  ovid/mem.Copy(a, strptr("abcdefghijklmnop"), 16)
  ovid/mem.Copy(a, a + 3, 13)
  if !ovid/mem.Eq(a, 16, strptr("defghijklmnopnop"), 16) {
    return 7
  }
  return 0
}
`)
	if out, code := buildRun(t, src); code != 0 {
		t.Fatalf("mem: code %d out %q", code, out)
	}
}

func TestLibs(t *testing.T) {
	sha := withProg(t, demo(`package demo
import ovid/io
import ovid/sha
func main(io *ovid/io.Cap) i64 {
  var raw i64 = ovid/io.Alloc(io, 32)
  var hex i64 = ovid/io.Alloc(io, 80)
  ovid/sha.Sum(io, strptr("abc"), strlen("abc"), raw)
  ovid/sha.Hex(hex, raw)
  ovid/io.Stdout(hex, 64)
  return 0
}
`), "ovid/sha")
	if out, code := buildRun(t, sha); code != 0 || out != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha: code %d out %q", code, out)
	}

	elfOut := filepath.Join(t.TempDir(), "exit.elf")
	asm := withProg(t, demo(`package demo
import ovid/io
import ovid/mem
import ovid/asm
import ovid/elf
func main(io *ovid/io.Cap) i64 {
  var c *ovid/asm.Code = ovid/asm.New(io)
  ovid/asm.MovRegImm(c, 0, 60)
  ovid/asm.MovRegImm(c, 7, 42)
  ovid/asm.Syscall(c)
  ovid/asm.Patch(c)
  var b *ovid/mem.Buf = ovid/elf.Link(io, c)
  var path i64 = ovid/io.Arg(io, 1)
  if ovid/io.WriteFile(io, path, ovid/io.CLen(path), b.data, b.len, 493) != 0 {
    return 8
  }
  return 0
}
`), "ovid/asm", "ovid/elf")
	if out, code := buildRun(t, asm, elfOut); code != 0 {
		t.Fatalf("asm: code %d out %q", code, out)
	}
	if _, code := run(t, elfOut); code != 42 {
		t.Fatalf("emitted elf exit %d", code)
	}

	note := filepath.Join(t.TempDir(), "note.txt")
	os.WriteFile(note, []byte("hello"), 0o644)
	if _, code := buildRun(t, demo(`package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var path i64 = ovid/io.Arg(io, 1)
  var pp i64 = ovid/io.Alloc(io, 8)
  var nn i64 = ovid/io.Alloc(io, 8)
  if ovid/io.ReadFile(io, path, ovid/io.CLen(path), pp, nn) != 0 {
    return 9
  }
  return load64(nn)
}
`), note); code != 5 {
		t.Fatalf("readfile len %d", code)
	}
}

const addSrc = `package demo

import ovid/io

func Add(a i64, b i64) i64 {
  return a + b
}

func main(io *ovid/io.Cap) i64 {
  return Add(1, true)
}
`

func TestCheckDiag(t *testing.T) {
	dir := mkmod(t, demo(addSrc))
	var b bytes.Buffer
	if code := Check(dir, false, &b); code != ExitFail {
		t.Fatalf("code %d\n%s", code, b.String())
	}
	d := lines(t, b.String())[0]
	if d["code"] != "type_mismatch" || d["line"] != float64(10) || d["expected"] != "i64" || d["got"] != "bool" ||
		!strings.HasSuffix(d["file"].(string), "demo/main.ov") || d["source"] != "  return Add(1, true)" {
		t.Fatalf("diag %v", d)
	}
	if s := last(t, b.String()); s["fact"] != "summary" || s["ok"] != false {
		t.Fatalf("summary %v", s)
	}
}

func TestSyntaxDiag(t *testing.T) {
	dir := mkmod(t, demo("package demo\nfunc F() i64 {\n  return (1 + \n}\n"))
	var b bytes.Buffer
	Check(dir, false, &b)
	d := lines(t, b.String())[0]
	if d["code"] != "syntax" || d["line"] == nil {
		t.Fatalf("diag %v", d)
	}
}

func editJSON(t *testing.T, dir string, req any, flags ...string) (map[string]any, int) {
	t.Helper()
	raw, _ := json.Marshal(req)
	p := filepath.Join(t.TempDir(), "edit.json")
	os.WriteFile(p, raw, 0o644)
	var b bytes.Buffer
	code := Edit(dir, p, EditOpts{DryRun: contains(flags, "dry"), RequireClean: contains(flags, "clean"), Show: contains(flags, "show")}, &b)
	return last(t, b.String()), code
}

func TestGrepPages(t *testing.T) {
	dir := mkmod(t, demo("package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  x = x + 1\n  return x\n}\n"))
	var b bytes.Buffer
	Grep(dir, `\bx\b`, "", false, 1, 2, &b)
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	end := last(t, b.String())
	if len(lines) != 3 || end["total"] != 4.0 || end["count"] != 2.0 || end["has_more"] != true || end["next_offset"] != 3.0 {
		t.Fatalf("page: %s", b.String())
	}
	if !strings.Contains(lines[0], `"stmt":"st:demo.main:2"`) || !strings.Contains(lines[0], `"decl":"fn:demo.main"`) {
		t.Fatalf("enclosing nodes: %s", lines[0])
	}
	b.Reset()
	Grep(dir, `\bx\b`, "", false, 3, 0, &b)
	if end := last(t, b.String()); end["count"] != 1.0 || end["has_more"] != false {
		t.Fatalf("last page: %s", b.String())
	}
}

// TestBuildSkipsTests: build and run compile the program without its
// _test.ov files, so a broken test stops check and test but not them, and
// the program cannot use a test's helpers.
func TestBuildSkipsTests(t *testing.T) {
	files := demo("package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return 3\n}\n")
	files["demo/main_test.ov"] = "package demo\n\nfunc TestX(io *ovid/io.Cap) i64 {\n  return nope\n}\n"
	dir := mkmod(t, files)
	var b bytes.Buffer
	if code := Build(dir, filepath.Join(t.TempDir(), "x"), &b); code != 0 {
		t.Fatalf("build: %d %s", code, b.String())
	}
	if canExec {
		b.Reset()
		if code := Run(dir, nil, &b); code != 3 {
			t.Fatalf("run: %d %s", code, b.String())
		}
	}
	b.Reset()
	if code := Check(dir, false, &b); code != ExitFail {
		t.Fatalf("check passed a broken test: %s", b.String())
	}

	files["demo/main.ov"] = "package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return helper()\n}\n"
	files["demo/main_test.ov"] = "package demo\n\nfunc helper() i64 {\n  return 0\n}\n"
	dir = mkmod(t, files)
	b.Reset()
	if code := Build(dir, filepath.Join(t.TempDir(), "x"), &b); code != ExitFail || !strings.Contains(b.String(), `"unknown_name"`) {
		t.Fatalf("build used a test helper: %d %s", code, b.String())
	}
}

// TestShowExprs: showing a statement lists its expressions with ids and
// hashes, and one of them can then be replaced on its own.
func TestShowExprs(t *testing.T) {
	dir := mkmod(t, demo("package demo\n\nimport ovid/io\n\ntype P struct {\n  v i64\n}\n\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1 + 2 * 3\n  return x\n}\n"))
	var b bytes.Buffer
	Show(dir, []string{"st:demo.main:1"}, true, true, false, &b)
	var exprs []any
	for _, ln := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		var r map[string]any
		if json.Unmarshal([]byte(ln), &r) == nil && r["exprs"] != nil {
			exprs, _ = r["exprs"].([]any)
		}
	}
	var mul map[string]any
	var texts []string
	for _, e := range exprs {
		e := e.(map[string]any)
		texts = append(texts, fmt.Sprint(e["text"]))
		if e["text"] == "2 * 3" {
			mul = e
		}
	}
	// Source order, outer before inner.
	if strings.Join(texts, "|") != "1 + 2 * 3|1|2 * 3|2|3" || mul == nil || mul["type"] != "i64" {
		t.Fatalf("exprs: %s", b.String())
	}
	b.Reset()
	Show(dir, []string{"st:demo.main:1"}, true, false, false, &b)
	if !strings.Contains(b.String(), "//   "+mul["id"].(string)+" 10:19 2 * 3  hash="+mul["hash"].(string)) {
		t.Fatalf("text: %s", b.String())
	}
	b.Reset()
	if Show(dir, []string{"main"}, true, false, false, &b); strings.Contains(b.String(), "//   ex:") {
		t.Fatalf("a decl lists exprs only with --exprs: %s", b.String())
	}
	b.Reset()
	if Show(dir, []string{"main"}, true, false, true, &b); !strings.Contains(b.String(), "//   ex:") {
		t.Fatalf("--exprs: %s", b.String())
	}
	b.Reset()
	if Show(dir, []string{"ty:demo.P"}, true, true, true, &b); !strings.Contains(b.String(), `"exprs":[]`) {
		t.Fatalf("a node without expressions lists []: %s", b.String())
	}
	b.Reset()
	op := EditOp{Op: "replace", ID: mul["id"].(string), Expect: mul["hash"].(string), Text: "(2 - 3)"}
	if code := EditOne(dir, op, "", EditOpts{}, &b); code != 0 {
		t.Fatalf("replace: %d %s", code, b.String())
	}
	src, _ := os.ReadFile(filepath.Join(dir, "demo", "main.ov"))
	if !strings.Contains(string(src), "var x i64 = 1 + (2 - 3)\n") {
		t.Fatalf("source: %s", src)
	}
}

func TestOneLine(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{`strlen("a  b")`, 72, `strlen("a  b")`},
		{"f(1,\n    2)", 72, "f(1, 2)"},
		{`strlen("héllo")`, 10, `strlen("h…`}, // byte 10 is inside é
	} {
		if got := oneLine(c.in, c.n); got != c.want {
			t.Errorf("oneLine(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// hashOf is the hash `ovid show` would print for id now.
func hashOf(t *testing.T, dir, id string) string {
	t.Helper()
	m, err := module.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m.Hash(id)
}

// TestPositionalIDsNeedExpect is the stale-id case: after an insert, st:1
// is a different statement, and an edit that read the old one must not
// land on the new one.
func TestPositionalIDsNeedExpect(t *testing.T) {
	dir := mkmod(t, demo("package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  return x\n}\n"))
	read := hashOf(t, dir, "st:demo.main:1") // an agent reads `var x i64 = 1`
	var b bytes.Buffer
	del := EditOp{Op: "delete", ID: "st:demo.main:1"}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{del}}, EditOpts{}, &b); code != ExitFail || last(t, b.String())["error"] != "expect_required" {
		t.Fatalf("no expect: %d %s", code, b.String())
	}
	// Someone else inserts before it.
	b.Reset()
	ins := EditOp{Op: "insert", Before: "st:demo.main:1", Expect: hashOf(t, dir, "fn:demo.main"), Text: "var y i64 = 2"}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{ins}}, EditOpts{}, &b); code != 0 {
		t.Fatalf("insert: %d %s", code, b.String())
	}
	b.Reset()
	del.Expect = read
	if code := runEdit(dir, &EditReq{Ops: []EditOp{del}}, EditOpts{}, &b); code != ExitStale ||
		!strings.Contains(fmt.Sprint(last(t, b.String())["message"]), "changed since you read") ||
		last(t, b.String())["hash"] != hashOf(t, dir, "st:demo.main:1") || last(t, b.String())["decl_hash"] != hashOf(t, dir, "fn:demo.main") {
		t.Fatalf("stale id: %d %s", code, b.String())
	}
	// A current hash passed with the wrong id names the node it belongs to.
	b.Reset()
	wrong := EditOp{Op: "delete", ID: "st:demo.main:1", Expect: hashOf(t, dir, "st:demo.main:2")}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{wrong}}, EditOpts{}, &b); code != ExitStale ||
		!strings.Contains(fmt.Sprint(last(t, b.String())["message"]), "belongs to st:demo.main:2") {
		t.Fatalf("wrong id: %d %s", code, b.String())
	}
	b.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{del}}, EditOpts{Force: true, AllowBroken: true}, &b); code != 0 {
		t.Fatalf("force: %d %s", code, b.String())
	}
}

const twinSrc = "package demo\n\nimport ovid/io\n\nfunc G() i64 {\n  var x i64 = 0\n  x = x + 1\n  x = x + 1\n  return x\n}\n\nfunc H() i64 {\n  var y i64 = 0\n  y = y + 1\n  return y\n}\n\nfunc main(io *ovid/io.Cap) i64 {\n  return G() + H()\n}\n"

// stmtsWith lists the statements of decl whose text is text, in order.
func stmtsWith(t *testing.T, dir, decl, text string) []string {
	t.Helper()
	m, err := module.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, id := range m.Order {
		if l := m.Index[id]; l.Kind == "stmt" && l.Decl == decl && m.Text(l.Span) == text {
			out = append(out, id)
		}
	}
	return out
}

func mainOv(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "demo/main.ov"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// edit1 runs one op with no flags.
func edit1(t *testing.T, dir string, op EditOp) (int, map[string]any) {
	t.Helper()
	var b bytes.Buffer
	code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{}, &b)
	return code, last(t, b.String())
}

// refused asserts that op is refused as stale and writes nothing.
func refused(t *testing.T, dir string, op EditOp) {
	t.Helper()
	before := mainOv(t, dir)
	code, r := edit1(t, dir, op)
	if code != ExitStale || r["error"] != "stale" || r["id"] != op.ID || r["hash"] == op.Expect {
		t.Fatalf("stale %v not refused: %d %v", op, code, r)
	}
	if after := mainOv(t, dir); after != before {
		t.Fatalf("a refused edit wrote:\n%s", after)
	}
}

// TestStaleTwin: a st: hash is bound to its decl, so a request read before
// the decl changed is refused, and never lands on a text-identical twin
// that now has its id (#22).
func TestStaleTwin(t *testing.T) {
	t.Run("delete replayed after it deleted one twin", func(t *testing.T) {
		dir := mkmod(t, demo(twinSrc))
		twins := stmtsWith(t, dir, "fn:demo.G", "x = x + 1")
		del := EditOp{Op: "delete", ID: twins[0], Expect: hashOf(t, dir, twins[0])}
		if code, r := edit1(t, dir, del); code != 0 {
			t.Fatalf("delete: %d %v", code, r)
		}
		// The survivor now has the deleted twin's id; a retry of the same
		// request (its answer lost, say) must not delete it too.
		if got := stmtsWith(t, dir, "fn:demo.G", "x = x + 1"); len(got) != 1 || got[0] != twins[0] {
			t.Fatalf("survivor %v", got)
		}
		refused(t, dir, del)
		// With the decl's hash read before, too.
		refused(t, dir, EditOp{Op: "delete", ID: twins[0], Expect: hashOf(t, mkmod(t, demo(twinSrc)), "fn:demo.G")})
	})
	t.Run("replace after another agent deleted a twin", func(t *testing.T) {
		dir := mkmod(t, demo(twinSrc))
		twins := stmtsWith(t, dir, "fn:demo.G", "x = x + 1")
		rep := EditOp{Op: "replace", ID: twins[0], Expect: hashOf(t, dir, twins[0]), Text: "x = x + 2"}
		if code, r := edit1(t, dir, EditOp{Op: "delete", ID: twins[0], Expect: hashOf(t, dir, twins[0])}); code != 0 {
			t.Fatalf("delete: %d %v", code, r)
		}
		refused(t, dir, rep)
	})
	t.Run("after a twin was inserted above", func(t *testing.T) {
		dir := mkmod(t, demo(twinSrc))
		first := stmtsWith(t, dir, "fn:demo.G", "x = x + 1")[0]
		del := EditOp{Op: "delete", ID: first, Expect: hashOf(t, dir, first)}
		rep := EditOp{Op: "replace", ID: first, Expect: hashOf(t, dir, first), Text: "x = x + 2"}
		ins := EditOp{Op: "insert", Before: first, Expect: hashOf(t, dir, "fn:demo.G"), Text: "x = x + 1"}
		if code, r := edit1(t, dir, ins); code != 0 {
			t.Fatalf("insert: %d %v", code, r)
		}
		refused(t, dir, del)
		refused(t, dir, rep)
	})
	t.Run("after the statements were reordered", func(t *testing.T) {
		src := strings.Replace(twinSrc, "  x = x + 1\n  x = x + 1\n", "  x = x + 1\n  x = x * 2\n  x = x + 1\n", 1)
		dir := mkmod(t, demo(src))
		second := stmtsWith(t, dir, "fn:demo.G", "x = x + 1")[1]
		rep := EditOp{Op: "replace", ID: second, Expect: hashOf(t, dir, second), Text: "x = x + 2"}
		reorder := EditOp{Op: "replace", ID: "fn:demo.G", Expect: hashOf(t, dir, "fn:demo.G"),
			Text: "func G() i64 {\n  var x i64 = 0\n  x = x * 2\n  x = x + 1\n  x = x + 1\n  return x\n}"}
		if code, r := edit1(t, dir, reorder); code != 0 {
			t.Fatalf("reorder: %d %v", code, r)
		}
		refused(t, dir, rep)
		refused(t, dir, EditOp{Op: "delete", ID: second, Expect: rep.Expect})
	})
}

// TestGuardsPerDecl: edits to different funcs, each guarded by what was
// read before either ran, both go through; of two edits to the same func
// read at the same time, the second is refused.
func TestGuardsPerDecl(t *testing.T) {
	dir := mkmod(t, demo(twinSrc))
	g := stmtsWith(t, dir, "fn:demo.G", "var x i64 = 0")[0]
	h := stmtsWith(t, dir, "fn:demo.H", "y = y + 1")[0]
	opG := EditOp{Op: "replace", ID: g, Expect: hashOf(t, dir, g), Text: "var x i64 = 5"}
	opH := EditOp{Op: "replace", ID: h, Expect: hashOf(t, dir, h), Text: "y = y + 2"}
	if code, r := edit1(t, dir, opG); code != 0 {
		t.Fatalf("G: %d %v", code, r)
	}
	if code, r := edit1(t, dir, opH); code != 0 {
		t.Fatalf("H after G: %d %v", code, r)
	}
	// Two agents read G; one changes its first statement, the other its
	// last. The second is refused, whichever hash it holds.
	ret := stmtsWith(t, dir, "fn:demo.G", "return x")[0]
	viaStmt := EditOp{Op: "replace", ID: ret, Expect: hashOf(t, dir, ret), Text: "return x + 1"}
	viaDecl := EditOp{Op: "replace", ID: ret, Expect: hashOf(t, dir, "fn:demo.G"), Text: "return x + 1"}
	first := EditOp{Op: "replace", ID: g, Expect: hashOf(t, dir, g), Text: "var x i64 = 6"}
	if code, r := edit1(t, dir, first); code != 0 {
		t.Fatalf("first: %d %v", code, r)
	}
	refused(t, dir, viaStmt)
	refused(t, dir, viaDecl)
	if src := mainOv(t, dir); !strings.Contains(src, "var x i64 = 6\n") || !strings.Contains(src, "y = y + 2\n") || !strings.Contains(src, "  return x\n") {
		t.Fatalf("source:\n%s", src)
	}
}

// TestEveryEditNeedsAGuard: an op on a decl id needs expect, a revision, or
// force, like one on a st: id; only an append of new decls to a package,
// which overwrites nothing, goes without (#22).
func TestEveryEditNeedsAGuard(t *testing.T) {
	dir := mkmod(t, demo(twinSrc))
	src := mainOv(t, dir)
	h := hashOf(t, dir, "fn:demo.H")
	body := "func H() i64 {\n  return 8\n}"
	for _, op := range []EditOp{
		{Op: "replace", ID: "fn:demo.H", Text: body},
		{Op: "replace", ID: "H", Text: body},
		{Op: "delete", ID: "fn:demo.H"},
		{Op: "insert", After: "fn:demo.H", Text: "func K() i64 {\n  return 1\n}"},
		{Op: "append", Into: "fn:demo.H", Text: "return 9"},
	} {
		if code, r := edit1(t, dir, op); code != ExitFail || r["error"] != "expect_required" || r["op"] != 0.0 {
			t.Errorf("%v unguarded: %d %v", op, code, r)
		}
	}
	// The same through a JSON batch.
	if r, code := editJSON(t, dir, []any{map[string]any{"op": "replace", "id": "fn:demo.H", "text": body}}); code != ExitFail || r["error"] != "expect_required" {
		t.Errorf("batch unguarded: %d %v", code, r)
	}
	// An expect on a package append would guard nothing.
	if code, r := edit1(t, dir, EditOp{Op: "append", Into: "demo", Expect: h, Text: "func K() i64 {\n  return 1\n}"}); code != ExitFail || r["error"] != "bad_edit" {
		t.Errorf("append with expect: %d %v", code, r)
	}
	if got := mainOv(t, dir); got != src {
		t.Fatalf("a refused edit wrote:\n%s", got)
	}

	// Two agents read H; the first replace goes through, the second is stale.
	a := EditOp{Op: "replace", ID: "fn:demo.H", Expect: h, Text: body}
	if code, r := edit1(t, dir, a); code != 0 {
		t.Fatalf("first: %d %v", code, r)
	}
	b := a
	b.Text = "func H() i64 {\n  return 9\n}"
	refused(t, dir, b)

	// A revision guards the whole request; a stale one refuses it.
	rev := last(t, func() string { var o bytes.Buffer; Check(dir, false, &o); return o.String() }())["revision"].(string)
	var out bytes.Buffer
	if code := runEdit(dir, &EditReq{Ops: []EditOp{{Op: "delete", ID: "fn:demo.H"}}}, EditOpts{Revision: "0123456789abcdef"}, &out); code != ExitStale {
		t.Fatalf("stale revision: %d %s", code, out.String())
	}
	out.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{{Op: "replace", ID: "fn:demo.H", Text: body}}}, EditOpts{Revision: rev}, &out); code != 0 {
		t.Fatalf("revision: %d %s", code, out.String())
	}
	// --force is the explicit opt-out.
	out.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{{Op: "replace", ID: "fn:demo.H", Text: "func H() i64 {\n  return 10\n}"}}}, EditOpts{Force: true}, &out); code != 0 {
		t.Fatalf("force: %d %s", code, out.String())
	}
	// It skips every guard, the request's stale revision included.
	out.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{{Op: "replace", ID: "fn:demo.H", Text: "func H() i64 {\n  return 11\n}"}}}, EditOpts{Revision: rev, Force: true}, &out); code != 0 {
		t.Fatalf("force with a stale revision: %d %s", code, out.String())
	}

	// A package append needs no guard, and replaying it is refused.
	k := EditOp{Op: "append", Into: "demo", Text: "func K() i64 {\n  return 1\n}"}
	if code, r := edit1(t, dir, k); code != 0 {
		t.Fatalf("append: %d %v", code, r)
	}
	src = mainOv(t, dir)
	if code, r := edit1(t, dir, k); code != ExitFail || r["error"] != "check" {
		t.Fatalf("replayed append: %d %v", code, r)
	}
	if got := mainOv(t, dir); got != src {
		t.Fatalf("a replayed append wrote:\n%s", got)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestEditFixAndStale(t *testing.T) {
	dir := mkmod(t, demo(addSrc))
	var sb bytes.Buffer
	Show(dir, []string{"main"}, true, true, false, &sb)
	h := lines(t, sb.String())[0]["hash"].(string)

	// Fix the bad argument by replacing the expression.
	r, code := editJSON(t, dir, map[string]any{"ops": []any{
		map[string]any{"op": "replace", "id": "ex:demo.main:2", "expect": h, "text": "2"},
	}}, "clean", "show")
	if code != 0 || r["check_ok"] != true {
		t.Fatalf("edit %d %v", code, r)
	}
	d := r["ops"].([]any)[0].(map[string]any)["decls"].([]any)[0].(map[string]any)
	if d["id"] != "fn:demo.main" || d["hash"] == h || !strings.Contains(d["text"].(string), "return Add(1, 2)") {
		t.Fatalf("decls %v", d)
	}
	out, code := buildRunDir(t, dir)
	if code != 3 {
		t.Fatalf("fixed program exit %d %s", code, out)
	}
	// The old hash of main is stale now.
	r, code = editJSON(t, dir, []any{map[string]any{"op": "delete", "id": "fn:demo.Add", "expect": h}})
	if code == 0 {
		t.Fatalf("expected failure %v", r)
	}
	r, code = editJSON(t, dir, []any{map[string]any{"op": "replace", "id": "main", "expect": h, "text": "func main(io *ovid/io.Cap) i64 {\n  return 0\n}"}})
	if code != ExitStale || r["error"] != "stale" || r["hash"] == h {
		t.Fatalf("stale %d %v", code, r)
	}
}

// TestEditStrictKeys: an unknown or repeated key in a request is refused
// before anything is written, naming the key and the op (#31).
func TestEditStrictKeys(t *testing.T) {
	src := "package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"
	dir := mkmod(t, demo(src))
	h := hashOf(t, dir, "fn:demo.main")
	body := `"func main(io *ovid/io.Cap) i64 {\n  return 1\n}"`
	for _, c := range []struct {
		name, req, key string
		op             any
	}{
		{"misspelled expect", `{"ops":[{"op":"replace","id":"fn:demo.main","expcet":"` + h + `","text":` + body + `}]}`, `"expcet"`, 0.0},
		{"expect in another case", `[{"op":"replace","id":"main","Expect":"` + h + `","text":` + body + `}]`, `"Expect"`, 0.0},
		{"repeated text", `{"ops":[{"op":"replace","id":"main","expect":"` + h + `","text":` + body + `,"text":"func main(io *ovid/io.Cap) i64 {\n  return 2\n}"}]}`, `"text" appears twice`, 0.0},
		{"second op", `{"ops":[{"op":"replace","id":"st:demo.main:1","expect":"` + h + `","text":"return 1"},{"op":"delete","idd":"st:demo.main:1"}]}`, `"idd"`, 1.0},
		{"one op on its own", `{"op":"replace","id":"main","expect":"` + h + `","txet":` + body + `}`, `"txet"`, 0.0},
		{"request key", `{"revison":"0123456789abcdef","ops":[{"op":"replace","id":"main","expect":"` + h + `","text":` + body + `}]}`, `"revison"`, nil},
		{"repeated ops", `{"ops":[],"ops":[{"op":"delete","id":"main","expect":"` + h + `"}]}`, `"ops" appears twice`, nil},
	} {
		p := filepath.Join(t.TempDir(), "edit.json")
		os.WriteFile(p, []byte(c.req), 0o644)
		var b bytes.Buffer
		code := Edit(dir, p, EditOpts{}, &b)
		r := last(t, b.String())
		if code != ExitFail || r["error"] != "bad_edit" || r["op"] != c.op || !strings.Contains(fmt.Sprint(r["message"]), c.key) {
			t.Errorf("%s: %d %s", c.name, code, b.String())
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov")); string(got) != src {
			t.Fatalf("%s: written:\n%s", c.name, got)
		}
	}
	// The same request spelled right goes through.
	r, code := editJSON(t, dir, map[string]any{"op": "replace", "id": "main", "expect": h, "text": "func main(io *ovid/io.Cap) i64 {\n  return 1\n}"})
	if code != 0 || r["written"] != true {
		t.Fatalf("well-formed: %d %v", code, r)
	}
}

// TestEditStrayFields: a field that belongs to another op is refused, not
// ignored. A replace that names a before was meant as an insert; doing a
// replace would drop the code the caller meant to keep.
func TestEditStrayFields(t *testing.T) {
	src := "package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  return x\n}\n"
	dir := mkmod(t, demo(src))
	h := hashOf(t, dir, "fn:demo.main")
	for _, c := range []struct {
		op  EditOp
		key string
	}{
		{EditOp{Op: "replace", ID: "st:demo.main:2", Before: "st:demo.main:2", Text: "return 0"}, `replace takes no "before"`},
		{EditOp{Op: "replace", ID: "st:demo.main:2", After: "st:demo.main:1", Text: "return 0"}, `replace takes no "after"`},
		{EditOp{Op: "replace", ID: "st:demo.main:2", File: "demo/x.ov", Text: "return 0"}, `replace takes no "file"`},
		{EditOp{Op: "delete", ID: "st:demo.main:1", Text: "var x i64 = 2"}, `delete takes no "text"`},
		{EditOp{Op: "delete", ID: "st:demo.main:1", Into: "fn:demo.main"}, `delete takes no "into"`},
		{EditOp{Op: "insert", ID: "st:demo.main:1", After: "st:demo.main:1", Text: "x = 2"}, `insert takes no "id"`},
		{EditOp{Op: "insert", After: "st:demo.main:1", File: "demo/x.ov", Text: "x = 2"}, `insert takes no "file"`},
		{EditOp{Op: "append", Into: "fn:demo.main", Before: "st:demo.main:2", Text: "x = 2"}, `append takes no "before"`},
		// Given, even empty: "before":"" or --before= is still a field
		// the op does not take.
		{EditOp{Op: "replace", ID: "st:demo.main:2", Text: "return 0", Given: []string{"before"}}, `replace takes no "before"`},
		{EditOp{Op: "delete", ID: "st:demo.main:1", Given: []string{"text"}}, `delete takes no "text"`},
	} {
		c.op.Expect = h
		// Both forms: one op from the command line, and a JSON request.
		var b bytes.Buffer
		code := EditOne(dir, c.op, "", EditOpts{}, &b)
		r := last(t, b.String())
		if code != ExitFail || r["error"] != "bad_edit" || !strings.Contains(fmt.Sprint(r["message"]), c.key) || r["hint"] == nil {
			t.Errorf("%s: %d %s", c.key, code, b.String())
		}
		// In JSON a given field is a key, here spelled out empty.
		raw, _ := json.Marshal(c.op)
		var req map[string]any
		json.Unmarshal(raw, &req)
		for _, k := range c.op.Given {
			req[k] = ""
		}
		r, code = editJSON(t, dir, req)
		if code != ExitFail || r["error"] != "bad_edit" || !strings.Contains(fmt.Sprint(r["message"]), c.key) {
			t.Errorf("%s (JSON): %d %v", c.key, code, r)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov")); string(got) != src {
			t.Fatalf("%s: written:\n%s", c.key, got)
		}
	}
}

// TestHelpCommand: ovid help <command> prints that command's entry, since
// agents ask for it by name; the edit ops share one entry.
func TestHelpCommand(t *testing.T) {
	for _, c := range []string{"init", "check", "build", "run", "test", "outline", "show", "refs", "grep",
		"replace", "insert", "append", "delete", "rename", "move", "dump", "version", "help"} {
		var b bytes.Buffer
		if code := Help(c, &b); code != ExitOK || !regexp.MustCompile(`(?m)(^ovid |\| )`+c+`\b`).MatchString(b.String()) {
			t.Errorf("help %s: %d\n%s", c, code, b.String())
		}
	}
	var b bytes.Buffer
	if Help("show", &b); strings.Contains(b.String(), "ovid refs") {
		t.Errorf("help show includes other entries:\n%s", b.String())
	}
	if code := Help("nope", &b); code != ExitUsage {
		t.Errorf("help nope: %d", code)
	}
}

func buildRunDir(t *testing.T, dir string) (string, int) {
	t.Helper()
	bin := filepath.Join(dir, "bin", "demo")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build %d:\n%s", code, b.String())
	}
	return run(t, bin)
}

func TestEditInsertAppend(t *testing.T) {
	dir := mkmod(t, demo("package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  return x\n}\n"))
	r, code := editJSON(t, dir, map[string]any{"ops": []any{
		map[string]any{"op": "insert", "after": "st:demo.main:1", "expect": hashOf(t, dir, "fn:demo.main"), "text": "x = Triple(x)\nwhile x < 20 {\n  x = x + 1\n}"},
		map[string]any{"op": "append", "into": "demo", "text": "  func Triple(v i64) i64 {\n    return v * 3\n  }"},
	}}, "clean")
	if code != 0 {
		t.Fatalf("edit %d %v", code, r)
	}
	src, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov"))
	want := "package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  x = Triple(x)\n  while x < 20 {\n    x = x + 1\n  }\n  return x\n}\n\nfunc Triple(v i64) i64 {\n  return v * 3\n}\n"
	if string(src) != want {
		t.Fatalf("got:\n%s", src)
	}
	ops := r["ops"].([]any)
	if ids := ops[1].(map[string]any)["ids"].([]any); len(ids) != 1 || ids[0] != "fn:demo.Triple" {
		t.Fatalf("new ids %v", ops)
	}
	if _, code := buildRunDir(t, dir); code != 20 {
		t.Fatalf("exit %d", code)
	}
	// A syntax error rejects the batch and writes nothing.
	r, code = editJSON(t, dir, []any{map[string]any{"op": "append", "into": "fn:demo.main", "expect": hashOf(t, dir, "fn:demo.main"), "text": "x = (1"}})
	if code == 0 || r["error"] != "syntax" {
		t.Fatalf("syntax %d %v", code, r)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov")); string(again) != want {
		t.Fatal("file changed after rejected edit")
	}
}

func TestRename(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": `package demo

import ovid/io
import demo/pt

func main(io *ovid/io.Cap) i64 {
  var p *demo/pt.Point = demo/pt.Make(io, 3)
  var x i64 = p.x
  p.x = x + demo/pt.Four() - demo/pt.Size
  return p.x + demo/pt.Size
}
`,
		"demo/pt/pt.ov": `package demo/pt

import ovid/io

const Size i64 = 4

type Point struct {
  x i64
}

func Make(io *ovid/io.Cap, x i64) *Point {
  var p *Point = ovid/io.Alloc(io, sizeof(Point) + Size) as *Point
  p.x = x
  return p
}

func Four() i64 {
  return Size
}
`,
	})
	for _, rn := range [][2]string{{"Make", "NewPoint"}, {"Point", "Pt"}, {"fld:demo/pt.Pt.x", "xx"}, {"Size", "Width"}, {"pa:demo/pt.NewPoint.x", "x0"}} {
		var b bytes.Buffer
		if code := Rename(dir, rn[0], rn[1], false, &b); code != 0 {
			t.Fatalf("rename %v: %s", rn, b.String())
		}
	}
	if pt, _ := os.ReadFile(filepath.Join(dir, "demo/pt/pt.ov")); !strings.Contains(string(pt), "sizeof(Pt) + Width") {
		t.Fatalf("pt.ov:\n%s", pt)
	}
	src, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov"))
	if !strings.Contains(string(src), "var p *demo/pt.Pt = demo/pt.NewPoint(io, 3)") || !strings.Contains(string(src), "p.xx = x + demo/pt.Four() - demo/pt.Width") {
		t.Fatalf("main.ov:\n%s", src)
	}
	if _, code := buildRunDir(t, dir); code != 7 {
		t.Fatalf("exit %d", code)
	}
	var b bytes.Buffer
	if code := Rename(dir, "Width", "NewPoint", false, &b); code == 0 {
		t.Fatalf("expected conflict: %s", b.String())
	}
}

// TestRunCrash: a program that faults under ovid run gets its exit code
// passed through, and stderr names the statement and the calls that led to it.
func TestRunCrash(t *testing.T) {
	needExec(t) // and ptrace, which linux has
	dir := mkmod(t, demo(`package demo

import ovid/io

type Node struct {
  v i64
}

func Get(n *Node) i64 {
  return n.v
}

func main(io *ovid/io.Cap) i64 {
  ovid/io.Print(strptr("before\n"))
  return Get(0 as *Node)
}
`))
	stdout, stderr := filepath.Join(dir, "out"), filepath.Join(dir, "err")
	code := withStdio(t, stdout, stderr, func() int { return Run(dir, nil, io.Discard) })
	if code != 128+11 {
		t.Fatalf("exit %d", code)
	}
	if out, _ := os.ReadFile(stdout); string(out) != "before\n" {
		t.Fatalf("stdout %q", out)
	}
	errb, _ := os.ReadFile(stderr)
	r := last(t, string(errb))
	at, _ := r["at"].(map[string]any)
	stack, _ := r["stack"].([]any)
	if r["error"] != "killed" || r["fault_addr"] != "0x0" || at["id"] != "st:demo.Get:1" || len(stack) != 2 {
		t.Fatalf("stderr: %s", errb)
	}
	if outer, _ := stack[1].(map[string]any); outer["id"] != "st:demo.main:2" {
		t.Fatalf("caller: %s", errb)
	}
}

// TestRunStoreLiteral: a store into a strptr literal faults, since rodata
// is read-only, and the hint says so; a store into a copy works.
func TestRunStoreLiteral(t *testing.T) {
	needExec(t) // and ptrace, which linux has
	src := `package demo

import ovid/io
import ovid/mem

func main(io *ovid/io.Cap) i64 {
  var p i64 = strptr("abc")
  COPY
  store8(p + 1, 66)
  return load8(p + 1)
}
`
	dir := mkmod(t, demo(strings.Replace(src, "COPY", "", 1)))
	stderr := filepath.Join(dir, "err")
	if code := withStdio(t, os.DevNull, stderr, func() int { return Run(dir, nil, io.Discard) }); code != 128+11 {
		t.Fatalf("exit %d", code)
	}
	errb, _ := os.ReadFile(stderr)
	r := last(t, string(errb))
	at, _ := r["at"].(map[string]any)
	if hint, _ := r["hint"].(string); !strings.Contains(hint, "string literal") || at["id"] != "st:demo.main:2" {
		t.Fatalf("stderr: %s", errb)
	}

	dir = mkmod(t, demo(strings.Replace(src, "COPY", "var b i64 = ovid/io.Alloc(io, 4)\n  ovid/mem.Copy(b, p, 4)\n  p = b", 1)))
	if code := withStdio(t, os.DevNull, stderr, func() int { return Run(dir, nil, io.Discard) }); code != 66 {
		t.Fatalf("store into a copy: exit %d", code)
	}
}

// TestRunStdio: a program that exits normally gets ovid run's stdin, writes
// its own stdout and stderr, and its exit code passes through, whether it
// runs traced or plainly (the fallback where ptrace is unavailable).
func TestRunStdio(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("ovid programs are linux/amd64 binaries")
	}
	dir := mkmod(t, demo(`package demo

import ovid/io

func main(io *ovid/io.Cap) i64 {
  var buf i64 = ovid/io.Alloc(io, 64)
  var n i64 = ovid/io.Read(0, buf, 64)
  ovid/io.Stdout(buf, n)
  ovid/io.Stderr(strptr("to stderr\n"), 10)
  return 3
}
`))
	in := filepath.Join(dir, "in")
	if err := os.WriteFile(in, []byte("from stdin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	check := func(how string, code int, out, errOut string) {
		t.Helper()
		o, _ := os.ReadFile(filepath.Join(dir, out))
		e, _ := os.ReadFile(filepath.Join(dir, errOut))
		if code != 3 || string(o) != "from stdin\n" || string(e) != "to stderr\n" {
			t.Fatalf("%s: exit %d, stdout %q, stderr %q", how, code, o, e)
		}
	}

	stdin, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	si := os.Stdin
	os.Stdin = stdin
	code := withStdio(t, filepath.Join(dir, "out"), filepath.Join(dir, "err"), func() int { return Run(dir, nil, io.Discard) })
	os.Stdin = si
	check("run", code, "out", "err")

	bin := filepath.Join(dir, "bin", "demo")
	var b bytes.Buffer
	if Build(dir, bin, &b) != 0 {
		t.Fatal(b.String())
	}
	for name, runner := range map[string]func(string, []string, procIO, time.Duration) procResult{"traced": runProc, "plain": runPlain} {
		stdin, err := os.Open(in)
		if err != nil {
			t.Fatal(err)
		}
		pr := runner(bin, nil, procIO{stdin: stdin, stdout: open(name + ".out"), stderr: open(name + ".err")}, 0)
		stdin.Close()
		if pr.err != nil || !pr.exited {
			t.Fatalf("%s: %+v", name, pr)
		}
		check(name, pr.code, name+".out", name+".err")
	}
}

// withStdio runs f with os.Stdout and os.Stderr sent to files.
func withStdio(t *testing.T, stdout, stderr string, f func() int) int {
	t.Helper()
	o, err := os.Create(stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	e, err := os.Create(stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	so, se := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = o, e
	defer func() { os.Stdout, os.Stderr = so, se }()
	return f()
}

func TestTestCommand(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": "package demo\nimport ovid/io\nfunc Two() i64 {\n  return 2\n}\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n",
		"demo/main_test.ov": `package demo
import ovid/io
import ovid/test
func TestTwo(io *ovid/io.Cap) i64 {
  if Two() != 2 {
    return 1
  }
  return 0
}
func TestFails(io *ovid/io.Cap) i64 {
  if !ovid/test.Eq(io, Two(), 3) {
    return 3
  }
  return 0
}
func TestCrash(io *ovid/io.Cap) i64 {
  return 1 / 0
}
type P struct {
  x i64
}
func get(p *P) i64 {
  return p.x
}
func TestNil(io *ovid/io.Cap) i64 {
  return get(0 as *P)
}
`,
	})
	var b bytes.Buffer
	// Listing compiles nothing, so it runs on every host.
	if code := Test(dir, "", true, &b); code != 0 || last(t, b.String())["count"] != float64(4) {
		t.Fatalf("list %d %s", code, b.String())
	}
	b.Reset()
	needExec(t)
	if code := Test(dir, "", false, &b); code != ExitFail {
		t.Fatalf("code %d %s", code, b.String())
	}
	got := map[string]map[string]any{}
	for _, r := range lines(t, b.String()) {
		if r["fact"] == "test" {
			got[r["id"].(string)] = r
		}
	}
	if got["fn:demo.TestTwo"]["ok"] != true || got["fn:demo.TestFails"]["exit"] != float64(3) || got["fn:demo.TestCrash"]["signal"] != "floating point exception" {
		t.Fatalf("results %v", got)
	}
	// A fault names the statement and the calls that led to it.
	nilT := got["fn:demo.TestNil"]
	if st, _ := nilT["stack"].([]any); nilT["fault_addr"] != "0x0" || len(st) != 2 ||
		st[0].(map[string]any)["id"] != "st:demo.get:1" || st[1].(map[string]any)["id"] != "st:demo.TestNil:1" {
		t.Fatalf("nil crash %v", nilT)
	}
	if at, _ := got["fn:demo.TestCrash"]["at"].(map[string]any); at["source"] != "  return 1 / 0" {
		t.Fatalf("div crash %v", got["fn:demo.TestCrash"])
	}
	if rb := got["fn:demo.TestFails"]["returned_by"].([]any); len(rb) != 1 || rb[0].(map[string]any)["source"] != "    return 3" ||
		got["fn:demo.TestFails"]["output"] != "got 2, want 3\n" {
		t.Fatalf("returned_by %v", got["fn:demo.TestFails"])
	}
	s := last(t, b.String())
	if s["passed"] != float64(1) || s["failed"] != float64(3) {
		t.Fatalf("summary %v", s)
	}
}

func TestRefsAndOutline(t *testing.T) {
	dir := mkmod(t, demo(addSrc))
	var b bytes.Buffer
	Refs(dir, "Add", Page{}, &b)
	rs := lines(t, b.String())
	if len(rs) != 2 || rs[0]["kind"] != "call" || rs[0]["line"] != float64(10) {
		t.Fatalf("refs %v", rs)
	}
	b.Reset()
	Outline(dir, "demo", false, false, Page{}, &b)
	if rs := lines(t, b.String()); len(rs) != 3 || rs[0]["sig"] != "func Add(a i64, b i64) i64" {
		t.Fatalf("outline %v", rs)
	}
	if s := rs[1]; s["files"] != float64(1) || s["external"] != float64(0) || s["by_pkg"].(map[string]any)["demo"] != float64(1) {
		t.Fatalf("refs summary %v", s)
	}
	b.Reset()
	Outline(dir, "demo", false, true, Page{}, &b)
	if rs := lines(t, b.String()); rs[0]["used_by"].(map[string]any)["demo"] != float64(1) {
		t.Fatalf("outline --uses %v", rs)
	}
}

func TestInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hello")
	var b bytes.Buffer
	if code := Init(dir, "", &b); code != 0 {
		t.Fatal(b.String())
	}
	b.Reset()
	needExec(t)
	if code := Test(dir, "", false, &b); code != 0 {
		t.Fatal(b.String())
	}
	if out, code := run(t, mustBuild(t, dir)); code != 0 || out != "hello, world\n" {
		t.Fatalf("%d %q", code, out)
	}
}

func mustBuild(t *testing.T, dir string) string {
	bin := filepath.Join(dir, "bin", "x")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatal(b.String())
	}
	return bin
}

// TestProgChecks checks the self-hosted CLI's source and its own tests.
func TestProgChecks(t *testing.T) {
	var b bytes.Buffer
	if code := Check(filepath.Join(repo(t), "prog"), false, &b); code != 0 {
		t.Fatalf("prog does not check:\n%s", b.String())
	}
}

// TestProgTests runs the self-hosted compiler's own Ovid tests.
func TestProgTests(t *testing.T) {
	var b bytes.Buffer
	needExec(t)
	if code := Test(filepath.Join(repo(t), "prog"), "", false, &b); code != 0 {
		t.Fatalf("prog tests fail:\n%s", b.String())
	}
}

func TestMove(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": `package demo

import ovid/io
import demo/geo

func main(io *ovid/io.Cap) i64 {
  var p *demo/geo.Point = demo/geo.Make(io, 3)
  return demo/geo.Twice(p.x) + Base
}
`,
		"demo/geo/geo.ov": `package demo/geo

import ovid/io

const Scale i64 = 2

type Point struct {
  x i64
}

// Make allocates a point.
func Make(io *ovid/io.Cap, x i64) *Point {
  var p *Point = ovid/io.Alloc(io, 8) as *Point
  p.x = x
  return p
}

func Twice(v i64) i64 {
  return v * Scale
}
`,
		"demo/base.ov": "package demo\n\nconst Base i64 = 1\n",
	})
	steps := [][2]string{
		{"Twice", "demo/num"}, // to a new package; needs demo/geo for Scale
		{"Base", "demo/geo"},  // demo used it unqualified; now it qualifies
		{"Make", "demo/num"},  // body names Point (stays in geo) and ovid/io
	}
	for _, s := range steps {
		var b bytes.Buffer
		if code := Move(dir, s[0], s[1], "", false, &b); code != 0 {
			t.Fatalf("move %v: %s", s, b.String())
		}
	}
	num, _ := os.ReadFile(filepath.Join(dir, "demo/num/num.ov"))
	for _, want := range []string{"import demo/geo", "import ovid/io", "return v * demo/geo.Scale", "// Make allocates a point.\nfunc Make(io *ovid/io.Cap, x i64) *demo/geo.Point {", "as *demo/geo.Point"} {
		if !strings.Contains(string(num), want) {
			t.Fatalf("num.ov lacks %q:\n%s", want, num)
		}
	}
	main, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov"))
	if !strings.Contains(string(main), "demo/num.Make(io, 3)") || !strings.Contains(string(main), "demo/num.Twice(p.x) + demo/geo.Base") || !strings.Contains(string(main), "import demo/num") {
		t.Fatalf("main.ov:\n%s", main)
	}
	if _, code := buildRunDir(t, dir); code != 7 {
		t.Fatalf("exit %d", code)
	}
	var b bytes.Buffer
	if code := Move(dir, "main", "demo/geo", "", false, &b); code == 0 {
		t.Fatal("moved main")
	}
}

// TestBinaryShape checks the emitted ELF: no segment is both writable and
// executable, and funcs main cannot reach are left out.
func TestBinaryShape(t *testing.T) {
	dir := mkmod(t, demo(`package demo
import ovid/io
func Unused() i64 {
  ovid/io.Stdout(strptr("never printed"), strlen("never printed"))
  return 1
}
func main(io *ovid/io.Cap) i64 {
  ovid/io.Stdout(strptr("hi\n"), strlen("hi\n"))
  return 0
}
`))
	bin := mustBuild(t, dir)
	if out, code := run(t, bin); code != 0 || out != "hi\n" {
		t.Fatalf("%d %q", code, out)
	}
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Flags&elf.PF_W != 0 && p.Flags&elf.PF_X != 0 {
			t.Fatalf("segment at %#x is writable and executable", p.Vaddr)
		}
	}
	raw, _ := os.ReadFile(bin)
	if bytes.Contains(raw, []byte("never printed")) {
		t.Fatal("unreachable func was emitted")
	}
	if len(raw) > 2048 {
		t.Fatalf("hello binary is %d bytes", len(raw))
	}
}

func TestEditOne(t *testing.T) {
	dir := mkmod(t, demo("package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return 1\n}\n"))
	text := filepath.Join(t.TempDir(), "text.ov")
	// Quotes and newlines need no escaping; the trailing newline is dropped.
	os.WriteFile(text, []byte("ovid/io.Stdout(strptr(\"a \\\"b\\\"\\n\"), strlen(\"a \\\"b\\\"\\n\"))\n"), 0o644)
	var b bytes.Buffer
	if code := EditOne(dir, EditOp{Op: "insert", Before: "st:demo.main:1", Expect: hashOf(t, dir, "fn:demo.main")}, text, EditOpts{RequireClean: true}, &b); code != 0 {
		t.Fatalf("insert %d %s", code, b.String())
	}
	b.Reset()
	if code := EditOne(dir, EditOp{Op: "delete", ID: "st:demo.main:1", Expect: hashOf(t, dir, "st:demo.main:1")}, "", EditOpts{DryRun: true}, &b); code != 0 || last(t, b.String())["written"] != false {
		t.Fatalf("delete dry-run %d %s", code, b.String())
	}
	if out, code := buildRunDir(t, dir); code != 1 || out != "a \"b\"\n" {
		t.Fatalf("exit %d out %q", code, out)
	}
}

// TestConcurrentEdits: writers that touch the same file run one after
// another under the module lock, so none of their changes is lost.
func TestConcurrentEdits(t *testing.T) {
	dir := mkmod(t, demo("package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	const n = 40
	codes := make([]int, n)
	outs := make([]bytes.Buffer, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op := EditOp{Op: "append", Into: "demo", Text: fmt.Sprintf("func F%d() i64 {\n  return %d\n}", i, i)}
			codes[i] = runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{}, &outs[i])
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 0 {
			t.Fatalf("writer %d: %d %s", i, c, outs[i].String())
		}
	}
	m, err := module.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, p := range m.Prog.Packages {
		if p.Path == "demo" {
			got = len(p.Funcs)
		}
	}
	if got != n+1 {
		t.Fatalf("%d funcs after %d appends; want %d", got, n, n+1)
	}
}

// TestEditFailsClosed: an edit that adds a check error is refused unless
// --allow-broken, and a dry run of it reports failure.
func TestEditFailsClosed(t *testing.T) {
	src := "package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"
	dir := mkmod(t, demo(src))
	op := EditOp{Op: "replace", ID: "st:demo.main:1", Text: "return true"}
	var b bytes.Buffer
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Force: true}, &b); code != ExitFail || last(t, b.String())["error"] != "check" {
		t.Fatalf("default: %d %s", code, b.String())
	}
	b.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{AllowBroken: true, DryRun: true, Force: true}, &b); code != ExitFail || last(t, b.String())["ok"] != false {
		t.Fatalf("dry run: %d %s", code, b.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov")); string(got) != src {
		t.Fatalf("written:\n%s", got)
	}
	b.Reset()
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{AllowBroken: true, Force: true}, &b); code != 0 || last(t, b.String())["check_ok"] != false {
		t.Fatalf("allow-broken: %d %s", code, b.String())
	}
}

// TestEditGuardComparesErrors: trading one error for another is not "no
// worse", even though the count is the same.
func TestEditGuardComparesErrors(t *testing.T) {
	src := "package demo\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return true\n}\n"
	dir := mkmod(t, demo(src))
	var b bytes.Buffer
	op := EditOp{Op: "replace", ID: "st:demo.main:1", Text: "return nope"}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Force: true}, &b); code != ExitFail {
		t.Fatalf("swap accepted: %d %s", code, b.String())
	}
	if rs := lines(t, b.String()); len(rs) != 2 || !strings.Contains(fmt.Sprint(rs[0]["message"]), "nope") {
		t.Fatalf("want only the new error reported: %v", rs)
	}
	b.Reset()
	op.Text = "return 0"
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Force: true}, &b); code != 0 || last(t, b.String())["check_ok"] != true {
		t.Fatalf("fix refused: %d %s", code, b.String())
	}
}

func TestMoveManyRollsBack(t *testing.T) {
	files := map[string]string{
		"demo/main.ov":    "package demo\n\nimport ovid/io\n\nconst K i64 = 2\n\nfunc main(io *ovid/io.Cap) i64 {\n  return K\n}\n",
		"demo/geo/geo.ov": "package demo/geo\n\nconst Taken i64 = 1\n",
	}
	dir := mkmod(t, files)
	var b bytes.Buffer
	// K moves, then main is refused: everything is put back.
	if code := MoveMany(dir, []string{"K", "main"}, "demo/geo", "", false, &b); code == 0 || last(t, b.String())["error"] != "rolled_back" {
		t.Fatalf("expected rollback %d %s", code, b.String())
	}
	for rel, want := range files {
		if got, _ := os.ReadFile(filepath.Join(dir, rel)); string(got) != want {
			t.Fatalf("%s not restored:\n%s", rel, got)
		}
	}
	// Into a new package, dry-run: moves and restores, removing the new dir.
	b.Reset()
	if code := MoveMany(dir, []string{"K", "Taken"}, "demo/num", "", true, &b); code != 0 {
		t.Fatalf("dry-run %d %s", code, b.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "demo/num")); !os.IsNotExist(err) {
		t.Fatal("dry-run left demo/num behind")
	}
	b.Reset()
	if code := MoveMany(dir, []string{"K", "Taken"}, "demo/num", "", false, &b); code != 0 {
		t.Fatalf("move %d %s", code, b.String())
	}
	if _, code := buildRunDir(t, dir); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

// TestSelfHost builds the self-hosted compiler with this one, then has it
// build itself and a small program: each output must be byte-identical to
// what the Go compiler produced from the same source.
func TestSelfHost(t *testing.T) {
	prog := filepath.Join(repo(t), "prog")
	stdDir := filepath.Join(repo(t), "std")
	tmp := t.TempDir()
	g1 := filepath.Join(tmp, "g1")
	var b bytes.Buffer
	if code := Build(prog, g1, &b); code != 0 {
		t.Fatalf("go build of prog: %s", b.String())
	}
	s1 := filepath.Join(tmp, "s1")
	if out, code := run(t, g1, "build", prog, "-o", s1, "--std", stdDir); code != 0 {
		t.Fatalf("self-hosted build of prog %d: %s", code, out)
	}
	same := func(a, b string) {
		t.Helper()
		x, _ := os.ReadFile(a)
		y, _ := os.ReadFile(b)
		if len(x) == 0 || !bytes.Equal(x, y) {
			t.Fatalf("%s (%d bytes) and %s (%d bytes) differ", a, len(x), b, len(y))
		}
	}
	same(g1, s1)

	hello := filepath.Join(tmp, "hello")
	b.Reset()
	if code := Init(hello, "", &b); code != 0 {
		t.Fatal(b.String())
	}
	hg := mustBuild(t, hello)
	hs := filepath.Join(tmp, "hs")
	if out, code := run(t, s1, "build", hello, "-o", hs, "--std", stdDir); code != 0 {
		t.Fatalf("self-hosted build of hello %d: %s", code, out)
	}
	same(hg, hs)
	if out, code := run(t, hs); code != 0 || out != "hello, world\n" {
		t.Fatalf("%d %q", code, out)
	}

	// Both compilers compute one revision, over ovid.mod, the module, and
	// the std they loaded: prog names a std directory, hello uses the
	// built-in one on the Go side and the same files by --std here.
	for _, dir := range []string{prog, hello} {
		b.Reset()
		Check(dir, false, &b)
		want := last(t, b.String())["revision"]
		out, _ := run(t, s1, "check", dir, "--std", stdDir)
		if got := last(t, out)["revision"]; got != want || want == nil {
			t.Fatalf("revision of %s: self-hosted %v, go %v", dir, got, want)
		}
	}

	// The self-hosted checker names each error's node and source line, and
	// reports a second decl of a name once, at that decl.
	bad := mkmod(t, demo("package demo\nimport ovid/io\nfunc F() i64 {\n  return 1\n}\nfunc F() i64 {\n  return 2\n}\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = true\n  return x\n}\n"))
	out, code := run(t, s1, "check", bad, "--std", stdDir)
	ds := lines(t, out)
	if code != 1 || len(ds) != 3 {
		t.Fatalf("self-hosted check %d: %s", code, out)
	}
	if d := ds[0]; d["code"] != "duplicate_name" || d["id"] != "fn:demo.F" || d["line"] != float64(6) {
		t.Fatalf("duplicate: %v", d)
	}
	if d := ds[1]; d["code"] != "type_mismatch" || d["id"] != "ex:demo.main:1" || d["func"] != "main" || d["line"] != float64(10) || d["col"] != float64(15) || d["source"] != "  var x i64 = true" {
		t.Fatalf("mismatch: %v", d)
	}
	if d := ds[2]; d["ok"] != false || d["errors"] != float64(2) {
		t.Fatalf("summary: %v", d)
	}

	// A var's value is checked before its name is in scope, by both
	// checkers, so check fails where build would.
	self := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = x\n  return x\n}\n"))
	b.Reset()
	Check(self, false, &b)
	gd := lines(t, b.String())[0]
	out, code = run(t, s1, "check", self, "--std", stdDir)
	if d := lines(t, out)[0]; code != 1 || d["code"] != "unknown_name" || gd["code"] != "unknown_name" || d["line"] != gd["line"] || d["col"] != gd["col"] {
		t.Fatalf("var x = x: self-hosted %d %s; go %v", code, out, gd)
	}

	// Like this compiler, its build prints only the receipt, or the errors
	// and their count.
	if out, _ := run(t, s1, "build", hello, "-o", hs, "--std", stdDir); len(lines(t, out)) != 1 || last(t, out)["output"] != hs {
		t.Fatalf("build receipt: %s", out)
	}
	out, code = run(t, s1, "build", bad, "-o", hs, "--std", stdDir)
	if d := last(t, out); code != 1 || len(lines(t, out)) != 3 || d["ok"] != false || d["errors"] != float64(2) || d["fact"] != nil {
		t.Fatalf("failed build %d: %s", code, out)
	}
	// A module that does not parse ends the same way for build, and with
	// the summary for check.
	broken := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return (\n}\n"))
	out, code = run(t, s1, "build", broken, "-o", hs, "--std", stdDir)
	if d := last(t, out); code != 1 || len(lines(t, out)) != 2 || lines(t, out)[0]["code"] != "syntax" || d["ok"] != false || d["errors"] != float64(1) || d["fact"] != nil {
		t.Fatalf("build of a syntax error %d: %s", code, out)
	}
	out, code = run(t, s1, "check", broken, "--std", stdDir)
	if d := last(t, out); code != 1 || d["fact"] != "summary" || d["ok"] != false || d["errors"] != float64(1) {
		t.Fatalf("check of a syntax error %d: %s", code, out)
	}

	// Both dumps are valid JSON and say the same thing. A literal's bytes
	// that are not UTF-8 (prog's asm tests have some) come out as
	// value_hex, which loses nothing.
	lit := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return load8(strptr(\"\\xb8\\n\") + 1) + strlen(\"é\")\n}\n"))
	for _, dir := range []string{prog, lit} {
		b.Reset()
		Dump(dir, "", "", &b)
		out, code := run(t, s1, "dump", dir, "--std", stdDir)
		var g, o any
		if !utf8.ValidString(out) || json.Unmarshal(b.Bytes(), &g) != nil || json.Unmarshal([]byte(out), &o) != nil || code != 0 {
			t.Fatalf("dump of %s is not valid JSON (%d)", dir, code)
		}
		if !reflect.DeepEqual(g, o) {
			t.Fatalf("dumps of %s differ", dir)
		}
	}
	if b.Reset(); Dump(lit, "", "", &b) != 0 || !strings.Contains(b.String(), `"value_hex": "b80a"`) || !strings.Contains(b.String(), `"value": "é"`) {
		t.Fatalf("dump of literals: %s", b.String())
	}
	// A source line that is not UTF-8 is still valid JSON in a diagnostic.
	badSrc := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return true // \xff\xe2\x82\n}\n"))
	b.Reset()
	Check(badSrc, false, &b)
	out, _ = run(t, s1, "check", badSrc, "--std", stdDir)
	if !utf8.ValidString(out) || lines(t, out)[0]["source"] != lines(t, b.String())[0]["source"] {
		t.Fatalf("diagnostic source: self-hosted %q, go %q", out, b.String())
	}

	// Its build leaves out _test.ov files too, so an error in one does not
	// stop it, and the two binaries still agree.
	tbad := filepath.Join(hello, "hello", "bad_test.ov")
	if err := os.WriteFile(tbad, []byte("package hello\nfunc TestBad() i64 {\n  return nope\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := run(t, s1, "build", hello, "-o", hs, "--std", stdDir); code != 0 {
		t.Fatalf("self-hosted build with a broken test %d: %s", code, out)
	}
	same(mustBuild(t, hello), hs)
	if _, code := run(t, s1, "check", hello, "--std", stdDir); code != 1 {
		t.Fatalf("self-hosted check passed a broken test")
	}
}

// TestSelfHostLarge builds a program far larger than the compiler with both
// compilers and compares the output. It is sized to cross what used to be
// fixed limits in the self-hosted one: a call chain 30000 funcs deep (the
// reachability walk recursed along it), more than 65536 distinct strings
// with repeats among them, and code, label, and fixup tables that must grow.
func TestSelfHostLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("large program")
	}
	dir := mkmod(t, demo(largeSource(30000)))
	g := mustBuild(t, dir)

	tmp := t.TempDir()
	s1 := filepath.Join(tmp, "s1")
	var b bytes.Buffer
	if code := Build(filepath.Join(repo(t), "prog"), s1, &b); code != 0 {
		t.Fatalf("go build of prog: %s", b.String())
	}
	s := filepath.Join(tmp, "big")
	if out, code := run(t, s1, "build", dir, "-o", s, "--std", filepath.Join(repo(t), "std")); code != 0 {
		t.Fatalf("self-hosted build %d: %s", code, out)
	}
	x, _ := os.ReadFile(g)
	y, _ := os.ReadFile(s)
	if len(x) == 0 || !bytes.Equal(x, y) {
		t.Fatalf("outputs differ: go %d bytes, self-hosted %d bytes", len(x), len(y))
	}
	_, want := run(t, g)
	if _, got := run(t, s); got != want {
		t.Fatalf("exit %d, want %d", got, want)
	}
}

// TestRevisionCoversBuild: the revision moves when ovid.mod changes, not
// only when a module file does.
func TestRevisionCoversBuild(t *testing.T) {
	files := demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n")
	files["alt/main.ov"] = "package alt\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 2\n}\n"
	dir := mkmod(t, files)
	rev := func() string {
		t.Helper()
		var b bytes.Buffer
		if code := Check(dir, false, &b); code != 0 {
			t.Fatalf("check: %s", b.String())
		}
		r, _ := last(t, b.String())["revision"].(string)
		if len(r) != 16 {
			t.Fatalf("revision %q", r)
		}
		return r
	}
	r0 := rev()
	if rev() != r0 {
		t.Fatal("the revision is not stable")
	}
	if err := os.WriteFile(filepath.Join(dir, "ovid.mod"), []byte("module demo\nentry alt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rev() == r0 {
		t.Fatal("changing the entry in ovid.mod kept the revision")
	}
}

// stdCopy copies the shipped packages a hello-sized program loads into a
// fresh directory, for the self-hosted compiler's --std.
func stdCopy(t *testing.T) string {
	t.Helper()
	stdDir := t.TempDir()
	for _, rel := range []string{"ovid/io/io.ov", "ovid/mem/mem.ov"} {
		src, err := os.ReadFile(filepath.Join(repo(t), "std", rel))
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(filepath.Join(stdDir, rel)), 0o755)
		if err := os.WriteFile(filepath.Join(stdDir, rel), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return stdDir
}

// TestRevisionCoversStd: the revision also moves when a loaded file of the
// standard library changes. The Go toolchain's is built in, so this is
// shown with the self-hosted compiler, whose --std is a directory.
func TestRevisionCoversStd(t *testing.T) {
	needExec(t)
	self := mustBuild(t, filepath.Join(repo(t), "prog"))
	stdDir := stdCopy(t)
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	rev := func() string {
		t.Helper()
		out, code := run(t, self, "check", dir, "--std", stdDir)
		r, _ := last(t, out)["revision"].(string)
		if code != 0 || len(r) != 16 {
			t.Fatalf("check %d: %s", code, out)
		}
		return r
	}
	r1 := rev()
	io := filepath.Join(stdDir, "ovid/io/io.ov")
	src, _ := os.ReadFile(io)
	if err := os.WriteFile(io, append(src, []byte("\n// changed\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := rev()
	if r2 == r1 {
		t.Fatal("changing a loaded std file kept the revision")
	}
	// A std file that is not loaded does not count.
	mem := filepath.Join(stdDir, "ovid/mem/mem.ov")
	src, _ = os.ReadFile(mem)
	os.WriteFile(mem, append(src, []byte("\n// changed\n")...), 0o644)
	if rev() != r2 {
		t.Fatal("changing a std file that is not loaded moved the revision")
	}
}

// TestSyscallNeedsShippedPackage: the checker allows syscall by where
// ovid/io came from, not by its name alone. A package with the name and
// without the origin cannot be loaded today (reserved_path), so the origin
// is cleared by hand here.
func TestSyscallNeedsShippedPackage(t *testing.T) {
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  ovid/io.Alloc(io, 8)\n  return 0\n}\n"))
	m, err := module.Load(dir)
	if err != nil || len(m.Errors) != 0 {
		t.Fatal(err, m.Errors)
	}
	count := func() int {
		n := 0
		for _, is := range check.Run(m.Prog).Issues {
			if is.Code == "syscall_forbidden" {
				n++
			}
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("%d syscall_forbidden in the shipped ovid/io", n)
	}
	for i := range m.Prog.Packages {
		if p := &m.Prog.Packages[i]; p.Path == "ovid/io" {
			if !p.Sys {
				t.Fatal("the shipped ovid/io is not marked as the toolchain's")
			}
			p.Sys = false
		} else if p.Sys && p.Path == "demo" {
			t.Fatal("a module package is marked as the toolchain's")
		}
	}
	if count() == 0 {
		t.Fatal("an ovid/io that is not the toolchain's may call syscall")
	}
}

// TestModuleCannotChooseStd: the right to call syscall belongs to the
// standard library the toolchain brings, and a module has no way to supply
// one: not by a std line in ovid.mod, and not by a package of the same path
// (tests/fail/reserved_path). Both compilers refuse the std line.
func TestModuleCannotChooseStd(t *testing.T) {
	evil := t.TempDir()
	os.MkdirAll(filepath.Join(evil, "ovid/io"), 0o755)
	os.WriteFile(filepath.Join(evil, "ovid/io/io.ov"), []byte("package ovid/io\ntype Cap struct {\n  argc i64\n  argv i64\n  heap i64\n  used i64\n  size i64\n}\nfunc Pwn() i64 {\n  return syscall(60, 42, 0, 0, 0, 0, 0)\n}\n"), 0o644)
	files := demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return ovid/io.Pwn()\n}\n")
	files["ovid.mod"] = "module demo\nentry demo\nstd " + evil + "\n"
	dir := mkmod(t, files)
	var b bytes.Buffer
	if code := Check(dir, false, &b); code != ExitFail {
		t.Fatalf("check exit %d: %s", code, b.String())
	}
	if r := last(t, b.String()); r["ok"] != false || r["error"] != "load" || !strings.Contains(r["message"].(string), "std line is no longer supported") {
		t.Fatalf("%v", r)
	}
	b.Reset()
	if code := Build(dir, filepath.Join(t.TempDir(), "x"), &b); code == ExitOK {
		t.Fatalf("build succeeded: %s", b.String())
	}

	needExec(t)
	self := mustBuild(t, filepath.Join(repo(t), "prog"))
	out, code := run(t, self, "check", dir, "--std", filepath.Join(repo(t), "std"))
	if rs := lines(t, out); code != 1 || rs[0]["code"] != "mod" || !strings.Contains(rs[0]["message"].(string), "std line is no longer supported") ||
		!strings.Contains(rs[0]["message"].(string), "--std") || strings.Contains(rs[0]["message"].(string), "built into") {
		t.Fatalf("self-hosted check %d: %s", code, out)
	}
	// An operator who points --std at a library whose Cap is not the
	// runtime's is told so; a module can no longer reach this.
	short := t.TempDir()
	os.MkdirAll(filepath.Join(short, "ovid/io"), 0o755)
	os.WriteFile(filepath.Join(short, "ovid/io/io.ov"), []byte("package ovid/io\ntype Cap struct {\n  argc i64\n  heap i64\n}\n"), 0o644)
	plain := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	out, code = run(t, self, "check", plain, "--std", short)
	if rs := lines(t, out); code != 1 || rs[0]["code"] != "bad_abi" {
		t.Fatalf("self-hosted check with a short Cap %d: %s", code, out)
	}
}

func TestHints(t *testing.T) {
	dir0 := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return sizeof(i64) + sizeof(Nope)\n}\n"))
	var b0 bytes.Buffer
	Check(dir0, false, &b0)
	if ds := lines(t, b0.String()); ds[0]["message"] != "sizeof(i64) is always 8" || !strings.Contains(ds[1]["message"].(string), "unknown type demo.Nope") {
		t.Fatalf("sizeof %v", ds)
	}
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  while true {\n    break\n  }\n  return 0\n}\n"))
	var b bytes.Buffer
	Check(dir, false, &b)
	if d := lines(t, b.String())[0]; d["message"] != "there is no break" || !strings.Contains(d["hint"].(string), "flag") {
		t.Fatalf("break %v", d)
	}
	dir = mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  F(&x)\n  return 0\n}\n"))
	b.Reset()
	Check(dir, false, &b)
	if d := lines(t, b.String())[0]; !strings.Contains(d["message"].(string), "no address-of") {
		t.Fatalf("& %v", d)
	}
}
