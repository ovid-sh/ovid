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
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc Two(x i64) (i64, error) {\n  return x, 0\n}\nfunc main(io *ovid/io.Cap) i64 {\n  var x i64 = 1\n  var v i64, e error = Two(x)\n  return v + (e as i64)\n}\n"))
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
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc Two(x i64) (i64, error) {\n  return x, 0\n}\nfunc main(io *ovid/io.Cap) i64 {\n  var v i64, e error = Two(1)\n  v, e = Two(v + (e as i64))\n  if e != 0 {\n    return e as i64\n  }\n  return v\n}\n"))
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
	want := "  var v i64, err error = Two(1)\n  v, err = Two(v + (err as i64))\n  if err != 0 {\n    return err as i64\n  }\n  return v\n"
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

// TestTwoNameVarDiscard: _ in a var2 discards a result and is no local:
// the statement declares one, which refs and rename reach without --name,
// and --name _ is not_found.
func TestTwoNameVarDiscard(t *testing.T) {
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc Two(x i64) (i64, error) {\n  return x, 0\n}\nfunc main(io *ovid/io.Cap) i64 {\n  var _, e error = Two(1)\n  var v i64, _ = Two(e as i64)\n  return v + (e as i64)\n}\n"))
	for _, c := range []struct {
		id, local string
		uses      int
	}{{"st:demo.main:1", "e", 2}, {"st:demo.main:2", "v", 1}} {
		var b bytes.Buffer
		if code := Refs(dir, c.id, "", true, Page{}, &b); code != 0 || last(t, b.String())["total"] != float64(c.uses) {
			t.Fatalf("refs %s: %d %s", c.id, code, b.String())
		}
		b.Reset()
		if code := Refs(dir, c.id, c.local, true, Page{}, &b); code != 0 {
			t.Fatalf("refs %s --name %s: %d %s", c.id, c.local, code, b.String())
		}
		for _, run := range []func() int{
			func() int { return Refs(dir, c.id, "_", true, Page{}, &b) },
			func() int { return Rename(dir, c.id, "z", "_", true, &b) },
		} {
			b.Reset()
			if code := run(); code == 0 || last(t, b.String())["error"] != "not_found" {
				t.Fatalf("--name _ on %s: %d %s", c.id, code, b.String())
			}
		}
	}
	var b bytes.Buffer
	if code := Rename(dir, "st:demo.main:1", "err", "", false, &b); code != 0 || last(t, b.String())["edits"] != float64(3) {
		t.Fatalf("rename the one local: %d %s", code, b.String())
	}
	src, _ := os.ReadFile(filepath.Join(dir, "demo/main.ov"))
	if !strings.Contains(string(src), "  var _, err error = Two(1)\n  var v i64, _ = Two(err as i64)\n  return v + (err as i64)\n") {
		t.Fatalf("after rename:\n%s", src)
	}
	b.Reset()
	if code := Check(dir, false, &b); code != 0 {
		t.Fatalf("after rename: %s", b.String())
	}
}

// TestMoveTwoNameVarType: a func whose only use of a package is the type of
// a var that receives two results brings that import along.
func TestMoveTwoNameVarType(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": "package demo\nimport ovid/io\nimport util\nfunc Make() (*util.P, error) {\n  return 0 as *util.P, 0\n}\nfunc Use() i64 {\n  var p *util.P, e error = Make()\n  return e as i64\n}\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n",
		"util/util.ov": "package util\ntype P struct {\n  x i64\n}\n",
	})
	var b bytes.Buffer
	if code := Move(dir, "Use", "other", "", false, &b); code != 0 {
		t.Fatalf("move %d: %s", code, b.String())
	}
	src, _ := os.ReadFile(filepath.Join(dir, "other/other.ov"))
	if !strings.Contains(string(src), "import util\n") || !strings.Contains(string(src), "var p *util.P, e error = demo.Make()") {
		t.Fatalf("other.ov:\n%s", src)
	}
	b.Reset()
	if code := Check(dir, false, &b); code != 0 {
		t.Fatalf("after move: %s", b.String())
	}
}
