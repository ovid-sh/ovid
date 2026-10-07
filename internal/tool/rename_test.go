package tool

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sources is every .ov file of a module, joined in path order.
func sources(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(p, ".ov") {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(src)
	}
	return b.String()
}

func buildTo(t *testing.T, dir, name string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin", name)
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build %d:\n%s", code, b.String())
	}
	return bin
}

// renameKeeps renames q to `to` and checks that the rewritten source has
// every string in want and none in gone, and that the program still exits
// with exit, as it did before the rename.
func renameKeeps(t *testing.T, dir, q, to string, exit int, want, gone []string) {
	t.Helper()
	bins := renameText(t, dir, q, to, want, gone)
	sameExit(t, exit, bins...)
}

// renameText is renameKeeps without running anything: it returns the
// binaries built before and after the rename.
func renameText(t *testing.T, dir, q, to string, want, gone []string) []string {
	t.Helper()
	before := buildTo(t, dir, "before")
	var b bytes.Buffer
	if code := Rename(dir, q, to, false, &b); code != 0 {
		t.Fatalf("rename %s %s: %s", q, to, b.String())
	}
	if r := last(t, b.String()); r["ok"] != true || r["check_ok"] != true {
		t.Fatalf("rename %s %s: %v", q, to, r)
	}
	src := sources(t, dir)
	for _, s := range want {
		if !strings.Contains(src, s) {
			t.Fatalf("rename %s %s: source lacks %q:\n%s", q, to, s, src)
		}
	}
	for _, s := range gone {
		if strings.Contains(src, s) {
			t.Fatalf("rename %s %s: source still has %q:\n%s", q, to, s, src)
		}
	}
	return []string{before, buildTo(t, dir, "after-"+to)}
}

// sameExit runs each binary and checks it exits with exit.
func sameExit(t *testing.T, exit int, bins ...string) {
	t.Helper()
	needExec(t)
	for _, bin := range bins {
		if _, code := run(t, bin); code != exit {
			t.Fatalf("%s: exit %d, want %d", filepath.Base(bin), code, exit)
		}
	}
}

// treeSrc is issue #21: a field spelled like a type, beside a second field
// with the new name. The comment and the string spell Node too.
const treeSrc = `package demo

import ovid/io

// Node is a tree node; strlen("Node") below is a string, not a use.
type Node struct {
  v i64
}

type Tree struct {
  Node *Node
  Item *Node
}

func Get(t *Tree) i64 {
  var n *Node = t.Node
  return n.v
}

func main(io *ovid/io.Cap) i64 {
  var t *Tree = ovid/io.Alloc(io, sizeof(Tree)) as *Tree
  t.Node = ovid/io.Alloc(io, sizeof(Node)) as *Node
  t.Item = ovid/io.Alloc(io, sizeof(Node)) as *Node
  t.Node.v = 7
  t.Item.v = 9
  return Get(t) + strlen("Node") - 4
}
`

// TestRenameTypeNotField: renaming a type rewrites its type positions only,
// never a field access or field name spelled the same.
func TestRenameTypeNotField(t *testing.T) {
	dir := mkmod(t, demo(treeSrc))
	bins := renameText(t, dir, "ty:demo.Node", "Item",
		[]string{"type Item struct", "  Node *Item\n  Item *Item\n", "var n *Item = t.Node", "t.Node = ovid/io.Alloc(io, sizeof(Item)) as *Item",
			"t.Node.v = 7", "t.Item.v = 9", `// Item is a tree node; strlen("Node")`, `strlen("Node") - 4`},
		[]string{"t.Item\n  return n.v"})
	// And back: the module is what it was.
	var b bytes.Buffer
	if code := Rename(dir, "ty:demo.Item", "Node", false, &b); code != 0 {
		t.Fatalf("rename back: %s", b.String())
	}
	if src := sources(t, dir); src != treeSrc {
		t.Fatalf("rename back:\n%s", src)
	}
	sameExit(t, 7, bins...)
}

// TestRefsTypeNotField: refs of a type lists type positions, not field
// accesses spelled the same; refs of the field lists only those.
func TestRefsTypeNotField(t *testing.T) {
	dir := mkmod(t, demo(treeSrc))
	var b bytes.Buffer
	if code := Refs(dir, "ty:demo.Node", true, Page{}, &b); code != 0 {
		t.Fatal(b.String())
	}
	rs := lines(t, b.String())
	// Two field types, the var in Get, and two sizeofs and two casts in main.
	if s := rs[len(rs)-1]; s["count"] != float64(7) {
		t.Fatalf("refs %v", rs)
	}
	for _, r := range rs[:len(rs)-1] {
		if r["kind"] != "type" {
			t.Fatalf("a use of the type that is not a type position: %v", r)
		}
	}
	b.Reset()
	if code := Refs(dir, "fld:demo.Tree.Node", true, Page{}, &b); code != 0 {
		t.Fatal(b.String())
	}
	rs = lines(t, b.String())
	// t.Node in Get, the store t.Node = ..., and t.Node in t.Node.v = 7.
	var kinds []string
	for _, r := range rs[:len(rs)-1] {
		kinds = append(kinds, r["kind"].(string))
	}
	if got := strings.Join(kinds, " "); got != "field setfield field" {
		t.Fatalf("refs of the field: %s\n%v", got, rs)
	}
}

// TestRenameFieldNotType: renaming a field rewrites its accesses only, not
// the type spelled the same or another struct's field of the same name.
func TestRenameFieldNotType(t *testing.T) {
	dir := mkmod(t, demo(`package demo

import ovid/io

type Node struct {
  v i64
}

type Tree struct {
  Node *Node
}

type Leaf struct {
  Node i64
}

func main(io *ovid/io.Cap) i64 {
  var t *Tree = ovid/io.Alloc(io, sizeof(Tree)) as *Tree
  var l *Leaf = ovid/io.Alloc(io, sizeof(Leaf)) as *Leaf
  t.Node = ovid/io.Alloc(io, sizeof(Node)) as *Node
  t.Node.v = 3
  l.Node = 4
  return t.Node.v * 10 + l.Node
}
`))
	renameKeeps(t, dir, "fld:demo.Tree.Node", "Root", 34,
		[]string{"type Node struct", "  Root *Node\n", "  Node i64\n", "t.Root = ovid/io.Alloc(io, sizeof(Node)) as *Node",
			"t.Root.v = 3", "l.Node = 4", "return t.Root.v * 10 + l.Node"},
		[]string{"t.Node", "l.Root"})
}

// shadowSrc has a const and a func whose names are also a field and a local.
const shadowSrc = `package demo

import ovid/io

const K i64 = 5

type Box struct {
  K i64
  Twice i64
}

func Twice(x i64) i64 {
  return x * 2
}

func Inner(b *Box) i64 {
  var K i64 = b.K
  var Twice i64 = 1
  return K + Twice
}

func main(io *ovid/io.Cap) i64 {
  var b *Box = ovid/io.Alloc(io, sizeof(Box)) as *Box
  b.K = 10
  b.Twice = Twice(K)
  return Inner(b) + b.Twice + K
}
`

// TestRenameNotFieldsOrLocals: renaming a const, func, local, or field
// touches only what the checker resolved to it.
func TestRenameNotFieldsOrLocals(t *testing.T) {
	for _, c := range []struct {
		q, to      string
		want, gone []string
	}{
		{"cn:demo.K", "Base",
			[]string{"const Base i64 = 5", "  K i64\n", "var K i64 = b.K", "return K + Twice", "b.K = 10", "Twice(Base)", "b.Twice + Base"},
			[]string{"b.Base", "var Base"}},
		{"fn:demo.Twice", "Double",
			[]string{"func Double(x i64)", "  Twice i64\n", "var Twice i64 = 1", "return K + Twice", "b.Twice = Double(K)", "b.Twice + K"},
			[]string{"b.Double", "var Double"}},
		{"st:demo.Inner:1", "k",
			[]string{"const K i64 = 5", "  K i64\n", "var k i64 = b.K", "return k + Twice", "b.K = 10", "Twice(K)", "b.Twice + K"},
			[]string{"b.k"}},
		{"fld:demo.Box.K", "Key",
			[]string{"const K i64 = 5", "  Key i64\n", "var K i64 = b.Key", "return K + Twice", "b.Key = 10", "Twice(K)", "b.Twice + K"},
			[]string{"Twice(Key)", "var Key"}},
	} {
		t.Run(c.q, func(t *testing.T) {
			renameKeeps(t, mkmod(t, demo(shadowSrc)), c.q, c.to, 26, c.want, c.gone)
		})
	}
}

// TestRenameSiblingLocals: two sibling blocks declare x; renaming one leaves
// the other.
func TestRenameSiblingLocals(t *testing.T) {
	dir := mkmod(t, demo(`package demo

import ovid/io

func main(io *ovid/io.Cap) i64 {
  var r i64 = 0
  if r == 0 {
    var x i64 = 3
    r = r + x
  } else {
    var x i64 = 100
    r = r + x
  }
  return r * 4
}
`))
	var b bytes.Buffer
	Refs(dir, "st:demo.main:3", true, Page{}, &b)
	if s := last(t, b.String()); s["count"] != float64(1) {
		t.Fatalf("refs of the first x: %s", b.String())
	}
	renameKeeps(t, dir, "st:demo.main:3", "y", 12,
		[]string{"var y i64 = 3\n    r = r + y\n", "var x i64 = 100\n    r = r + x\n"}, nil)
}

// TestRenameQualified: uses spelled path.Name in another package are
// renamed; that package's fields and locals of the same name are not.
func TestRenameQualified(t *testing.T) {
	files := map[string]string{
		"demo/main.ov": `package demo

import ovid/io
import demo/geo

type Shape struct {
  Point *demo/geo.Point
  Make i64
  Scale i64
}

func main(io *ovid/io.Cap) i64 {
  var s *Shape = ovid/io.Alloc(io, sizeof(Shape)) as *Shape
  s.Point = demo/geo.Make(io, 4)
  s.Make = 2
  s.Scale = 1
  var Scale i64 = s.Point.x * demo/geo.Scale
  return Scale + s.Make + s.Scale
}
`,
		"demo/geo/geo.ov": `package demo/geo

import ovid/io

const Scale i64 = 3

type Point struct {
  x i64
}

func Make(io *ovid/io.Cap, x i64) *Point {
  var p *Point = ovid/io.Alloc(io, sizeof(Point)) as *Point
  p.x = x
  return p
}
`,
	}
	for _, c := range []struct {
		q, to      string
		want, gone []string
	}{
		{"ty:demo/geo.Point", "Pt",
			[]string{"  Point *demo/geo.Pt\n", "s.Point = demo/geo.Make(io, 4)", "s.Point.x", "type Pt struct", ") *Pt {", "var p *Pt = ovid/io.Alloc(io, sizeof(Pt)) as *Pt"},
			[]string{"s.Pt"}},
		{"fn:demo/geo.Make", "New",
			[]string{"  Make i64\n", "s.Point = demo/geo.New(io, 4)", "s.Make = 2", "s.Make + s.Scale", "func New(io"},
			[]string{"s.New"}},
		{"cn:demo/geo.Scale", "Factor",
			[]string{"  Scale i64\n", "s.Scale = 1", "var Scale i64 = s.Point.x * demo/geo.Factor", "return Scale + s.Make + s.Scale", "const Factor i64 = 3"},
			[]string{"s.Factor", "var Factor"}},
	} {
		t.Run(c.q, func(t *testing.T) {
			fs := map[string]string{}
			for k, v := range files {
				fs[k] = v
			}
			renameKeeps(t, mkmod(t, fs), c.q, c.to, 15, c.want, c.gone)
		})
	}
}

// TestRenameDocComment: a doc comment that opens with the decl's name is
// renamed with it; one that does not, and other mentions, are left alone.
func TestRenameDocComment(t *testing.T) {
	dir := mkmod(t, demo(`package demo

import ovid/io

// Twice doubles x. Twice is used by main; see also Thrice.
func Twice(x i64) i64 {
  return x + x
}

// A helper: calls Twice twice.
func Four(x i64) i64 {
  return Twice(Twice(x))
}

func main(io *ovid/io.Cap) i64 {
  return Four(1)
}
`))
	var b bytes.Buffer
	if code := Rename(dir, "Twice", "Double", false, &b); code != 0 {
		t.Fatal(b.String())
	}
	r := last(t, b.String())
	if r["doc"] != true || r["edits"] != 4.0 {
		t.Fatalf("receipt %v", r)
	}
	src := sources(t, dir)
	for _, want := range []string{"// Double doubles x. Twice is used by main; see also Thrice.\nfunc Double(x i64) i64", "// A helper: calls Twice twice.\nfunc Four", "return Double(Double(x))"} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing %q in:\n%s", want, src)
		}
	}
	b.Reset()
	if code := Rename(dir, "Four", "Quad", false, &b); code != 0 {
		t.Fatal(b.String())
	}
	if r := last(t, b.String()); r["doc"] != false || r["edits"] != 2.0 {
		t.Fatalf("receipt %v", r)
	}
	if src := sources(t, dir); !strings.Contains(src, "// A helper: calls Twice twice.\nfunc Quad") {
		t.Fatalf("doc comment changed:\n%s", src)
	}
	// A tab after the slashes is leading space too.
	dir = mkmod(t, demo("package demo\n\nimport ovid/io\n\n//\tOne is one.\nfunc One() i64 {\n  return 1\n}\n\nfunc main(io *ovid/io.Cap) i64 {\n  return One()\n}\n"))
	b.Reset()
	if code := Rename(dir, "One", "Uno", false, &b); code != 0 {
		t.Fatal(b.String())
	}
	if r := last(t, b.String()); r["doc"] != true {
		t.Fatalf("receipt %v", r)
	}
	if src := sources(t, dir); !strings.Contains(src, "//\tUno is one.\nfunc Uno") {
		t.Fatalf("tab doc comment not renamed:\n%s", src)
	}
}
