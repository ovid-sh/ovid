package tool

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenameSeesTwoNameVar: both names of a var that receives two results
// are locals a rename can collide with.
func TestRenameSeesTwoNameVar(t *testing.T) {
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc Two(x i64) (i64, i64) {\n  return x, 0\n}\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  var v i64, e i64 = Two(x)\n  return v + e\n}\n"))
	for _, to := range []string{"v", "e"} {
		var b bytes.Buffer
		if code := Rename(dir, "st:demo.main:1", to, "", true, &b); code == 0 || last(t, b.String())["error"] != "conflict" {
			t.Fatalf("rename x to %s: %d %s", to, code, b.String())
		}
	}
	var b bytes.Buffer
	if code := Rename(dir, "st:demo.main:1", "y", "", true, &b); code != 0 {
		t.Fatalf("rename x to y: %s", b.String())
	}
	// The two-name var's id names two locals: without --name both commands
	// refuse, and say which names to choose from.
	b.Reset()
	if code := Rename(dir, "st:demo.main:2", "z", "", true, &b); code == 0 || last(t, b.String())["error"] != "unsupported" || !strings.Contains(b.String(), "declares two locals, v and e") {
		t.Fatalf("rename of the two-name var: %d %s", code, b.String())
	}
	b.Reset()
	if code := Refs(dir, "st:demo.main:2", "", true, Page{}, &b); code == 0 || !strings.Contains(b.String(), "declares two locals, v and e") {
		t.Fatalf("refs of the two-name var: %d %s", code, b.String())
	}
}

// TestTwoNameVarByName: --name picks one local of a var that receives two
// results, for refs and rename; uses of the other are left alone.
func TestTwoNameVarByName(t *testing.T) {
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc Two(x i64) (i64, i64) {\n  return x, 0\n}\nfunc main(io *ovid/io.Cap) i64 {\n  var v i64, e i64 = Two(1)\n  v, e = Two(v + e)\n  if e != 0 {\n    return e\n  }\n  return v\n}\n"))
	var b bytes.Buffer
	if code := Refs(dir, "st:demo.main:1", "e", true, Page{}, &b); code != 0 || last(t, b.String())["total"] != float64(4) {
		t.Fatalf("refs --name e: %d %s", code, b.String())
	}
	b.Reset()
	if code := Refs(dir, "st:demo.main:1", "z", true, Page{}, &b); code == 0 || last(t, b.String())["error"] != "not_found" {
		t.Fatalf("refs --name z: %d %s", code, b.String())
	}
	b.Reset()
	if code := Rename(dir, "st:demo.main:1", "err", "e", false, &b); code != 0 || last(t, b.String())["edits"] != float64(5) {
		t.Fatalf("rename --name e: %d %s", code, b.String())
	}
	src, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov"))
	want := "  var v i64, err i64 = Two(1)\n  v, err = Two(v + err)\n  if err != 0 {\n    return err\n  }\n  return v\n"
	if !strings.Contains(string(src), want) {
		t.Fatalf("after rename:\n%s", src)
	}
	b.Reset()
	if code := Rename(dir, "st:demo.main:1", "w", "v", false, &b); code != 0 {
		t.Fatalf("rename --name v: %d %s", code, b.String())
	}
	b.Reset()
	if code := Check(dir, false, &b); code != 0 {
		t.Fatalf("after renames: %s", b.String())
	}
	// A name that is not the target's own is refused for any target.
	b.Reset()
	if code := Rename(dir, "fn:demo.Two", "Pair", "Three", true, &b); code == 0 || last(t, b.String())["error"] != "not_found" {
		t.Fatalf("rename fn --name Three: %d %s", code, b.String())
	}
}

// TestMoveTwoNameVarType: a func whose only use of a package is the type of
// a var that receives two results brings that import along.
func TestMoveTwoNameVarType(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": "package demo\nimport ovid/io\nimport util\nfunc Make() (*util.P, i64) {\n  return 0 as *util.P, 0\n}\nfunc Use() i64 {\n  var p *util.P, e i64 = Make()\n  return e\n}\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n",
		"util/util.ov": "package util\ntype P struct {\n  x i64\n}\n",
	})
	var b bytes.Buffer
	if code := Move(dir, "Use", "other", "", false, &b); code != 0 {
		t.Fatalf("move %d: %s", code, b.String())
	}
	src, _ := os.ReadFile(filepath.Join(dir, "other/other.ov"))
	if !strings.Contains(string(src), "import util\n") || !strings.Contains(string(src), "var p *util.P, e i64 = demo.Make()") {
		t.Fatalf("other.ov:\n%s", src)
	}
	b.Reset()
	if code := Check(dir, false, &b); code != 0 {
		t.Fatalf("after move: %s", b.String())
	}
}
