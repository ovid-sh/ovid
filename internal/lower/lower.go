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
// The checks and the pairs' parts must be simple operands (a name or a
// constant), so anything else is first moved into a temp local (t#k)
// by a statement placed before the one being lowered. Moving an
// expression keeps evaluation order: every earlier expression in the
// same statement that calls a func is moved too, and an operand of &&
// or || or a while condition that needs a move is restructured into an
// if, so the move runs exactly when the operand would have.
//
// The lowered tree is the code generator's alone: ids, hashes, and the
// tools see the tree the checker saw.
package lower

import (
	"fmt"
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
}

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
	body, err := l.stmts(fn.Body)
	fn.Body = body
	return err
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

// hasCall says evaluating n could call a func.
func hasCall(n *ir.Node) bool {
	found := false
	n.Walk(func(m *ir.Node) {
		if m.Op == "call" || m.Op == "syscall" {
			found = true
		}
	})
	return found
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
	if simple(n) {
		return n, nil
	}
	return l.temp(l.typeOf(n), n, p), nil
}

// pair lowers a bytes-typed expression to its address and length, both
// simple operands, placing in p the statements that must run first.
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
		return a, ln, err
	case "field":
		base, err := l.hoist(n.Base, p)
		if err != nil {
			return nil, nil, err
		}
		a := l.temp("i64", &ir.Node{Op: "field", Base: base, Name: n.Name}, p)
		ln = l.temp("i64", &ir.Node{Op: "field", Base: base, Name: n.Name + "#n"}, p)
		return a, ln, nil
	case "slice":
		bp, bn, err := l.pair(n.Base, p)
		if err != nil {
			return nil, nil, err
		}
		i, err := l.hoist(n.Left, p)
		if err != nil {
			return nil, nil, err
		}
		j, err := l.hoist(n.Right, p)
		if err != nil {
			return nil, nil, err
		}
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
// and when one adds to p, every earlier child that calls a func is
// moved into a temp ahead of that addition, so calls still happen in
// source order.
func (l *lowerer) expr(n *ir.Node, p *pre) (*ir.Node, error) {
	if n == nil {
		return nil, nil
	}
	switch n.Op {
	case "byte":
		bp, bn, err := l.pair(n.Base, p)
		if err != nil {
			return nil, err
		}
		i, err := l.hoist(n.Arg, p)
		if err != nil {
			return nil, err
		}
		return &ir.Node{ID: n.ID, Op: "bload", Base: bp, Left: i, Right: bn}, nil
	case "blen":
		_, bn, err := l.pair(n.Base, p)
		return bn, err
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
		var kept []int // indexes into m.Args of earlier args with calls
		for _, a := range n.Args {
			at := len(*p)
			if l.typeOf(a) == "bytes" {
				ap, an, err := l.pair(a, p)
				if err != nil {
					return nil, err
				}
				kept = l.keepOrder(p, at, m.Args, kept)
				m.Args = append(m.Args, ap, an)
				continue
			}
			a, err := l.expr(a, p)
			if err != nil {
				return nil, err
			}
			kept = l.keepOrder(p, at, m.Args, kept)
			if hasCall(a) {
				kept = append(kept, len(m.Args))
			}
			m.Args = append(m.Args, a)
		}
		return &m, nil
	}
	m := *n
	var done []**ir.Node // earlier children lowered in place
	for _, f := range []**ir.Node{&m.Left, &m.Right, &m.Arg, &m.Base, &m.Cond} {
		if *f == nil {
			continue
		}
		at := len(*p)
		v, err := l.expr(*f, p)
		if err != nil {
			return nil, err
		}
		if len(*p) > at {
			// Earlier siblings with calls move ahead of this addition.
			for _, g := range done {
				if hasCall(*g) {
					l.insertTemp(p, at, g)
					at++
				}
			}
			done = nil
		}
		*f = v
		done = append(done, f)
	}
	return &m, nil
}

// keepOrder is expr's reordering for call arguments: when lowering the
// argument that started at mark at added statements, the earlier
// arguments that call funcs (kept, indexes into args) move into temps
// placed at at, ahead of those statements. It returns kept, emptied
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
	t := l.typeOf(*n)
	l.bind(nm, t)
	decl := &ir.Node{Op: "var", Name: nm, Type: t, Val: *n}
	*p = append((*p)[:at], append(pre{decl}, (*p)[at:]...)...)
	*n = name(nm)
}

func (l *lowerer) stmts(ss []*ir.Node) ([]*ir.Node, error) {
	l.push()
	defer l.pop()
	var out []*ir.Node
	for _, s := range ss {
		lowered, err := l.stmt(s)
		if err != nil {
			return nil, err
		}
		// What was placed before the statement is the statement's, for
		// the crash report when a check in it fails.
		for _, m := range lowered {
			if m.ID == "" {
				m.ID = s.ID
			}
		}
		out = append(out, lowered...)
	}
	return out, nil
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
			return append(p, &ir.Node{ID: s.ID, Op: "setfield", Base: base, Name: s.Name, Val: a},
				&ir.Node{Op: "setfield", Base: base, Name: s.Name + "#n", Val: ln}), nil
		}
		base, err := l.expr(s.Base, &p)
		if err != nil {
			return nil, err
		}
		v, err := l.expr(s.Val, &p)
		if err != nil {
			return nil, err
		}
		m := *s
		m.Base, m.Val = base, v
		return append(p, &m), nil
	case "setbyte":
		ix := s.Base
		bp, bn, err := l.pair(ix.Base, &p)
		if err != nil {
			return nil, err
		}
		i, err := l.hoist(ix.Arg, &p)
		if err != nil {
			return nil, err
		}
		v, err := l.expr(s.Val, &p)
		if err != nil {
			return nil, err
		}
		return append(p, &ir.Node{Op: "chk", Addr: i, Val: bn},
			&ir.Node{ID: s.ID, Op: "store8", Addr: add(bp, i), Val: v}), nil
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
				// return b, e: the error code is the third word.
				e, err := l.expr(s.Val2, &p)
				if err != nil {
					return nil, err
				}
				m.Args = []*ir.Node{e}
			}
			return append(p, m), nil
		}
		m := *s
		var err error
		if m.Val, err = l.expr(s.Val, &p); err != nil {
			return nil, err
		}
		if m.Val2, err = l.expr(s.Val2, &p); err != nil {
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
		var cp pre
		cond, err := l.expr(s.Cond, &cp)
		if err != nil {
			return nil, err
		}
		body, err := l.stmts(s.Body)
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
		loop := &ir.Node{ID: s.ID, Op: "while", Cond: name(g.Name),
			Body: append(cp, &ir.Node{Op: "if", Cond: cond, Then: body, Else: []*ir.Node{stop}})}
		return append(p, loop), nil
	}
	// expr, store*, chk: lower the expressions in place.
	m := *s
	var err error
	for _, f := range []**ir.Node{&m.Addr, &m.Val, &m.Val2} {
		if *f == nil {
			continue
		}
		if *f, err = l.expr(*f, &p); err != nil {
			return nil, err
		}
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
