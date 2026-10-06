package tool

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// lastLine decodes the last line of text output, the JSON "ok" line.
func lastLine(t *testing.T, out string) map[string]any {
	t.Helper()
	ls := strings.Split(strings.TrimSpace(out), "\n")
	var r map[string]any
	if err := json.Unmarshal([]byte(ls[len(ls)-1]), &r); err != nil {
		t.Fatalf("last line not JSON: %q", ls[len(ls)-1])
	}
	return r
}

// TestReadCommandsAsText: outline, refs, and grep print text an agent can
// read in place of grep and cat, with the records behind --json.
func TestReadCommandsAsText(t *testing.T) {
	t.Setenv("OVID_PATHS", "module")
	dir := mkmod(t, demo(`package demo
import ovid/io
// Twice doubles.
func Twice(x i64) i64 {
  return x + x
}
func Thrice(x i64) i64 {
  return Twice(x) + x
}
func main(io *ovid/io.Cap) i64 {
  return Twice(Thrice(1)) + Twice(2)
}
`))
	var b bytes.Buffer
	if code := Outline(dir, "demo", false, false, false, false, Page{}, &b); code != 0 {
		t.Fatal(b.String())
	}
	out := b.String()
	want := "demo/main.ov\n    4  func Twice(x i64) i64\n    7  func Thrice(x i64) i64\n   10  func main(io *ovid/io.Cap) i64\n"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("outline:\n%s", out)
	}
	if l := lastLine(t, out); l["ok"] != true || l["total"] != 3.0 {
		t.Fatalf("outline last line %v", l)
	}
	b.Reset()
	Outline(dir, "demo", false, true, true, false, Page{}, &b)
	if s := b.String(); !strings.Contains(s, "    4  func Twice(x i64) i64  fn:demo.Twice hash=") || !strings.Contains(s, "  used by demo 3\n") || !strings.Contains(s, "func main(io *ovid/io.Cap) i64  fn:demo.main hash=") || !strings.Contains(s, "  unused\n") {
		t.Fatalf("outline --ids --uses:\n%s", s)
	}
	b.Reset()
	Outline(dir, "", false, false, false, false, Page{}, &b)
	if s := b.String(); !strings.HasPrefix(s, "demo  funcs=3 types=0 consts=0  imports ovid/io  demo/main.ov\n") {
		t.Fatalf("outline packages:\n%s", s)
	}

	b.Reset()
	if code := Refs(dir, "Twice", false, Page{}, &b); code != 0 {
		t.Fatal(b.String())
	}
	want = "demo/main.ov  fn:demo.Thrice\n    8: return Twice(x) + x\ndemo/main.ov  fn:demo.main\n   11: return Twice(Thrice(1)) + Twice(2)\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Fatalf("refs:\n%s", b.String())
	}
	if l := lastLine(t, b.String()); l["total"] != 3.0 {
		t.Fatalf("refs last line %v", l)
	}

	b.Reset()
	if code := Grep(dir, `Twice\(`, "", false, false, 0, 0, &b); code != 0 {
		t.Fatal(b.String())
	}
	// Line 11 holds two matches and is printed once; total counts both.
	want = "demo/main.ov  fn:demo.Twice\n    4: func Twice(x i64) i64 {\ndemo/main.ov  fn:demo.Thrice\n    8:   return Twice(x) + x\ndemo/main.ov  fn:demo.main\n   11:   return Twice(Thrice(1)) + Twice(2)\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Fatalf("grep:\n%s", b.String())
	}
	if l := lastLine(t, b.String()); l["total"] != 4.0 || l["count"] != 4.0 {
		t.Fatalf("grep last line %v", l)
	}
	b.Reset()
	Grep(dir, `Twice\(`, "", false, true, 0, 0, &b)
	if rs := lines(t, b.String()); len(rs) != 5 || rs[3]["line"] != 11.0 || rs[3]["decl"] != "fn:demo.main" {
		t.Fatalf("grep --json: %v", rs)
	}

	// show: the source under its header, statement ids only with --ids.
	b.Reset()
	Show(dir, []string{"Twice"}, false, false, false, &b)
	if s := b.String(); !strings.HasPrefix(s, "// func fn:demo.Twice demo/main.ov:3-6 hash=") || strings.Contains(s, "// @") {
		t.Fatalf("show:\n%s", s)
	}
	b.Reset()
	Show(dir, []string{"Twice"}, true, false, false, &b)
	if s := b.String(); !strings.Contains(s, "return x + x  // @st:demo.Twice:1") {
		t.Fatalf("show --ids:\n%s", s)
	}
}
