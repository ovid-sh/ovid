package tool

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ovid/internal/module"
)

const (
	dupA = "package app\n\nimport ovid/io\n\nfunc F() i64 {\n  return 1\n}\n\nfunc main(io *ovid/io.Cap) i64 {\n  return F()\n}\n"
	dupB = "package app\n\n// F again.\nfunc F() i64 {\n  return 2\n}\n"
)

// dupMod is a module where a.ov and b.ov of package app both declare F.
func dupMod(t *testing.T, b string) string {
	t.Helper()
	t.Setenv(module.PathsEnv, "module")
	return mkmod(t, map[string]string{"ovid.mod": "module app\nentry app\n", "app/a.ov": dupA, "app/b.ov": b})
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// copyHashes is the hash of each copy of id, by file, from outline.
func copyHashes(t *testing.T, dir, id string) map[string]string {
	t.Helper()
	var b bytes.Buffer
	if code := Outline(dir, "app", false, false, Page{}, &b); code != ExitOK {
		t.Fatalf("outline %d %s", code, b.String())
	}
	out := map[string]string{}
	for _, r := range lines(t, b.String()) {
		if r["id"] == id {
			out[fmt.Sprint(r["file"])] = fmt.Sprint(r["hash"])
		}
	}
	return out
}

// TestDuplicateIDOutlineShow: each copy of a duplicated id is indexed, and
// outline and show list each at its own file and line with its own hash.
func TestDuplicateIDOutlineShow(t *testing.T) {
	dir := dupMod(t, dupB)
	var b bytes.Buffer
	if code := Check(dir, false, &b); code != ExitFail || !strings.Contains(b.String(), `"code":"duplicate_name"`) {
		t.Fatalf("check %d %s", code, b.String())
	}
	b.Reset()
	Outline(dir, "app", false, false, Page{}, &b)
	var fs []map[string]any
	for _, r := range lines(t, b.String()) {
		if r["id"] == "fn:app.F" {
			fs = append(fs, r)
		}
	}
	if len(fs) != 2 || fs[0]["file"] != "app/a.ov" || fs[0]["line"] != 5.0 || fs[1]["file"] != "app/b.ov" || fs[1]["line"] != 4.0 ||
		fs[1]["doc"] != "F again." || fs[0]["hash"] == fs[1]["hash"] || fs[0]["id_copies"] != 2.0 || fs[1]["id_copies"] != 2.0 {
		t.Fatalf("outline of F: %v", fs)
	}
	m, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs := m.Copies("fn:app.F")
	if len(cs) != 2 || m.LocHash(cs[0]) != fs[0]["hash"] || m.LocHash(cs[1]) != fs[1]["hash"] || m.Hash("fn:app.F") != fs[0]["hash"] {
		t.Fatalf("copies %v", cs)
	}

	b.Reset()
	if code := Show(dir, []string{"F"}, false, false, false, &b); code != ExitOK {
		t.Fatalf("show %d %s", code, b.String())
	}
	want := fmt.Sprintf("// func fn:app.F app/a.ov:5-7 hash=%s\nfunc F() i64 {\n  return 1\n}\n"+
		"// func fn:app.F app/b.ov:3-6 hash=%s\n// F again.\nfunc F() i64 {\n  return 2\n}\n", fs[0]["hash"], fs[1]["hash"])
	if b.String() != want {
		t.Fatalf("show F:\n%s\nwant:\n%s", b.String(), want)
	}
	b.Reset()
	if code := Show(dir, []string{"st:app.F:1"}, false, true, false, &b); code != ExitOK {
		t.Fatalf("show --json %d %s", code, b.String())
	}
	rs := lines(t, b.String())
	if len(rs) != 3 || rs[0]["file"] != "app/a.ov" || rs[0]["text"] != "return 1" || rs[1]["file"] != "app/b.ov" || rs[1]["text"] != "return 2" ||
		rs[0]["hash"] == rs[1]["hash"] || last(t, b.String())["count"] != 2.0 {
		t.Fatalf("show --json st:app.F:1: %v", rs)
	}
	// The expression inside each copy is that copy's.
	ex0, _ := rs[0]["exprs"].([]any)
	ex1, _ := rs[1]["exprs"].([]any)
	if len(ex0) != 1 || len(ex1) != 1 || ex0[0].(map[string]any)["text"] != "1" || ex1[0].(map[string]any)["text"] != "2" {
		t.Fatalf("exprs %v %v", ex0, ex1)
	}
	// grep places a match in b.ov in b.ov's copy.
	b.Reset()
	Grep(dir, "return 2", "", false, 0, 0, &b)
	if r := lines(t, b.String())[0]; r["decl"] != "fn:app.F" || r["stmt"] != "st:app.F:1" {
		t.Fatalf("grep %v", r)
	}
}

// TestDuplicateIDEdit: an edit to a duplicated id must say which copy by
// its hash; without one it is ambiguous_id and nothing is written.
func TestDuplicateIDEdit(t *testing.T) {
	dir := dupMod(t, dupB)
	hs := copyHashes(t, dir, "fn:app.F")
	m, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rev := m.Revision()

	// A revision guard or --force does not choose a copy.
	for _, o := range []EditOpts{{Revision: rev}, {Force: true}} {
		var b bytes.Buffer
		op := EditOp{Op: "replace", ID: "fn:app.F", Text: "func F() i64 {\n  return 9\n}"}
		if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, o, &b); code != ExitFail {
			t.Fatalf("%+v: %d %s", o, code, b.String())
		}
		r := last(t, b.String())
		cs, _ := r["copies"].([]any)
		if r["error"] != "ambiguous_id" || r["id"] != "fn:app.F" || len(cs) != 2 {
			t.Fatalf("%+v: %v", o, r)
		}
		c0, c1 := cs[0].(map[string]any), cs[1].(map[string]any)
		if c0["file"] != "app/a.ov" || c0["line"] != 5.0 || c0["hash"] != hs["app/a.ov"] || c1["file"] != "app/b.ov" || c1["line"] != 4.0 || c1["hash"] != hs["app/b.ov"] {
			t.Fatalf("copies %v", cs)
		}
	}
	// A hash no copy has is stale.
	var b bytes.Buffer
	op := EditOp{Op: "replace", ID: "F", Expect: "000000000000", Text: "func F() i64 {\n  return 9\n}"}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{}, &b); code != ExitStale || last(t, b.String())["error"] != "stale" {
		t.Fatalf("wrong hash: %d %s", code, b.String())
	}
	if readFile(t, dir, "app/a.ov") != dupA || readFile(t, dir, "app/b.ov") != dupB {
		t.Fatal("a refused edit wrote")
	}

	// b.ov's hash picks b.ov's copy, by id or by name.
	b.Reset()
	op = EditOp{Op: "replace", ID: "F", Expect: hs["app/b.ov"], Text: "func F() i64 {\n  return 3\n}"}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{}, &b); code != ExitOK {
		t.Fatalf("replace b's copy: %d %s", code, b.String())
	}
	r := last(t, b.String())
	if fmt.Sprint(r["files"]) != "[app/b.ov]" {
		t.Fatalf("receipt %v", r)
	}
	if got := readFile(t, dir, "app/b.ov"); got != "package app\n\n// F again.\nfunc F() i64 {\n  return 3\n}\n" {
		t.Fatalf("b.ov:\n%s", got)
	}
	if readFile(t, dir, "app/a.ov") != dupA {
		t.Fatal("a.ov changed")
	}
	// The receipt's decl hash is the new b copy's.
	ds := r["ops"].([]any)[0].(map[string]any)["decls"].([]any)
	if hs2 := copyHashes(t, dir, "fn:app.F"); len(ds) != 1 || ds[0].(map[string]any)["hash"] != hs2["app/b.ov"] || hs2["app/a.ov"] != hs["app/a.ov"] {
		t.Fatalf("decls %v, outline %v", ds, hs2)
	}

	// A statement of a copy is picked by its decl's hash too.
	hs = copyHashes(t, dir, "fn:app.F")
	b.Reset()
	op = EditOp{Op: "replace", ID: "st:app.F:1", Expect: hs["app/a.ov"], Text: "return 4"}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{}, &b); code != ExitOK {
		t.Fatalf("replace a's statement: %d %s", code, b.String())
	}
	if got := readFile(t, dir, "app/a.ov"); got != strings.Replace(dupA, "return 1", "return 4", 1) {
		t.Fatalf("a.ov:\n%s", got)
	}

	// Delete one copy and the module is clean.
	hs = copyHashes(t, dir, "fn:app.F")
	b.Reset()
	op = EditOp{Op: "delete", ID: "fn:app.F", Expect: hs["app/b.ov"]}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{}, &b); code != ExitOK || last(t, b.String())["check_ok"] != true {
		t.Fatalf("delete b's copy: %d %s", code, b.String())
	}
	if got := readFile(t, dir, "app/b.ov"); got != "package app\n\n" {
		t.Fatalf("b.ov:\n%q", got)
	}
}

// TestDuplicateIDIdentical: copies with the same text have the same hash;
// the op takes the first, so deleting one of two identical copies works.
func TestDuplicateIDIdentical(t *testing.T) {
	dir := dupMod(t, "package app\n\nfunc F() i64 {\n  return 1\n}\n")
	hs := copyHashes(t, dir, "fn:app.F")
	if hs["app/a.ov"] != hs["app/b.ov"] {
		t.Fatalf("hashes %v", hs)
	}
	var b bytes.Buffer
	op := EditOp{Op: "delete", ID: "fn:app.F", Expect: hs["app/a.ov"]}
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{}, &b); code != ExitOK || last(t, b.String())["check_ok"] != true {
		t.Fatalf("delete: %d %s", code, b.String())
	}
	if got := readFile(t, dir, "app/a.ov"); got != "package app\n\nimport ovid/io\n\nfunc main(io *ovid/io.Cap) i64 {\n  return F()\n}\n" {
		t.Fatalf("a.ov:\n%s", got)
	}
}

// TestDuplicateIDRefsRenameMove: commands that need one node refuse a
// duplicated id rather than choose a copy.
func TestDuplicateIDRefsRenameMove(t *testing.T) {
	dir := dupMod(t, dupB)
	for name, run := range map[string]func(*bytes.Buffer) int{
		"refs":   func(b *bytes.Buffer) int { return Refs(dir, "F", Page{}, b) },
		"rename": func(b *bytes.Buffer) int { return Rename(dir, "fn:app.F", "G", false, b) },
		"move":   func(b *bytes.Buffer) int { return Move(dir, "F", "app/util", "", false, b) },
	} {
		var b bytes.Buffer
		if code := run(&b); code != ExitFail {
			t.Fatalf("%s: %d %s", name, code, b.String())
		}
		if r := last(t, b.String()); r["error"] != "ambiguous_id" || len(r["copies"].([]any)) != 2 {
			t.Fatalf("%s: %v", name, r)
		}
	}
	if readFile(t, dir, "app/a.ov") != dupA || readFile(t, dir, "app/b.ov") != dupB {
		t.Fatal("a refused command wrote")
	}
}

// TestDuplicateIDQualifiedName: Func.param and Type.field reach every copy
// of a duplicated param or field, as the full id does.
func TestDuplicateIDQualifiedName(t *testing.T) {
	t.Setenv(module.PathsEnv, "module")
	a := "package app\n\nimport ovid/io\n\ntype P struct {\n  v i64\n}\n\nfunc F(x i64) i64 {\n  return x\n}\n\nfunc main(io *ovid/io.Cap) i64 {\n  return F(1)\n}\n"
	b := "package app\n\ntype P struct {\n  v i64\n}\n\nfunc F(x i64) i64 {\n  return x + 1\n}\n"
	dir := mkmod(t, map[string]string{"ovid.mod": "module app\nentry app\n", "app/a.ov": a, "app/b.ov": b})
	m, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for q, id := range map[string]string{"F.x": "pa:app.F.x", "app.F.x": "pa:app.F.x", "P.v": "fld:app.P.v"} {
		ls, err := m.Lookup(q)
		if err != nil || len(ls) != 2 || ls[0].Span.File == ls[1].Span.File {
			t.Fatalf("lookup %s: %v %v", q, ls, err)
		}
		full, _ := m.Lookup(id)
		if len(full) != 2 || full[0] != ls[0] || full[1] != ls[1] {
			t.Fatalf("lookup %s: %v, the full id %v", q, ls, full)
		}
	}
	var out bytes.Buffer
	if code := Show(dir, []string{"F.x"}, false, true, false, &out); code != ExitOK || last(t, out.String())["count"] != 2.0 {
		t.Fatalf("show F.x %d %s", code, out.String())
	}
}
