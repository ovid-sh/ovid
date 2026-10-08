// Package lower rewrites a checked program so that no bytes value is
// left in it, which lets the code generator keep one word per value.
//
// A bytes is an address and a length. After lowering, every bytes local,
// param, or field b is the pair of i64 b and b#n; a func returning bytes
// returns two results, one returning (bytes, i64) three (the third word
// rides in rcx: a return with a third value in Args, a var2 or assign2
// with a third name in Args); b[i] is a bload (a checked load); b[i] = v
// a chk and a store8; b[i:j] the pair (p + i, j - i) after two chks;
// len(b) is b#n; "lit" is strptr and strlen; bytes(p, n) the pair.
//
// The checks and the pairs' parts are simple operands (a name or a
// constant) or, for a field, its two loads left in place; anything else
// is first moved into a temp local (t#k) by a statement placed before
// the one being lowered. Moving an expression keeps evaluation order:
// every earlier expression in the same statement that is not a simple
// operand is moved too (a memory read left of a call that writes it
// must come first), the halves of a field among them, and an operand of
// && or || or a while condition that needs a move is restructured into
// an if, so the move runs exactly when the operand would have.
//
// The lowered tree is the code generator's alone: ids, hashes, and the
// tools see the tree the checker saw.
package lower

import (
	"fmt"
	"math"
	"strings"

	"ovid/internal/check"
	"ovid/internal/ir"
)

type lowerer struct {
	pkgs   map[string]*ir.Package
	pkg    *ir.Package
	bfield map[string]bool // "pkg.Type.field" declared bytes
	bfunc  map[string]int  // "pkg.Func" → words its result takes (2 or 3)
	scopes []map[string]string
	tmp    int
	locals map[string]*local // the function's locals, for the check elision
	free   []freePair        // b[i] needs no check here
}

// local is what the check elision knows about one of a function's
// names: how often it is declared (a param or a var; var2 too), the
// value of its one var, whether anything assigns it, and whether every
// assignment is name = name + 1.
type local struct {
	decls    int
	val      *ir.Node
	assigned bool
	grows    bool
}

// freePair is a loop's bytes and index whose b[i] the condition proves
// in bounds.
type freePair struct{ b, i string }

// pre is the list of statements a lowering places before the statement
// it is working on.
type pre []*ir.Node

// Program lowers p in place.
func Program(p *ir.Program) error {
	l := &lowerer{pkgs: map[string]*ir.Package{}, bfield: map[string]bool{}, bfunc: map[string]int{}}
	for i := range p.Packages {
		l.pkgs[p.Packages[i].Path] = &p.Packages[i]
	}
	// Signatures first, so a call site knows what its callee returns.
	for _, pk := range l.pkgs {
		for ti := range pk.Types {
			for _, f := range pk.Types[ti].Fields {
				if f.Type == "bytes" {
					l.bfield[pk.Path+"."+pk.Types[ti].Name+"."+f.Name] = true
				}
			}
		}
		for fi := range pk.Funcs {
			if fn := &pk.Funcs[fi]; fn.Result == "bytes" {
				l.bfunc[pk.Path+"."+fn.Name] = 2
				if fn.Result2 != "" {
					l.bfunc[pk.Path+"."+fn.Name] = 3
				}
			}
		}
	}
	for _, pk := range l.pkgs {
		l.pkg = pk
		for ti := range pk.Types {
			td := &pk.Types[ti]
			var fs []ir.Field
			for _, f := range td.Fields {
				if f.Type == "bytes" {
					fs = append(fs, ir.Field{ID: f.ID, Name: f.Name, Type: "i64"}, ir.Field{ID: f.ID + "#n", Name: f.Name + "#n", Type: "i64"})
					continue
				}
				fs = append(fs, f)
			}
			td.Fields = fs
		}
		for fi := range pk.Funcs {
			if err := l.fn(&pk.Funcs[fi]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *lowerer) fn(fn *ir.Func) error {
	l.scopes = []map[string]string{{}}
	var ps []ir.Param
	for _, pa := range fn.Params {
		l.bind(pa.Name, pa.Type)
		if pa.Type == "bytes" {
			ps = append(ps, ir.Param{ID: pa.ID, Name: pa.Name, Type: "i64"}, ir.Param{ID: pa.ID + "#n", Name: pa.Name + "#n", Type: "i64"})
			continue
		}
		ps = append(ps, pa)
	}
	fn.Params = ps
	if fn.Result == "bytes" {
		fn.Result = "i64"
		if fn.Result2 == "" {
			fn.Result2 = "i64"
		}
	}
	l.locals = map[string]*local{}
	for _, pa := range fn.Params {
		l.local(pa.Name).decls++
	}
	l.scan(fn.Body)
	l.free = nil
	body, err := l.stmts(fn.Body)
	fn.Body = body
	return err
}

func (l *lowerer) local(name string) *local {
	c := l.locals[name]
	if c == nil {
		c = &local{grows: true}
		l.locals[name] = c
	}
	return c
}

// scan records the declarations and assignments of the function's
// names, through every block.
func (l *lowerer) scan(ss []*ir.Node) {
	for _, s := range ss {
		switch s.Op {
		case "var":
			c := l.local(s.Name)
			c.decls++
			c.val = s.Val
		case "var2":
			l.local(s.Name).decls++
			l.local(s.Two.Name).decls++
		case "assign":
			c := l.local(s.Name)
			c.assigned = true
			if growth(s) != 1 {
				c.grows = false
			}
		case "assign2":
			for _, nm := range []string{s.Name, s.Two.Name} {
				c := l.local(nm)
				c.assigned, c.grows = true, false
			}
		}
		l.scan(s.Then)
		l.scan(s.Else)
		l.scan(s.Body)
	}
}

// growth is the k of the statement name = name + k, k a positive
// literal, or 0 when s is not one.
func growth(s *ir.Node) int64 {
	v := s.Val
	if s.Op != "assign" || v.Op != "add" || v.Left.Op != "name" || v.Left.Pkg != "" || v.Left.Name != s.Name || v.Right.Op != "int" || v.Right.Int < 1 {
		return 0
	}
	return v.Right.Int
}

// lenOf is the bytes local b when n is len(b), or "".
func (l *lowerer) lenOf(n *ir.Node) string {
	if n == nil || (n.Op != "len" && n.Op != "blen") {
		return ""
	}
	b := n.Base
	if n.Op == "len" {
		if n.Pkg != "" || b == nil {
			return ""
		}
		b = name(n.Name)
	}
	if b.Op != "name" || b.Pkg != "" || l.lookup(b.Name) != "bytes" {
		return ""
	}
	return b.Name
}

// freeLoop says what the while statement s proves: its condition is
// i < len(b), or i < n with n the one var len(b), where b and n are
// never assigned, i is declared once as a var with a literal that is
// not negative and only ever grows by one, and the body assigns i only
// in its own top-level statements. Then b[i] is in bounds in the
// condition and in the body's statements before the first of those,
// which is the k returned (len(body) when there is none); ok is false
// when s proves nothing. Growing by one cannot wrap, since i < len(b)
// < 2^63 first; a larger step could, to a negative i the signed
// condition lets through. The names must be locals in scope here: a
// package constant can spell the same, and the table is by name.
func (l *lowerer) freeLoop(s *ir.Node) (p freePair, k int, ok bool) {
	c := s.Cond
	if c.Op == "land" {
		c = c.Left
	}
	if c.Op != "lt" || c.Left.Op != "name" || c.Left.Pkg != "" {
		return p, 0, false
	}
	i := c.Left.Name
	if l.lookup(i) == "" {
		return p, 0, false
	}
	b := l.lenOf(c.Right)
	if b == "" && c.Right.Op == "name" && c.Right.Pkg == "" && l.lookup(c.Right.Name) != "" {
		if n := l.locals[c.Right.Name]; n != nil && n.decls == 1 && !n.assigned && n.val != nil {
			b = l.lenOf(n.val)
		}
	}
	if b == "" {
		return p, 0, false
	}
	if bl := l.locals[b]; bl == nil || bl.decls != 1 || bl.assigned {
		return p, 0, false
	}
	il := l.locals[i]
	if il == nil || il.decls != 1 || il.val == nil || il.val.Op != "int" || il.val.Int < 0 || !il.grows {
		return p, 0, false
	}
	k = len(s.Body)
	for j, t := range s.Body {
		if t.Op == "assign" && t.Name == i {
			if j < k {
				k = j
			}
			continue
		}
		if assigns(t, i) {
			return p, 0, false
		}
	}
	return freePair{b, i}, k, true
}

// assigns says whether a statement under s (not s itself) assigns name.
func assigns(s *ir.Node, name string) bool {
	for _, ss := range [][]*ir.Node{s.Then, s.Else, s.Body} {
		for _, t := range ss {
			if (t.Op == "assign" && t.Name == name) || assigns(t, name) {
				return true
			}
		}
	}
	return false
}

// isFree says whether b[i] needs no check where the lowering stands.
func (l *lowerer) isFree(b, i *ir.Node) bool {
	if b.Op != "name" || i.Op != "name" || b.Pkg != "" || i.Pkg != "" {
		return false
	}
	for _, f := range l.free {
		if f.b == b.Name && f.i == i.Name {
			return true
		}
	}
	return false
}

// bind records a local's type in its resolved form (*pkg.T), which the
// code generator also accepts as a source type.
func (l *lowerer) bind(name, t string) {
	if r, err := check.Resolve(l.pkg, t, l.pkgs); err == nil {
		t = r
	}
	l.scopes[len(l.scopes)-1][name] = t
}

func (l *lowerer) lookup(name string) string {
	for i := len(l.scopes) - 1; i >= 0; i-- {
		if t, ok := l.scopes[i][name]; ok {
			return t
		}
	}
	return ""
}

func (l *lowerer) push() { l.scopes = append(l.scopes, map[string]string{}) }
func (l *lowerer) pop()  { l.scopes = l.scopes[:len(l.scopes)-1] }

// fieldIsBytes says whether field f of the struct pointer type bt was
// declared bytes (its decl has been expanded by now).
func (l *lowerer) fieldIsBytes(bt, f string) bool {
	return l.bfield[strings.TrimPrefix(bt, "*")+"."+f]
}

// resultWords is how many words a call to n returns: 1, or 2 or 3 for a
// bytes result.
func (l *lowerer) resultWords(n *ir.Node) int {
	path := n.Pkg
	if path == "" {
		path = l.pkg.Path
	}
	if w := l.bfunc[path+"."+n.Func]; w != 0 {
		return w
	}
	return 1
}

// typeOf is the checker's type of n, for the shapes the lowering asks
// about: whether something is a bytes, and a field's base.
func (l *lowerer) typeOf(n *ir.Node) string {
	switch n.Op {
	case "name":
		if n.Pkg == "" {
			if t := l.lookup(n.Name); t != "" {
				return t
			}
		}
		return "i64"
	case "str", "slice", "bytes":
		return "bytes"
	case "cast":
		t, _ := check.Resolve(l.pkg, n.Type, l.pkgs)
		return t
	case "field":
		bt := l.typeOf(n.Base)
		if l.fieldIsBytes(bt, n.Name) {
			return "bytes"
		}
		t, _, _ := check.FieldType(bt, n.Name, l.pkgs)
		return t
	case "call":
		if l.resultWords(n) > 1 {
			return "bytes"
		}
		path := n.Pkg
		if path == "" {
			path = l.pkg.Path
		}
		if pk := l.pkgs[path]; pk != nil {
			for i := range pk.Funcs {
				if pk.Funcs[i].Name == n.Func {
					return pk.Funcs[i].Result
				}
			}
		}
		return "i64"
	case "bool", "eq", "ne", "lt", "le", "gt", "ge", "ult", "land", "lor", "not":
		return "bool"
	}
	return "i64"
}

func name(s string) *ir.Node { return &ir.Node{Op: "name", Name: s} }
func add(a, b *ir.Node) *ir.Node {
	return &ir.Node{Op: "add", Left: a, Right: b}
}
func sub(a, b *ir.Node) *ir.Node {
	return &ir.Node{Op: "sub", Left: a, Right: b}
}
func one() *ir.Node { return &ir.Node{Op: "int", Int: 1} }

// simple says n can be an operand of a check or a pair without being
// moved: a local or a constant.
func simple(n *ir.Node) bool {
	switch n.Op {
	case "name", "int", "strlen", "strptr", "sizeof", "len":
		return true
	}
	return false
}

// pure says n is a load with nothing to run: a local, or a field of one.
func pure(n *ir.Node) bool {
	return n.Op == "name" || n.Op == "field" && pure(n.Base)
}

// lowType is the type a temp holding the lowered n takes: typeOf, except
// that a field it still calls bytes is the address half of a pair, which
// pair leaves as a load of the field named after it.
func (l *lowerer) lowType(n *ir.Node) string {
	if t := l.typeOf(n); t != "bytes" || n.Op != "field" {
		return t
	}
	return "i64"
}

// fix makes the lowered n a simple operand, moving it into a temp if it
// is not one.
func (l *lowerer) fix(n *ir.Node, p *pre) *ir.Node {
	if simple(n) {
		return n
	}
	return l.temp(l.lowType(n), n, p)
}

// fixAt moves the lowered operands in ns that are not simple into temps
// declared at position at of p, ahead of what was added since, so their
// reads keep their place in source order.
func (l *lowerer) fixAt(p *pre, at int, ns ...**ir.Node) {
	if len(*p) > at {
		l.fixAll(p, at, ns...)
	}
}

// fixAll is fixAt whether or not p grew.
func (l *lowerer) fixAll(p *pre, at int, ns ...**ir.Node) {
	for _, n := range ns {
		if !simple(*n) {
			l.insertTemp(p, at, n)
			at++
		}
	}
}

// temp declares a fresh local of type t holding v, in pre, and names it.
func (l *lowerer) temp(t string, v *ir.Node, p *pre) *ir.Node {
	l.tmp++
	nm := fmt.Sprintf("t#%d", l.tmp)
	l.bind(nm, t)
	*p = append(*p, &ir.Node{Op: "var", Name: nm, Type: t, Val: v})
	return name(nm)
}

// hoist lowers n and makes it a simple operand, moving it into a temp
// if it is not one.
func (l *lowerer) hoist(n *ir.Node, p *pre) (*ir.Node, error) {
	n, err := l.expr(n, p)
	if err != nil {
		return nil, err
	}
	return l.fix(n, p), nil
}

// pair lowers a bytes-typed expression to its address and length: simple
// operands, or a field's two loads in place, placing in p the statements
// that must run first.
func (l *lowerer) pair(n *ir.Node, p *pre) (addr, ln *ir.Node, err error) {
	switch n.Op {
	case "name":
		return name(n.Name), name(n.Name + "#n"), nil
	case "str":
		return &ir.Node{Op: "strptr", ValK: 3, Str: n.Str}, &ir.Node{Op: "strlen", ValK: 3, Str: n.Str}, nil
	case "bytes":
		a, err := l.hoist(n.Left, p)
		if err != nil {
			return nil, nil, err
		}
		ln, err := l.hoist(n.Right, p)
		if err != nil {
			return nil, nil, err
		}
		// n < 2^63 unsigned: a negative length would let every later
		// check pass. A literal length settles it here.
		if ln.Op != "int" || ln.Int < 0 {
			*p = append(*p, &ir.Node{Op: "chk", Addr: ln, Val: &ir.Node{Op: "int", Int: math.MinInt64}})
		}
		return a, ln, nil
	case "field":
		// Two loads of the expanded fields, left where they are: a base
		// with nothing to run is read in place, any other is moved first.
		base, err := l.expr(n.Base, p)
		if err != nil {
			return nil, nil, err
		}
		if !pure(base) {
			base = l.temp(l.typeOf(base), base, p)
		}
		return &ir.Node{Op: "field", Base: base, Name: n.Name}, &ir.Node{Op: "field", Base: base, Name: n.Name + "#n"}, nil
	case "slice":
		bp, bn, err := l.pair(n.Base, p)
		if err != nil {
			return nil, nil, err
		}
		at := len(*p)
		i, err := l.hoist(n.Left, p)
		if err != nil {
			return nil, nil, err
		}
		j, err := l.hoist(n.Right, p)
		if err != nil {
			return nil, nil, err
		}
		l.fixAt(p, at, &bp, &bn)
		// j <= n and i <= j, unsigned, so a negative bound trips too.
		*p = append(*p, &ir.Node{Op: "chk", Addr: j, Val: add(bn, one())})
		*p = append(*p, &ir.Node{Op: "chk", Addr: i, Val: add(j, one())})
		a := l.temp("i64", add(bp, i), p)
		ln = l.temp("i64", sub(j, i), p)
		return a, ln, nil
	case "call":
		c, err := l.expr(n, p)
		if err != nil {
			return nil, nil, err
		}
		l.tmp++
		t := fmt.Sprintf("t#%d", l.tmp)
		l.bind(t, "i64")
		l.bind(t+"#n", "i64")
		*p = append(*p, &ir.Node{Op: "var2", Name: t, Type: "i64", Two: &ir.Second{Name: t + "#n", Type: "i64"}, Val: c})
		return name(t), name(t + "#n"), nil
	}
	return nil, nil, fmt.Errorf("lower: a bytes %s", n.Op)
}

// expr lowers the bytes operations inside an i64 or bool expression,
// placing in p what must run first. Children go in evaluation order,
// and when one adds to p, every earlier child that is not a simple
// operand is moved into a temp ahead of that addition, so what it
// computes or reads still happens in source order.
func (l *lowerer) expr(n *ir.Node, p *pre) (*ir.Node, error) {
	if n == nil {
		return nil, nil
	}
	// The checker rewrites b[i] and len(b) on a bytes local to byte and
	// blen; a tree compiled without it (a load alone) still has index and
	// len, which say the same when the name is a bytes.
	if (n.Op == "index" || n.Op == "len") && n.Pkg == "" && l.lookup(n.Name) == "bytes" && n.Base != nil {
		m := *n
		m.Op = map[string]string{"index": "byte", "len": "blen"}[n.Op]
		n = &m
	}
	switch n.Op {
	case "byte":
		free := l.isFree(n.Base, n.Arg)
		bp, bn, err := l.pair(n.Base, p)
		if err != nil {
			return nil, err
		}
		at := len(*p)
		i, err := l.hoist(n.Arg, p)
		if err != nil {
			return nil, err
		}
		l.fixAt(p, at, &bp, &bn)
		if free {
			return &ir.Node{ID: n.ID, Op: "load8", Arg: add(bp, i)}, nil
		}
		return &ir.Node{ID: n.ID, Op: "bload", Base: bp, Left: i, Right: bn}, nil
	case "blen":
		_, bn, err := l.pair(n.Base, p)
		return bn, err
	case "ptr":
		a, _, err := l.pair(n.Arg, p)
		return a, err
	case "land", "lor":
		left, err := l.expr(n.Left, p)
		if err != nil {
			return nil, err
		}
		var rp pre
		right, err := l.expr(n.Right, &rp)
		if err != nil {
			return nil, err
		}
		if len(rp) == 0 {
			m := *n
			m.Left, m.Right = left, right
			return &m, nil
		}
		// The right side has statements: run them only when it is
		// evaluated. var t bool = left; if t { rp; t = right } (or if
		// !t for ||), and the expression is t.
		t := l.temp("bool", left, p)
		cond := ir.Node{Op: "name", Name: t.Name}
		body := append(rp, &ir.Node{Op: "assign", Name: t.Name, Val: right})
		if n.Op == "land" {
			*p = append(*p, &ir.Node{Op: "if", Cond: &cond, Then: body})
		} else {
			*p = append(*p, &ir.Node{Op: "if", Cond: &ir.Node{Op: "not", Arg: &cond}, Then: body})
		}
		return name(t.Name), nil
	case "call", "syscall":
		m := *n
		m.Args = nil
		var kept []int // indexes into m.Args of earlier args not simple
		for _, a := range n.Args {
			at := len(*p)
			if l.typeOf(a) == "bytes" {
				ap, an, err := l.pair(a, p)
				if err != nil {
					return nil, err
				}
				kept = l.keepOrder(p, at, m.Args, kept)
				if !simple(ap) {
					kept = append(kept, len(m.Args), len(m.Args)+1)
				}
				m.Args = append(m.Args, ap, an)
				continue
			}
			a, err := l.expr(a, p)
			if err != nil {
				return nil, err
			}
			kept = l.keepOrder(p, at, m.Args, kept)
			if !simple(a) {
				kept = append(kept, len(m.Args))
			}
			m.Args = append(m.Args, a)
		}
		return &m, nil
	}
	m := *n
	if err := l.ordered(p, &m.Left, &m.Right, &m.Arg, &m.Base, &m.Cond); err != nil {
		return nil, err
	}
	return &m, nil
}

// ordered lowers the operands in evaluation order, in place; when one
// adds to p, every earlier operand that is not simple is moved into a
// temp ahead of that addition, so what it computes or reads still
// happens in source order. Expression children and a statement's
// operands both go through it.
func (l *lowerer) ordered(p *pre, fields ...**ir.Node) error {
	var done []**ir.Node // earlier operands lowered in place
	for _, f := range fields {
		if *f == nil {
			continue
		}
		at := len(*p)
		v, err := l.expr(*f, p)
		if err != nil {
			return err
		}
		if len(*p) > at {
			for _, g := range done {
				if !simple(*g) {
					l.insertTemp(p, at, g)
					at++
				}
			}
			done = nil
		}
		*f = v
		done = append(done, f)
	}
	return nil
}

// keepOrder is expr's reordering for call arguments: when lowering the
// argument that started at mark at added statements, the earlier
// arguments that are not simple (kept, indexes into args) move into
// temps placed at at, ahead of those statements. It returns kept, emptied
// when they moved.
func (l *lowerer) keepOrder(p *pre, at int, args []*ir.Node, kept []int) []int {
	if len(*p) == at {
		return kept
	}
	for _, i := range kept {
		l.insertTemp(p, at, &args[i])
		at++
	}
	return nil
}

// insertTemp moves *n into a temp declared at position at of p.
func (l *lowerer) insertTemp(p *pre, at int, n **ir.Node) {
	l.tmp++
	nm := fmt.Sprintf("t#%d", l.tmp)
	t := l.lowType(*n)
	l.bind(nm, t)
	decl := &ir.Node{Op: "var", Name: nm, Type: t, Val: *n}
	*p = append((*p)[:at], append(pre{decl}, (*p)[at:]...)...)
	*n = name(nm)
}

func (l *lowerer) stmts(ss []*ir.Node) ([]*ir.Node, error) {
	return l.region(ss, -1)
}

// region lowers the statements of a block; with k not negative, the last
// entry of l.free holds for the first k of them only and is dropped
// after them.
func (l *lowerer) region(ss []*ir.Node, k int) ([]*ir.Node, error) {
	l.push()
	defer l.pop()
	if k >= 0 {
		defer func() { l.free = l.free[:len(l.free)-1] }()
	}
	var out []*ir.Node
	for j, s := range ss {
		if j == k {
			l.free[len(l.free)-1] = freePair{}
		}
		lowered, err := l.stmt(s)
		if err != nil {
			return nil, err
		}
		// What was placed before the statement is the statement's, for
		// the crash report when a check in it fails: the statements
		// themselves and what the restructuring of && or a while put
		// under them.
		for _, m := range lowered {
			claim(m, s.ID)
		}
		out = append(out, lowered...)
	}
	return out, nil
}

// claim gives statement n and the generated statements under it the id
// id, leaving statements that have one (the source's own) alone.
func claim(n *ir.Node, id string) {
	if n.ID != "" {
		return
	}
	n.ID = id
	for _, b := range [][]*ir.Node{n.Then, n.Else, n.Body} {
		for _, m := range b {
			claim(m, id)
		}
	}
}

func (l *lowerer) stmt(s *ir.Node) ([]*ir.Node, error) {
	var p pre
	switch s.Op {
	case "var":
		if s.Type == "bytes" {
			if s.Val.Op == "call" {
				c, err := l.expr(s.Val, &p)
				if err != nil {
					return nil, err
				}
				l.bind(s.Name, "bytes")
				return append(p, &ir.Node{ID: s.ID, Op: "var2", Name: s.Name, Type: "i64", Two: &ir.Second{Name: s.Name + "#n", Type: "i64"}, Val: c}), nil
			}
			a, ln, err := l.pair(s.Val, &p)
			if err != nil {
				return nil, err
			}
			l.bind(s.Name, "bytes")
			return append(p, &ir.Node{ID: s.ID, Op: "var", Name: s.Name, Type: "i64", Val: a},
				&ir.Node{Op: "var", Name: s.Name + "#n", Type: "i64", Val: ln}), nil
		}
		v, err := l.expr(s.Val, &p)
		if err != nil {
			return nil, err
		}
		l.bind(s.Name, s.Type)
		m := *s
		m.Val = v
		return append(p, &m), nil
	case "var2", "assign2":
		v, err := l.expr(s.Val, &p)
		if err != nil {
			return nil, err
		}
		m := *s
		m.Val = v
		if s.Op == "var2" {
			l.bind(s.Name, s.Type)
			l.bind(s.Two.Name, s.Two.Type)
		}
		if s.Val.Op == "call" && l.resultWords(s.Val) == 3 {
			// var b bytes, e i64 = f(): b, b#n, and e, the third in rcx.
			m.Type = "i64"
			m.Two = &ir.Second{Name: s.Name + "#n", Type: "i64"}
			if s.Name == "_" {
				m.Two.Name = "_"
			}
			if s.Op == "assign2" {
				m.Two.Type = ""
			}
			m.Args = []*ir.Node{name(s.Two.Name)}
		}
		return append(p, &m), nil
	case "assign":
		if l.lookup(s.Name) == "bytes" {
			if s.Val.Op == "call" {
				c, err := l.expr(s.Val, &p)
				if err != nil {
					return nil, err
				}
				return append(p, &ir.Node{ID: s.ID, Op: "assign2", Name: s.Name, Two: &ir.Second{Name: s.Name + "#n"}, Val: c}), nil
			}
			a, ln, err := l.pair(s.Val, &p)
			if err != nil {
				return nil, err
			}
			return append(p, &ir.Node{ID: s.ID, Op: "assign", Name: s.Name, Val: a},
				&ir.Node{Op: "assign", Name: s.Name + "#n", Val: ln}), nil
		}
		v, err := l.expr(s.Val, &p)
		if err != nil {
			return nil, err
		}
		m := *s
		m.Val = v
		return append(p, &m), nil
	case "setfield":
		if l.fieldIsBytes(l.typeOf(s.Base), s.Name) {
			base, err := l.hoist(s.Base, &p)
			if err != nil {
				return nil, err
			}
			a, ln, err := l.pair(s.Val, &p)
			if err != nil {
				return nil, err
			}
			// Both words are read before the first store: through a cast
			// the destination may overlap the source, and the whole value
			// is what the assignment means.
			a, ln = l.fix(a, &p), l.fix(ln, &p)
			return append(p, &ir.Node{ID: s.ID, Op: "setfield", Base: base, Name: s.Name, Val: a},
				&ir.Node{Op: "setfield", Base: base, Name: s.Name + "#n", Val: ln}), nil
		}
		m := *s
		if err := l.ordered(&p, &m.Base, &m.Val); err != nil {
			return nil, err
		}
		return append(p, &m), nil
	case "setbyte":
		ix := s.Base
		free := l.isFree(ix.Base, ix.Arg)
		bp, bn, err := l.pair(ix.Base, &p)
		if err != nil {
			return nil, err
		}
		at := len(p)
		i, err := l.hoist(ix.Arg, &p)
		if err != nil {
			return nil, err
		}
		v, err := l.expr(s.Val, &p)
		if err != nil {
			return nil, err
		}
		// The store computes v before its address, so when v could
		// change b, b is read first and the check and the store see the
		// same pair.
		if !simple(v) {
			l.fixAll(&p, at, &bp, &bn)
		} else {
			l.fixAt(&p, at, &bp, &bn)
		}
		store := &ir.Node{ID: s.ID, Op: "store8", Addr: add(bp, i), Val: v}
		if free {
			return append(p, store), nil
		}
		return append(p, &ir.Node{Op: "chk", Addr: i, Val: bn}, store), nil
	case "return":
		if s.Val != nil && l.typeOf(s.Val) == "bytes" {
			if s.Val.Op == "call" && s.Val2 == nil {
				// Forwarded: the callee's results are this func's.
				c, err := l.expr(s.Val, &p)
				if err != nil {
					return nil, err
				}
				return append(p, &ir.Node{ID: s.ID, Op: "return", Val: c}), nil
			}
			a, ln, err := l.pair(s.Val, &p)
			if err != nil {
				return nil, err
			}
			m := &ir.Node{ID: s.ID, Op: "return", Val: a, Val2: ln}
			if s.Val2 != nil {
				// return b, e: the error code is the third word, and the
				// code generator computes it first, so b is read into
				// temps ahead of it.
				m.Val, m.Val2 = l.fix(a, &p), l.fix(ln, &p)
				e, err := l.expr(s.Val2, &p)
				if err != nil {
					return nil, err
				}
				m.Args = []*ir.Node{e}
			}
			return append(p, m), nil
		}
		m := *s
		if err := l.ordered(&p, &m.Val, &m.Val2); err != nil {
			return nil, err
		}
		return append(p, &m), nil
	case "if":
		cond, err := l.expr(s.Cond, &p)
		if err != nil {
			return nil, err
		}
		then, err := l.stmts(s.Then)
		if err != nil {
			return nil, err
		}
		els, err := l.stmts(s.Else)
		if err != nil {
			return nil, err
		}
		m := *s
		m.Cond, m.Then, m.Else = cond, then, els
		return append(p, &m), nil
	case "while":
		// A loop over b by i needs no check of b[i] in its condition and
		// before i grows: the condition proved it.
		k := len(s.Body)
		if f, kk, ok := l.freeLoop(s); ok {
			l.free = append(l.free, f)
			k = kk
		} else {
			l.free = append(l.free, freePair{})
		}
		var cp pre
		cond, err := l.expr(s.Cond, &cp)
		if err != nil {
			return nil, err
		}
		body, err := l.region(s.Body, k)
		if err != nil {
			return nil, err
		}
		if len(cp) == 0 {
			m := *s
			m.Cond, m.Body = cond, body
			return []*ir.Node{&m}, nil
		}
		// The condition has statements, which must run before each
		// test: var go bool = true; while go { cp; if cond { body }
		// else { go = false } }.
		g := l.temp("bool", &ir.Node{Op: "bool", ValK: 2, Bool: true}, &p)
		stop := &ir.Node{Op: "assign", Name: g.Name, Val: &ir.Node{Op: "bool", ValK: 2, Bool: false}}
		// Without an id of its own, so claim reaches the checks in cp.
		loop := &ir.Node{Op: "while", Cond: name(g.Name),
			Body: append(cp, &ir.Node{Op: "if", Cond: cond, Then: body, Else: []*ir.Node{stop}})}
		return append(p, loop), nil
	}
	// expr, store*, chk: lower the expressions in place, in order.
	m := *s
	if err := l.ordered(&p, &m.Addr, &m.Val, &m.Val2); err != nil {
		return nil, err
	}
	return append(p, &m), nil
}

// Clone is a deep copy of p's packages' types and funcs, the parts
// lowering rewrites, so the module's own tree, which the tools and crash
// reports read by id, stays as the checker saw it.
func Clone(p *ir.Program) *ir.Program {
	q := *p
	q.Packages = make([]ir.Package, len(p.Packages))
	for i := range p.Packages {
		pk := p.Packages[i]
		pk.Types = append([]ir.TypeDecl(nil), pk.Types...)
		for ti := range pk.Types {
			pk.Types[ti].Fields = append([]ir.Field(nil), pk.Types[ti].Fields...)
		}
		pk.Funcs = append([]ir.Func(nil), pk.Funcs...)
		for fi := range pk.Funcs {
			fn := &pk.Funcs[fi]
			fn.Params = append([]ir.Param(nil), fn.Params...)
			fn.Body = cloneNodes(fn.Body)
		}
		q.Packages[i] = pk
	}
	return &q
}

func cloneNodes(ns []*ir.Node) []*ir.Node {
	if ns == nil {
		return nil
	}
	out := make([]*ir.Node, len(ns))
	for i, n := range ns {
		out[i] = cloneNode(n)
	}
	return out
}

func cloneNode(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	m := *n
	if n.Two != nil {
		two := *n.Two
		m.Two = &two
	}
	m.Left, m.Right, m.Arg, m.Base = cloneNode(n.Left), cloneNode(n.Right), cloneNode(n.Arg), cloneNode(n.Base)
	m.Addr, m.Val, m.Val2, m.Cond = cloneNode(n.Addr), cloneNode(n.Val), cloneNode(n.Val2), cloneNode(n.Cond)
	m.Args, m.Then, m.Else, m.Body = cloneNodes(n.Args), cloneNodes(n.Then), cloneNodes(n.Else), cloneNodes(n.Body)
	return &m
}
