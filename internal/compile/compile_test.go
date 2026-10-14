package compile

import (
	"math"
	"math/bits"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"ovid/internal/ir"
)

func ioPkg() ir.Package {
	return ir.Package{
		ID:   "pkg:ovid/io",
		Path: "ovid/io",
		Types: []ir.TypeDecl{{
			ID:   "ty:ovid/io.Cap",
			Name: "Cap",
			Fields: []ir.Field{
				{ID: "fld:argc", Name: "argc", Type: "i64"},
				{ID: "fld:argv", Name: "argv", Type: "i64"},
				{ID: "fld:heap", Name: "heap", Type: "i64"},
				{ID: "fld:used", Name: "used", Type: "i64"},
				{ID: "fld:size", Name: "size", Type: "i64"},
				{ID: "fld:maps", Name: "maps", Type: "i64"},
			},
		}},
	}
}

func prog(fns ...ir.Func) *ir.Program {
	return &ir.Program{
		Revision: "",
		Module:   "t",
		Entry:    "demo",
		Packages: []ir.Package{
			ioPkg(),
			{
				ID:      "pkg:demo",
				Path:    "demo",
				Imports: []ir.Import{{ID: "im:demo:ovid/io", Path: "ovid/io"}},
				Funcs:   fns,
			},
		},
	}
}

func mainFn(body ...*ir.Node) ir.Func {
	return ir.Func{
		ID:     "fn:demo.main",
		Name:   "main",
		Params: []ir.Param{{ID: "pa:io", Name: "io", Type: "*ovid/io.Cap"}},
		Result: "i64",
		Body:   body,
	}
}

func nInt(id string, v int64) *ir.Node {
	return &ir.Node{ID: id, Op: "int", ValK: 1, Int: v}
}

func ret(id string, v *ir.Node) *ir.Node {
	return &ir.Node{ID: id, Op: "return", Val: v}
}

func run(t *testing.T, p *ir.Program, args ...string) (string, int) {
	t.Helper()
	bin, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("compiled only: %s/%s cannot execute linux/amd64 binaries", runtime.GOOS, runtime.GOARCH)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "prog")
	if err := os.WriteFile(path, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run: %v\n%s", err, out)
		}
	}
	return string(out), code
}

func TestReturn42(t *testing.T) {
	_, code := run(t, prog(mainFn(ret("s1", nInt("e1", 42)))))
	if code != 42 {
		t.Fatalf("exit %d", code)
	}
}

func TestAdd(t *testing.T) {
	body := ret("s1", &ir.Node{ID: "e1", Op: "add", Left: nInt("e2", 40), Right: nInt("e3", 2)})
	_, code := run(t, prog(mainFn(body)))
	if code != 42 {
		t.Fatalf("exit %d", code)
	}
}

func TestIf(t *testing.T) {
	// if 1 == 1 { return 7 } else { return 8 }
	cond := &ir.Node{ID: "c", Op: "eq", Left: nInt("a", 1), Right: nInt("b", 1)}
	iff := &ir.Node{ID: "s", Op: "if", Cond: cond,
		Then: []*ir.Node{ret("r1", nInt("n1", 7))},
		Else: []*ir.Node{ret("r2", nInt("n2", 8))},
	}
	_, code := run(t, prog(mainFn(iff)))
	if code != 7 {
		t.Fatalf("exit %d", code)
	}
}

func TestLoop(t *testing.T) {
	// var i i64 = 0; var s i64 = 0; while i < 10 { s = s + i; i = i + 1 }; return s
	// sum 0..9 = 45
	body := []*ir.Node{
		{ID: "v1", Op: "var", Name: "i", Type: "i64", Val: nInt("zi", 0)},
		{ID: "v2", Op: "var", Name: "s", Type: "i64", Val: nInt("zs", 0)},
		{ID: "w", Op: "while", Cond: &ir.Node{ID: "lt", Op: "lt", Left: &ir.Node{ID: "ni", Op: "name", Name: "i"}, Right: nInt("ten", 10)},
			Body: []*ir.Node{
				{ID: "as", Op: "assign", Name: "s", Val: &ir.Node{ID: "add", Op: "add", Left: &ir.Node{ID: "ns", Op: "name", Name: "s"}, Right: &ir.Node{ID: "ni2", Op: "name", Name: "i"}}},
				{ID: "ai", Op: "assign", Name: "i", Val: &ir.Node{ID: "ad2", Op: "add", Left: &ir.Node{ID: "ni3", Op: "name", Name: "i"}, Right: nInt("one", 1)}},
			},
		},
		ret("r", &ir.Node{ID: "ns2", Op: "name", Name: "s"}),
	}
	_, code := run(t, prog(mainFn(body...)))
	if code != 45 {
		t.Fatalf("exit %d", code)
	}
}

func TestCall(t *testing.T) {
	add := ir.Func{
		ID: "fn:demo.Add", Name: "Add", Result: "i64",
		Params: []ir.Param{{ID: "pa", Name: "a", Type: "i64"}, {ID: "pb", Name: "b", Type: "i64"}},
		Body: []*ir.Node{
			ret("r", &ir.Node{ID: "sum", Op: "add", Left: &ir.Node{ID: "na", Op: "name", Name: "a"}, Right: &ir.Node{ID: "nb", Op: "name", Name: "b"}}),
		},
	}
	main := mainFn(ret("rm", &ir.Node{ID: "call", Op: "call", Func: "Add", Args: []*ir.Node{nInt("x", 20), nInt("y", 22)}}))
	_, code := run(t, prog(add, main))
	if code != 42 {
		t.Fatalf("exit %d", code)
	}
}

func TestArgc(t *testing.T) {
	body := ret("r", &ir.Node{ID: "f", Op: "field", Name: "argc", Base: &ir.Node{ID: "io", Op: "name", Name: "io"}})
	_, code := run(t, prog(mainFn(body)), "a", "b")
	// argv = prog, a, b → 3
	if code != 3 {
		t.Fatalf("exit %d", code)
	}
}

func TestWrite(t *testing.T) {
	io := ioPkg()
	io.Funcs = []ir.Func{{
		ID: "fn:ovid/io.Write", Name: "Write", Result: "i64",
		Params: []ir.Param{{ID: "p1", Name: "fd", Type: "i64"}, {ID: "p2", Name: "p", Type: "i64"}, {ID: "p3", Name: "n", Type: "i64"}},
		Body: []*ir.Node{{
			ID: "rw", Op: "return",
			Val: &ir.Node{ID: "sc", Op: "syscall", Args: []*ir.Node{
				nInt("n", 1),
				{ID: "fd", Op: "name", Name: "fd"},
				{ID: "pp", Op: "name", Name: "p"},
				{ID: "nn", Op: "name", Name: "n"},
				nInt("z1", 0), nInt("z2", 0), nInt("z3", 0),
			}},
		}},
	}}
	demo := ir.Package{
		ID: "pkg:demo", Path: "demo",
		Imports: []ir.Import{{ID: "im", Path: "ovid/io"}},
		Funcs: []ir.Func{mainFn(
			&ir.Node{ID: "call", Op: "expr", Val: &ir.Node{ID: "c", Op: "call", Pkg: "ovid/io", Func: "Write", Args: []*ir.Node{
				nInt("fd", 1),
				{ID: "sp", Op: "strptr", ValK: 3, Str: "hi\n"},
				{ID: "sl", Op: "strlen", ValK: 3, Str: "hi\n"},
			}}},
			ret("r", nInt("z", 0)),
		)},
	}
	p := &ir.Program{Module: "t", Entry: "demo", Packages: []ir.Package{io, demo}}
	out, code := run(t, p)
	if code != 0 || out != "hi\n" {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestAlloc(t *testing.T) {
	// var p i64 = heap; store64(p, 42); return load64(p) via field heap
	body := []*ir.Node{
		{ID: "v", Op: "var", Name: "p", Type: "i64", Val: &ir.Node{ID: "h", Op: "field", Name: "heap", Base: &ir.Node{ID: "io", Op: "name", Name: "io"}}},
		{ID: "st", Op: "store64", Addr: &ir.Node{ID: "np", Op: "name", Name: "p"}, Val: nInt("v42", 42)},
		ret("r", &ir.Node{ID: "ld", Op: "load64", Arg: &ir.Node{ID: "np2", Op: "name", Name: "p"}}),
	}
	_, code := run(t, prog(mainFn(body...)))
	if code != 42 {
		t.Fatalf("exit %d", code)
	}
}

func TestDivModShift(t *testing.T) {
	// return (20 / 3) + (20 % 3) + (1 << 3) + (8 >> 2) = 6 + 2 + 8 + 2 = 18
	e := func(op, id string, l, r *ir.Node) *ir.Node {
		return &ir.Node{ID: id, Op: op, Left: l, Right: r}
	}
	expr := e("add", "a1",
		e("add", "a2",
			e("add", "a3", e("div", "d", nInt("n20", 20), nInt("n3", 3)), e("mod", "m", nInt("n20b", 20), nInt("n3b", 3))),
			e("shl", "s", nInt("one", 1), nInt("three", 3)),
		),
		e("shr", "r", nInt("eight", 8), nInt("two", 2)),
	)
	_, code := run(t, prog(mainFn(ret("ret", expr))))
	if code != 18 {
		t.Fatalf("exit %d", code)
	}
}

// TestBind: each declaration has its own local and type, a name resolves
// to the declaration in scope, and sibling blocks reuse slots, so a frame
// holds the most locals in scope at once, not every one the func declares.
func TestBind(t *testing.T) {
	v := func(name, typ string) *ir.Node { return &ir.Node{Op: "var", Name: name, Type: typ} }
	use := func(name string) *ir.Node { return &ir.Node{Op: "expr", Val: &ir.Node{Op: "name", Name: name}} }
	bT, bBool, useT, useBool := v("b", "*T"), v("b", "bool"), use("b"), use("b")
	body := []*ir.Node{
		v("a", "i64"),
		{Op: "if", Then: []*ir.Node{bT, v("c", "i64"), useT}, Else: []*ir.Node{bBool, useBool}},
		{Op: "if", Then: []*ir.Node{v("b", "i64")}},
		{Op: "while", Body: []*ir.Node{v("d", "i64"), {Op: "if", Then: []*ir.Node{v("e", "i64")}}}},
		v("f", "i64"),
	}
	c := &cg{pkg: &ir.Package{Path: "demo"}}
	c.bindFunc(&ir.Func{Params: []ir.Param{{Name: "p", Type: "i64"}}, Body: body})
	if len(c.locals) != 9 {
		t.Fatalf("%d locals, want 9: one per param and var", len(c.locals))
	}
	// In scope at once at most: p, a, and the then branch's b and c, or p,
	// a, and the loop's d and e.
	if c.live != 4 {
		t.Fatalf("live %d, want 4 (9 if siblings did not share slots)", c.live)
	}
	i, j := c.ref[useT.Val], c.ref[useBool.Val]
	if i != c.ref[bT] || j != c.ref[bBool] || i == j {
		t.Fatalf("b resolves to locals %d and %d, want %d and %d", i, j, c.ref[bT], c.ref[bBool])
	}
	if c.locals[i].typ != "*demo.T" || c.locals[j].typ != "bool" {
		t.Fatalf("b has types %s and %s", c.locals[i].typ, c.locals[j].typ)
	}
	if c.locals[i].disp != c.locals[j].disp {
		t.Fatalf("sibling b in slots %d and %d", c.locals[i].disp, c.locals[j].disp)
	}
}

// TestDivConst checks the instructions divConst emits, simulated, against
// Go's division, for divisors across the immediate range and dividends
// at the edges.
func TestDivConst(t *testing.T) {
	ds := []int64{math.MinInt32, math.MaxInt32, 1000000000, -1000000000, 1000000007}
	for d := int64(2); d <= 1000; d++ {
		ds = append(ds, d, -d)
	}
	for k := 2; k <= 31; k++ {
		for e := int64(-2); e <= 2; e++ {
			if d := int64(1)<<k + e; d <= math.MaxInt32 {
				ds = append(ds, d, -d)
			}
		}
	}
	rng := rand.New(rand.NewSource(55))
	for i := 0; i < 500; i++ {
		ds = append(ds, rng.Int63n(math.MaxInt32-2)+2, -rng.Int63n(math.MaxInt32-2)-2)
	}
	xs := []int64{0, 1, -1, 2, -2, math.MaxInt64, math.MinInt64, math.MinInt64 + 1, math.MaxInt64 - 1}
	for i := 0; i < 200; i++ {
		x := int64(rng.Uint64())
		xs = append(xs, x, x>>(i%64), -(x >> (i % 64)))
	}
	for _, d := range ds {
		for _, x := range xs {
			x2 := []int64{x, x / d * d, x/d*d - 1, x/d*d + 1}
			for _, x := range x2 {
				if q := simDiv(x, d); q != x/d {
					t.Fatalf("%d / %d: got %d, want %d", x, d, q, x/d)
				}
			}
		}
	}
}

// simDiv computes x / d the way divConst's instructions do.
func simDiv(x, d int64) int64 {
	if d > 0 && d&(d-1) == 0 {
		k := uint(bits.TrailingZeros64(uint64(d)))
		return (x + int64(uint64(x>>63)>>(64-k))) >> k
	}
	m, s := magic(d)
	// The high half of the signed product rdx:rax = x * m.
	hi, _ := bits.Mul64(uint64(x), uint64(m))
	h := int64(hi)
	if x < 0 {
		h -= m
	}
	if m < 0 {
		h -= x
	}
	if d > 0 && m < 0 {
		h += x
	} else if d < 0 && m > 0 {
		h -= x
	}
	h >>= s
	return h + int64(uint64(h)>>63)
}
