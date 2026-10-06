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
		if code := Rename(dir, "st:demo.main:1", to, true, &b); code == 0 || last(t, b.String())["error"] != "conflict" {
			t.Fatalf("rename x to %s: %d %s", to, code, b.String())
		}
	}
	var b bytes.Buffer
	if code := Rename(dir, "st:demo.main:1", "y", true, &b); code != 0 {
		t.Fatalf("rename x to y: %s", b.String())
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
