// Package lower is the POC for design D of issue #8: a two-word bytes
// value, implemented by rewriting a checked program so that no bytes is
// left: every bytes local, param, field, and result becomes a pair of
// i64 (p and p#n), b[i] becomes a checked load (the bload op), b[i:j]
// a checked pair, len(b) the length, and "lit" strptr/strlen. The code
// generator keeps its one-word value model; the two-result convention
// (rax:rdx, var2, assign2, return v, e) carries a bytes result.
//
// Not in the POC: (bytes, i64) results (three words), bytes in the
// shipped packages, hoisted checks under && / || (a check hoisted out
// of a short-circuit operand runs unconditionally), while conditions
// that need hoisting (the hoisted statements run once, before the loop).
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
	bfield map[string]bool // "pkg.Type.field" that is bytes (before expansion)
	bfunc  map[string]bool // "pkg.Func" that returns bytes
	scopes []map[string]string
	tmp    int
}

// Program lowers p in place. The error is a POC limit met.
func Program(p *ir.Program) error {
	l := &lowerer{pkgs: map[string]*ir.Package{}, bfield: map[string]bool{}, bfunc: map[string]bool{}}
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
			if pk.Funcs[fi].Result == "bytes" {
				l.bfunc[pk.Path+"."+pk.Funcs[fi].Name] = true
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
		fn.Result, fn.Result2 = "i64", "i64"
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

// typeOf is the checker's type of n, for the shapes the POC needs.
func (l *lowerer) typeOf(n *ir.Node) string {
	switch n.Op {
	case "name":
		if n.Pkg == "" {
			if t := l.lookup(n.Name); t != "" {
				return t
			}
		}
		return "i64"
	case "blit", "bslice", "bmake":
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
		path := n.Pkg
		if path == "" {
			path = l.pkg.Path
		}
		if l.bfunc[path+"."+n.Func] {
			return "bytes"
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

// fieldIsBytes says whether field f of the struct pointer type bt was
// declared bytes (its decl has been expanded by now).
func (l *lowerer) fieldIsBytes(bt, f string) bool {
	return l.bfield[strings.TrimPrefix(bt, "*")+"."+f]
}

func name(s string) *ir.Node { return &ir.Node{Op: "name", Name: s} }

func add(a, b *ir.Node) *ir.Node { return &ir.Node{Op: "add", Left: a, Right: b} }
func sub(a, b *ir.Node) *ir.Node { return &ir.Node{Op: "sub", Left: a, Right: b} }
func one() *ir.Node              { return &ir.Node{Op: "int", Int: 1} }

func simple(n *ir.Node) bool {
	return n.Op == "name" || n.Op == "int" || n.Op == "strlen" || n.Op == "strptr"
}

// hoist makes n a simple operand: itself, or a fresh temp assigned before.
func (l *lowerer) hoist(n *ir.Node, pre *[]*ir.Node) (*ir.Node, error) {
	n, err := l.expr(n, pre)
	if err != nil {
		return nil, err
	}
	if simple(n) {
		return n, nil
	}
	l.tmp++
	t := fmt.Sprintf("t#%d", l.tmp)
	ty := l.typeOf(n)
	l.bind(t, ty)
	*pre = append(*pre, &ir.Node{Op: "var", Name: t, Type: ty, Val: n})
	return name(t), nil
}

// pair lowers a bytes-typed expression to its address and length, both
// simple operands, with the statements that must run first in pre. A
// call is received into a temp pair.
func (l *lowerer) pair(n *ir.Node, pre *[]*ir.Node) (p, ln *ir.Node, err error) {
	switch n.Op {
	case "name":
		return name(n.Name), name(n.Name + "#n"), nil
	case "blit":
		return &ir.Node{Op: "strptr", ValK: 3, Str: n.Str}, &ir.Node{Op: "strlen", ValK: 3, Str: n.Str}, nil
	case "bmake":
		p, err := l.hoist(n.Left, pre)
		if err != nil {
			return nil, nil, err
		}
		ln, err := l.hoist(n.Right, pre)
		return p, ln, err
	case "field":
		base, err := l.hoist(n.Base, pre)
		if err != nil {
			return nil, nil, err
		}
		fp := &ir.Node{Op: "field", Base: base, Name: n.Name}
		fn := &ir.Node{Op: "field", Base: base, Name: n.Name + "#n"}
		p, err := l.hoist(fp, pre)
		if err != nil {
			return nil, nil, err
		}
		ln, err := l.hoist(fn, pre)
		return p, ln, err
	case "bslice":
		bp, bn, err := l.pair(n.Base, pre)
		if err != nil {
			return nil, nil, err
		}
		i, err := l.hoist(n.Left, pre)
		if err != nil {
			return nil, nil, err
		}
		j, err := l.hoist(n.Right, pre)
		if err != nil {
			return nil, nil, err
		}
		// j <= n and i <= j, unsigned, so a negative bound trips too.
		*pre = append(*pre, &ir.Node{Op: "chk", Addr: j, Val: add(bn, one())})
		*pre = append(*pre, &ir.Node{Op: "chk", Addr: i, Val: add(j, one())})
		p, err := l.hoist(add(bp, i), pre)
		if err != nil {
			return nil, nil, err
		}
		ln, err = l.hoist(sub(j, i), pre)
		return p, ln, err
	case "call":
		c, err := l.expr(n, pre)
		if err != nil {
			return nil, nil, err
		}
		l.tmp++
		t := fmt.Sprintf("t#%d", l.tmp)
		l.bind(t, "i64")
		l.bind(t+"#n", "i64")
		*pre = append(*pre, &ir.Node{Op: "var2", Name: t, Type: "i64", Two: &ir.Second{Name: t + "#n", Type: "i64"}, Val: c})
		return name(t), name(t + "#n"), nil
	}
	return nil, nil, fmt.Errorf("POC: cannot lower a bytes %s", n.Op)
}

// expr lowers the bytes operations inside an i64 or bool expression.
func (l *lowerer) expr(n *ir.Node, pre *[]*ir.Node) (*ir.Node, error) {
	if n == nil {
		return nil, nil
	}
	switch n.Op {
	case "bindex":
		bp, bn, err := l.pair(n.Base, pre)
		if err != nil {
			return nil, err
		}
		i, err := l.hoist(n.Arg, pre)
		if err != nil {
			return nil, err
		}
		return &ir.Node{ID: n.ID, Op: "bload", Base: bp, Left: i, Right: bn}, nil
	case "blen":
		_, bn, err := l.pair(n.Base, pre)
		return bn, err
	case "call":
		var args []*ir.Node
		for _, a := range n.Args {
			if l.typeOf(a) == "bytes" {
				p, ln, err := l.pair(a, pre)
				if err != nil {
					return nil, err
				}
				args = append(args, p, ln)
				continue
			}
			a, err := l.expr(a, pre)
			if err != nil {
				return nil, err
			}
			args = append(args, a)
		}
		m := *n
		m.Args = args
		return &m, nil
	}
	m := *n
	var err error
	for _, f := range []**ir.Node{&m.Left, &m.Right, &m.Arg, &m.Base, &m.Addr, &m.Val, &m.Val2, &m.Cond} {
		if *f == nil {
			continue
		}
		if *f, err = l.expr(*f, pre); err != nil {
			return nil, err
		}
	}
	return &m, nil
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
		out = append(out, lowered...)
	}
	return out, nil
}

func (l *lowerer) stmt(s *ir.Node) ([]*ir.Node, error) {
	var pre []*ir.Node
	switch s.Op {
	case "var":
		if s.Type == "bytes" {
			if s.Val.Op == "call" {
				c, err := l.expr(s.Val, &pre)
				if err != nil {
					return nil, err
				}
				l.bind(s.Name, "bytes")
				return append(pre, &ir.Node{ID: s.ID, Op: "var2", Name: s.Name, Type: "i64", Two: &ir.Second{Name: s.Name + "#n", Type: "i64"}, Val: c}), nil
			}
			p, ln, err := l.pair(s.Val, &pre)
			if err != nil {
				return nil, err
			}
			l.bind(s.Name, "bytes")
			return append(pre, &ir.Node{ID: s.ID, Op: "var", Name: s.Name, Type: "i64", Val: p},
				&ir.Node{Op: "var", Name: s.Name + "#n", Type: "i64", Val: ln}), nil
		}
		v, err := l.expr(s.Val, &pre)
		if err != nil {
			return nil, err
		}
		l.bind(s.Name, s.Type)
		m := *s
		m.Val = v
		return append(pre, &m), nil
	case "var2":
		v, err := l.expr(s.Val, &pre)
		if err != nil {
			return nil, err
		}
		l.bind(s.Name, s.Type)
		l.bind(s.Two.Name, s.Two.Type)
		m := *s
		m.Val = v
		return append(pre, &m), nil
	case "assign":
		if l.lookup(s.Name) == "bytes" {
			if s.Val.Op == "call" {
				c, err := l.expr(s.Val, &pre)
				if err != nil {
					return nil, err
				}
				return append(pre, &ir.Node{ID: s.ID, Op: "assign2", Name: s.Name, Two: &ir.Second{Name: s.Name + "#n"}, Val: c}), nil
			}
			p, ln, err := l.pair(s.Val, &pre)
			if err != nil {
				return nil, err
			}
			return append(pre, &ir.Node{ID: s.ID, Op: "assign", Name: s.Name, Val: p},
				&ir.Node{Op: "assign", Name: s.Name + "#n", Val: ln}), nil
		}
		v, err := l.expr(s.Val, &pre)
		if err != nil {
			return nil, err
		}
		m := *s
		m.Val = v
		return append(pre, &m), nil
	case "setfield":
		if l.fieldIsBytes(l.typeOf(s.Base), s.Name) {
			base, err := l.hoist(s.Base, &pre)
			if err != nil {
				return nil, err
			}
			p, ln, err := l.pair(s.Val, &pre)
			if err != nil {
				return nil, err
			}
			return append(pre, &ir.Node{ID: s.ID, Op: "setfield", Base: base, Name: s.Name, Val: p},
				&ir.Node{Op: "setfield", Base: base, Name: s.Name + "#n", Val: ln}), nil
		}
		base, err := l.expr(s.Base, &pre)
		if err != nil {
			return nil, err
		}
		v, err := l.expr(s.Val, &pre)
		if err != nil {
			return nil, err
		}
		m := *s
		m.Base, m.Val = base, v
		return append(pre, &m), nil
	case "bstore":
		ix := s.Base
		bp, bn, err := l.pair(ix.Base, &pre)
		if err != nil {
			return nil, err
		}
		i, err := l.hoist(ix.Arg, &pre)
		if err != nil {
			return nil, err
		}
		v, err := l.expr(s.Val, &pre)
		if err != nil {
			return nil, err
		}
		return append(pre, &ir.Node{Op: "chk", Addr: i, Val: bn},
			&ir.Node{ID: s.ID, Op: "store8", Addr: add(bp, i), Val: v}), nil
	case "return":
		if s.Val != nil && s.Val2 == nil && l.typeOf(s.Val) == "bytes" {
			if s.Val.Op == "call" {
				// Forwarded: the callee's rax:rdx are this func's.
				c, err := l.expr(s.Val, &pre)
				if err != nil {
					return nil, err
				}
				return append(pre, &ir.Node{ID: s.ID, Op: "return", Val: c}), nil
			}
			p, ln, err := l.pair(s.Val, &pre)
			if err != nil {
				return nil, err
			}
			return append(pre, &ir.Node{ID: s.ID, Op: "return", Val: p, Val2: ln}), nil
		}
		m := *s
		var err error
		if m.Val, err = l.expr(s.Val, &pre); err != nil {
			return nil, err
		}
		if m.Val2, err = l.expr(s.Val2, &pre); err != nil {
			return nil, err
		}
		return append(pre, &m), nil
	case "if":
		cond, err := l.expr(s.Cond, &pre)
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
		return append(pre, &m), nil
	case "while":
		cond, err := l.expr(s.Cond, &pre)
		if err != nil {
			return nil, err
		}
		if len(pre) > 0 {
			return nil, fmt.Errorf("POC: a while condition that needs a hoisted statement")
		}
		body, err := l.stmts(s.Body)
		if err != nil {
			return nil, err
		}
		m := *s
		m.Cond, m.Body = cond, body
		return []*ir.Node{&m}, nil
	}
	// expr, store*, chk, assign2: lower the expressions in place.
	m := *s
	var err error
	for _, f := range []**ir.Node{&m.Addr, &m.Val, &m.Val2} {
		if *f == nil {
			continue
		}
		if *f, err = l.expr(*f, &pre); err != nil {
			return nil, err
		}
	}
	if strings.HasPrefix(m.Op, "store") || m.Op == "chk" {
		for i := range m.Args {
			if m.Args[i], err = l.expr(m.Args[i], &pre); err != nil {
				return nil, err
			}
		}
	}
	return append(pre, &m), nil
}
