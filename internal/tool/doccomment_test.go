package tool

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docSrc has two documented funcs, one with a comment that a blank line
// separates from it (so not its doc comment), and main.
const docSrc = `package demo

import ovid/io

// G returns one.
func G() i64 {
  return 1
}

// H returns zero.
// It is never more.
func H() i64 {
  return 0
}

// A loose note.

func K() i64 {
  return 2
}

func main(io *ovid/io.Cap) i64 {
  return G() + H() + K()
}
`

// docEdit runs one forced op and fails the test unless it succeeds.
func docEdit(t *testing.T, dir string, op EditOp) map[string]any {
	t.Helper()
	var b bytes.Buffer
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Force: true, Show: true}, &b); code != 0 {
		t.Fatalf("%s %s: %d %s", op.Op, op.ID+op.Before+op.After+op.Into, code, b.String())
	}
	return last(t, b.String())
}

func wantFile(t *testing.T, dir, want string) {
	t.Helper()
	if got := mainOv(t, dir); got != want {
		t.Fatalf("demo/main.ov:\n%s\nwant:\n%s", got, want)
	}
}

// TestDocCommentReplace: replace swaps the doc comment with the decl's
// text: the new text's comment takes the old one's place, and text with
// none removes it (#24).
func TestDocCommentReplace(t *testing.T) {
	dir := mkmod(t, demo(docSrc))
	r := docEdit(t, dir, EditOp{Op: "replace", ID: "G", Text: "// G returns two.\nfunc G() i64 {\n  return 2\n}"})
	wantFile(t, dir, strings.Replace(docSrc, "// G returns one.\nfunc G() i64 {\n  return 1\n}", "// G returns two.\nfunc G() i64 {\n  return 2\n}", 1))
	// The receipt's text is the decl as show prints it, comment included.
	d := r["ops"].([]any)[0].(map[string]any)["decls"].([]any)[0].(map[string]any)
	if d["id"] != "fn:demo.G" || d["text"] != "// G returns two.\nfunc G() i64 {\n  return 2\n}" || d["hash"] != hashOf(t, dir, "fn:demo.G") {
		t.Fatalf("receipt: %v", r)
	}

	docEdit(t, dir, EditOp{Op: "replace", ID: "H", Text: "func H() i64 {\n  return 3\n}"})
	wantFile(t, dir, strings.Replace(
		strings.Replace(docSrc, "// G returns one.\nfunc G() i64 {\n  return 1\n}", "// G returns two.\nfunc G() i64 {\n  return 2\n}", 1),
		"// H returns zero.\n// It is never more.\nfunc H() i64 {\n  return 0\n}", "func H() i64 {\n  return 3\n}", 1))

	// A comment a blank line above is not K's, so replacing K keeps it.
	dir = mkmod(t, demo(docSrc))
	docEdit(t, dir, EditOp{Op: "replace", ID: "K", Text: "func K() i64 {\n  return 4\n}"})
	wantFile(t, dir, strings.Replace(docSrc, "func K() i64 {\n  return 2\n}", "func K() i64 {\n  return 4\n}", 1))
}

// TestDocCommentDelete: delete takes the doc comment with the decl, so it
// cannot become the next decl's.
func TestDocCommentDelete(t *testing.T) {
	src := strings.Replace(docSrc, "G() + H() + K()", "G() + K()", 1)
	dir := mkmod(t, demo(src))
	docEdit(t, dir, EditOp{Op: "delete", ID: "H"})
	wantFile(t, dir, strings.Replace(src, "// H returns zero.\n// It is never more.\nfunc H() i64 {\n  return 0\n}\n\n", "", 1))

	// K's loose note is not its doc comment, so it stays.
	src = strings.Replace(docSrc, "G() + H() + K()", "G() + H()", 1)
	dir = mkmod(t, demo(src))
	docEdit(t, dir, EditOp{Op: "delete", ID: "K"})
	wantFile(t, dir, strings.Replace(src, "func K() i64 {\n  return 2\n}\n\n", "", 1))
}

// TestDocCommentInsert: a decl inserted before another lands above that
// decl's doc comment; one inserted after or appended keeps its own.
func TestDocCommentInsert(t *testing.T) {
	k2 := "// K2 is new.\nfunc K2() i64 {\n  return 5\n}"
	dir := mkmod(t, demo(docSrc))
	docEdit(t, dir, EditOp{Op: "insert", Before: "H", Text: k2})
	wantFile(t, dir, strings.Replace(docSrc, "// H returns zero.\n", k2+"\n\n// H returns zero.\n", 1))

	dir = mkmod(t, demo(docSrc))
	docEdit(t, dir, EditOp{Op: "insert", After: "G", Text: k2})
	wantFile(t, dir, strings.Replace(docSrc, "  return 1\n}\n", "  return 1\n}\n\n"+k2+"\n", 1))

	dir = mkmod(t, demo(docSrc))
	var b bytes.Buffer
	if code := runEdit(dir, &EditReq{Ops: []EditOp{{Op: "append", Into: "demo", Text: k2}}}, EditOpts{}, &b); code != 0 {
		t.Fatalf("append: %d %s", code, b.String())
	}
	wantFile(t, dir, docSrc+"\n"+k2+"\n")
	if got := showJSON(t, dir, "K2"); got["doc"] != "K2 is new." || got["text"] != k2 {
		t.Fatalf("show K2: %v", got)
	}
}

func showJSON(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	var b bytes.Buffer
	if code := Show(dir, []string{id}, true, true, false, &b); code != 0 {
		t.Fatalf("show %s: %s", id, b.String())
	}
	return lines(t, b.String())[0]
}

// TestDocCommentShow: show prints the doc comment, and its header and
// JSON say where it starts.
func TestDocCommentShow(t *testing.T) {
	t.Setenv("OVID_PATHS", "module")
	dir := mkmod(t, demo(docSrc))
	h := hashOf(t, dir, "fn:demo.H")
	var b bytes.Buffer
	Show(dir, []string{"H"}, true, false, false, &b)
	want := "// func fn:demo.H demo/main.ov:10-14 hash=" + h + "\n" +
		"// H returns zero.\n// It is never more.\nfunc H() i64 {  // @fn:demo.H\n  return 0  // @st:demo.H:1\n}\n"
	if b.String() != want {
		t.Fatalf("show H:\n%s\nwant:\n%s", b.String(), want)
	}
	r := showJSON(t, dir, "H")
	if r["text"] != "// H returns zero.\n// It is never more.\nfunc H() i64 {\n  return 0\n}" || r["doc"] != "H returns zero. It is never more." ||
		r["doc_line"] != 10.0 || r["line"] != 12.0 || r["end_line"] != 14.0 {
		t.Fatalf("show --json H: %v", r)
	}
	// No doc comment: no doc keys, and the loose note is not printed.
	r = showJSON(t, dir, "K")
	if _, ok := r["doc"]; ok || r["doc_line"] != nil || r["text"] != "func K() i64 {\n  return 2\n}" {
		t.Fatalf("show --json K: %v", r)
	}
	b.Reset()
	Show(dir, []string{"K"}, false, false, false, &b)
	if strings.Contains(b.String(), "loose") || !strings.Contains(b.String(), ":18-20 hash=") {
		t.Fatalf("show K:\n%s", b.String())
	}
	// outline's doc is the same comment.
	b.Reset()
	Outline(dir, "demo", false, false, &b)
	for _, d := range lines(t, b.String()) {
		if d["id"] == "fn:demo.H" && (d["doc"] != "H returns zero. It is never more." || d["line"] != 12.0) {
			t.Fatalf("outline H: %v", d)
		}
		if d["id"] == "fn:demo.K" && d["doc"] != nil {
			t.Fatalf("outline K: %v", d)
		}
	}
	// grep places a match in a doc comment in its decl.
	b.Reset()
	Grep(dir, "never more", "", false, 0, 0, &b)
	if g := lines(t, b.String())[0]; g["decl"] != "fn:demo.H" {
		t.Fatalf("grep: %s", b.String())
	}
}

// TestDocCommentHash: a decl's hash covers its doc comment, so an edit
// guarded by a hash read before the comment changed is stale, and so is
// one guarded by a statement's hash in it.
func TestDocCommentHash(t *testing.T) {
	dir := mkmod(t, demo(docSrc))
	decl, st, k := hashOf(t, dir, "fn:demo.G"), hashOf(t, dir, "st:demo.G:1"), hashOf(t, dir, "fn:demo.K")
	p := filepath.Join(dir, "demo/main.ov")
	if err := os.WriteFile(p, []byte(strings.Replace(docSrc, "// G returns one.", "// G returns 1.", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if hashOf(t, dir, "fn:demo.G") == decl || hashOf(t, dir, "st:demo.G:1") == st {
		t.Fatal("a doc comment change left the hashes alone")
	}
	refused(t, dir, EditOp{Op: "replace", ID: "fn:demo.G", Expect: decl, Text: "func G() i64 {\n  return 2\n}"})
	refused(t, dir, EditOp{Op: "replace", ID: "st:demo.G:1", Expect: st, Text: "return 2"})
	// A stale refusal's text is the decl as show prints it.
	_, r := edit1(t, dir, EditOp{Op: "delete", ID: "fn:demo.G", Expect: decl})
	if r["text"] != "// G returns 1.\nfunc G() i64 {\n  return 1\n}" {
		t.Fatalf("stale text: %v", r)
	}
	// Another decl's hash does not move.
	if hashOf(t, dir, "fn:demo.K") != k {
		t.Fatal("G's doc comment moved K's hash")
	}
}

// TestDocCommentMove: move takes the doc comment along and leaves nothing
// behind.
func TestDocCommentMove(t *testing.T) {
	dir := mkmod(t, demo(docSrc))
	var b bytes.Buffer
	if code := Move(dir, "H", "demo/util", "", false, &b); code != 0 {
		t.Fatalf("move: %s", b.String())
	}
	main := mainOv(t, dir)
	if strings.Contains(main, "H returns") || strings.Contains(main, "never more") {
		t.Fatalf("main.ov kept H's comment:\n%s", main)
	}
	util, _ := os.ReadFile(filepath.Join(dir, "demo/util/util.ov"))
	if want := "package demo/util\n\n// H returns zero.\n// It is never more.\nfunc H() i64 {\n  return 0\n}\n"; string(util) != want {
		t.Fatalf("util.ov:\n%s", util)
	}
	if r := showJSON(t, dir, "fn:demo/util.H"); r["doc"] != "H returns zero. It is never more." {
		t.Fatalf("show moved H: %v", r)
	}
}
