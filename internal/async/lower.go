// Package async lowers async funcs to plain Ovid IR, between the checker and
// code generation (POC A of #75), so neither backend knows about them.
//
// An async func F becomes a resume function F$resume(fr i64) i64 over a
// frame on the heap: an ovid/async.Frame head, then one 8-byte slot per
// parameter and local. Its body is split at each await into numbered
// blocks, run by a loop over the frame's state:
//
//	func F$resume($fr i64) i64 {
//	  var x$0 i64 = load64($fr + 72)          // every local, from its slot
//	  var $st i64 = load64($fr + 8)
//	  while true {
//	    if $st == 0 { ...; $st = 1 } else { if $st == 1 { ... } }
//	  }
//	}
//
// An await stores every local back, records the next block, and returns 0
// to the scheduler; return stores the result and returns 1. Locals that were
// scoped to a block become function-wide, renamed name$slot. Calls are
// frames: `await G(a)` allocates G's frame, stores a in it, and runs it at
// once with ovid/async.Start; only if G waits does F wait too, with its
// frame as G's waiter. `spawn G(a)` enqueues the frame and yields it as the
// task. Ovid has no function values, so the scheduler reaches a resume
// function through ovid/async.dispatch, whose body is written here: one
// branch per async func, by the number stored in the frame.
//
// Three names of ovid/async are intrinsics: Self() is the frame, await
// Park() returns to the scheduler unconditionally, and dispatch.
package async

import (
	"fmt"

	"ovid/internal/check"
	"ovid/internal/ir"
)

const rt = "ovid/async"

// Lower returns p with its async funcs lowered, or p itself when it has
// none. p is not modified.
func Lower(p *ir.Program) (*ir.Program, error) {
	l := &lowerer{prog: p, pkgs: map[string]*ir.Package{}, funcs: map[string]*fnInfo{}}
	var order []*fnInfo
	for i := range p.Packages {
		pkg := &p.Packages[i]
		l.pkgs[pkg.Path] = pkg
		for fi := range pkg.Funcs {
			fn := &pkg.Funcs[fi]
			if !fn.Async || (pkg.Path == rt && fn.Name == "Park") {
				continue
			}
			f := &fnInfo{pkg: pkg, fn: fn, id: len(order) + 1}
			l.funcs[pkg.Path+"."+fn.Name] = f
			order = append(order, f)
		}
	}
	if len(order) == 0 {
		return p, nil
	}
	if l.pkgs[rt] == nil {
		return nil, fmt.Errorf("async funcs need package %s: import it", rt)
	}
	if err := l.frameLayout(); err != nil {
		return nil, err
	}
	for _, f := range order {
		f.bind()
	}

	q := *p
	q.Packages = append([]ir.Package{}, p.Packages...)
	for i := range q.Packages {
		pkg := &q.Packages[i]
		var funcs []ir.Func
		for _, fn := range pkg.Funcs {
			if !fn.Async {
				if pkg.Path == rt && fn.Name == "dispatch" {
					fn.Body = l.dispatch(order)
				}
				funcs = append(funcs, fn)
				continue
			}
			f := l.funcs[pkg.Path+"."+fn.Name]
			if f == nil {
				continue // Park
			}
			res, err := l.resume(f)
			if err != nil {
				return nil, err
			}
			funcs = append(funcs, res)
			if pkg.Path == p.Entry && fn.Name == "main" {
				funcs = append(funcs, l.syncMain(f))
			}
		}
		pkg.Funcs = funcs
	}
	return &q, nil
}

type lowerer struct {
	prog  *ir.Program
	pkgs  map[string]*ir.Package
	funcs map[string]*fnInfo
	off   map[string]int64 // ovid/async.Frame's fields
	head  int64            // its size: where the slots start
}

// fnInfo is an async func and its frame: one slot per param, then one per
// local declaration, each with its own name.
type fnInfo struct {
	pkg   *ir.Package
	fn    *ir.Func
	id    int
	slots []slot
	ref   map[*ir.Node]int // name, var, assign, var2, assign2 -> slot
	ref2  map[*ir.Node]int // var2, assign2 -> the second name's slot
}

type slot struct {
	name string // the renamed local
	typ  string // as written, in the func's package
}

func (l *lowerer) frameLayout() error {
	pkg := l.pkgs[rt]
	for _, t := range pkg.Types {
		if t.Name != "Frame" {
			continue
		}
		l.off = map[string]int64{}
		for i, f := range t.Fields {
			l.off[f.Name] = int64(8 * i)
		}
		l.head = int64(8 * len(t.Fields))
		for _, need := range []string{"fn", "st", "res", "waiter", "cap", "arg"} {
			if _, ok := l.off[need]; !ok {
				return fmt.Errorf("%s.Frame has no field %s", rt, need)
			}
		}
		return nil
	}
	return fmt.Errorf("%s has no type Frame", rt)
}

// size is the frame size of f: the head, its slots, and the child slot.
func (l *lowerer) size(f *fnInfo) int64 { return l.head + int64(8*(len(f.slots)+1)) }

func (l *lowerer) slotOff(i int) int64 { return l.head + int64(8*i) }

// bind gives every param and local declaration of f a slot, and resolves
// each use of a name to it, scoped as the checker scopes.
func (f *fnInfo) bind() {
	f.ref = map[*ir.Node]int{}
	f.ref2 = map[*ir.Node]int{}
	var scope []int
	declare := func(s *ir.Node, name, typ string) int {
		i := len(f.slots)
		f.slots = append(f.slots, slot{fmt.Sprintf("%s$%d", name, i), typ})
		scope = append(scope, i)
		if s != nil {
			f.ref[s] = i
		}
		return i
	}
	lookup := func(name string) (int, bool) {
		for j := len(scope) - 1; j >= 0; j-- {
			if f.slots[scope[j]].name == fmt.Sprintf("%s$%d", name, scope[j]) {
				return scope[j], true
			}
		}
		return 0, false
	}
	var expr func(n *ir.Node)
	expr = func(n *ir.Node) {
		if n == nil {
			return
		}
		if n.Op == "name" && n.Pkg == "" {
			if i, ok := lookup(n.Name); ok {
				f.ref[n] = i
			}
		}
		for _, c := range []*ir.Node{n.Left, n.Right, n.Arg, n.Base} {
			expr(c)
		}
		for _, a := range n.Args {
			expr(a)
		}
	}
	var stmts func(ss []*ir.Node)
	stmts = func(ss []*ir.Node) {
		mark := len(scope)
		for _, s := range ss {
			if s == nil {
				continue
			}
			for _, c := range []*ir.Node{s.Val, s.Val2, s.Base, s.Addr, s.Cond} {
				expr(c)
			}
			switch s.Op {
			case "var":
				declare(s, s.Name, s.Type)
			case "var2":
				if s.Name != "_" {
					declare(s, s.Name, s.Type)
				}
				if s.Two.Name != "_" {
					f.ref2[s] = declare(nil, s.Two.Name, s.Two.Type)
				}
			case "assign":
				if i, ok := lookup(s.Name); ok {
					f.ref[s] = i
				}
			case "assign2":
				if i, ok := lookup(s.Name); ok {
					f.ref[s] = i
				}
				if i, ok := lookup(s.Two.Name); ok {
					f.ref2[s] = i
				}
			}
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
		}
		scope = scope[:mark]
	}
	for _, pa := range f.fn.Params {
		declare(nil, pa.Name, pa.Type)
	}
	stmts(f.fn.Body)
}

// Node builders.

func name(n string) *ir.Node { return &ir.Node{Op: "name", Name: n} }
func num(v int64) *ir.Node   { return &ir.Node{Op: "int", Int: v, ValK: 1} }
func add(a, b *ir.Node) *ir.Node {
	return &ir.Node{Op: "add", Left: a, Right: b}
}
func at(base string, off int64) *ir.Node { return add(name(base), num(off)) }
func load(base string, off int64) *ir.Node {
	return &ir.Node{Op: "load64", Arg: at(base, off)}
}
func store(base string, off int64, v *ir.Node) *ir.Node {
	return &ir.Node{Op: "store64", Addr: at(base, off), Val: v}
}
func assign(n string, v *ir.Node) *ir.Node { return &ir.Node{Op: "assign", Name: n, Val: v} }
func call(pkg, fn string, args ...*ir.Node) *ir.Node {
	return &ir.Node{Op: "call", Pkg: pkg, Func: fn, Args: args}
}
func ret(v *ir.Node) *ir.Node { return &ir.Node{Op: "return", Val: v} }
func eq(a, b *ir.Node) *ir.Node {
	return &ir.Node{Op: "eq", Left: a, Right: b}
}

const (
	fr    = "$fr"
	st    = "$st"
	child = "$c"
)

// resume is the resume function of f.
func (l *lowerer) resume(f *fnInfo) (ir.Func, error) {
	b := &builder{l: l, f: f}
	b.cur = b.newBlock()
	if err := b.list(f.fn.Body); err != nil {
		return ir.Func{}, err
	}
	// Every path returns (the checker saw to it); a block that falls off
	// the end anyway finishes with 0 rather than spinning.
	b.emit(store(fr, l.off["res"], num(0)), ret(num(1)))

	var body []*ir.Node
	for i, s := range f.slots {
		var v *ir.Node = load(fr, l.slotOff(i))
		if s.typ != "i64" && s.typ != "bool" {
			v = &ir.Node{Op: "cast", Type: s.typ, Arg: v}
		}
		body = append(body, &ir.Node{Op: "var", Name: s.name, Type: s.typ, Val: v})
	}
	body = append(body,
		&ir.Node{Op: "var", Name: child, Type: "i64", Val: load(fr, l.slotOff(len(f.slots)))},
		&ir.Node{Op: "var", Name: st, Type: "i64", Val: load(fr, l.off["st"])})
	// The dispatch chain: if $st == 0 {...} else { if $st == 1 {...} ... }
	var chain []*ir.Node
	for i := len(b.blocks) - 1; i >= 0; i-- {
		n := &ir.Node{Op: "if", Cond: eq(name(st), num(int64(i))), Then: b.blocks[i], Else: chain}
		chain = []*ir.Node{n}
	}
	body = append(body, &ir.Node{Op: "while", Cond: &ir.Node{Op: "bool", Bool: true, ValK: 2}, Body: chain}, ret(num(1)))
	return ir.Func{
		ID:     f.fn.ID + "$resume",
		Name:   f.fn.Name + "$resume",
		Params: []ir.Param{{Name: fr, Type: "i64"}},
		Result: "i64",
		Body:   body,
	}, nil
}

// syncMain is the main that runs the async main f: a scheduler, f's frame
// with io in its first slot, and Run.
func (l *lowerer) syncMain(f *fnInfo) ir.Func {
	io := f.fn.Params[0].Name
	return ir.Func{
		ID:     f.fn.ID,
		Name:   "main",
		Params: []ir.Param{{Name: io, Type: f.fn.Params[0].Type}},
		Result: "i64",
		Body: []*ir.Node{
			{Op: "var", Name: "$rt", Type: "i64", Val: call(rt, "New", name(io))},
			{Op: "var", Name: child, Type: "i64", Val: call(rt, "NewRoot", name("$rt"), num(l.size(f)), num(int64(f.id)))},
			store(child, l.slotOff(0), name(io)),
			ret(call(rt, "Run", name("$rt"), name(child))),
		},
	}
}

// dispatch is ovid/async.dispatch's body: the resume function of the frame
// fr by its number.
func (l *lowerer) dispatch(order []*fnInfo) []*ir.Node {
	param := "fr"
	for i := range l.pkgs[rt].Funcs {
		if fn := l.pkgs[rt].Funcs[i]; fn.Name == "dispatch" && len(fn.Params) == 1 {
			param = fn.Params[0].Name
		}
	}
	body := []*ir.Node{{Op: "var", Name: "$id", Type: "i64", Val: load(param, l.off["fn"])}}
	for _, f := range order {
		body = append(body, &ir.Node{Op: "if", Cond: eq(name("$id"), num(int64(f.id))),
			Then: []*ir.Node{ret(call(f.pkg.Path, f.fn.Name+"$resume", name(param)))}})
	}
	return append(body, ret(num(1)))
}

// builder splits one async func's body into blocks.
type builder struct {
	l      *lowerer
	f      *fnInfo
	blocks [][]*ir.Node
	cur    int
}

func (b *builder) newBlock() int {
	b.blocks = append(b.blocks, nil)
	return len(b.blocks) - 1
}

func (b *builder) emit(ns ...*ir.Node) { b.blocks[b.cur] = append(b.blocks[b.cur], ns...) }

func (b *builder) goTo(k int) *ir.Node { return assign(st, num(int64(k))) }

// suspend is what an await that waits does: every local back into its
// slot, the block to resume at, and a return to the scheduler.
func (b *builder) suspend(k int) []*ir.Node {
	var ns []*ir.Node
	for i, s := range b.f.slots {
		ns = append(ns, store(fr, b.l.slotOff(i), name(s.name)))
	}
	ns = append(ns, store(fr, b.l.slotOff(len(b.f.slots)), name(child)),
		store(fr, b.l.off["st"], num(int64(k))), ret(num(0)))
	return ns
}

func hasAwait(n *ir.Node) bool {
	found := false
	n.Walk(func(c *ir.Node) {
		if c.Op == "await" {
			found = true
		}
	})
	return found
}

func (b *builder) list(ss []*ir.Node) error {
	for _, s := range ss {
		if s == nil {
			continue
		}
		if !hasAwait(s) {
			ns, err := b.stmt(s)
			if err != nil {
				return err
			}
			b.emit(ns...)
			continue
		}
		switch s.Op {
		case "var", "assign", "expr":
			if s.Val == nil || s.Val.Op != "await" {
				return fmt.Errorf("%s: await must be the whole value of a statement", s.ID)
			}
			v, err := b.await(s.Val.Arg)
			if err != nil {
				return err
			}
			if s.Op != "expr" {
				b.emit(assign(b.f.slots[b.f.ref[s]].name, v))
			}
		case "if":
			then := b.newBlock()
			els := -1
			if len(s.Else) > 0 {
				els = b.newBlock()
			}
			join := b.newBlock()
			no := join
			if els >= 0 {
				no = els
			}
			b.emit(&ir.Node{Op: "if", Cond: b.expr(s.Cond), Then: []*ir.Node{b.goTo(then)}, Else: []*ir.Node{b.goTo(no)}})
			b.cur = then
			if err := b.list(s.Then); err != nil {
				return err
			}
			b.emit(b.goTo(join))
			if els >= 0 {
				b.cur = els
				if err := b.list(s.Else); err != nil {
					return err
				}
				b.emit(b.goTo(join))
			}
			b.cur = join
		case "while":
			head, body, exit := b.newBlock(), b.newBlock(), b.newBlock()
			b.emit(b.goTo(head))
			b.cur = head
			b.emit(&ir.Node{Op: "if", Cond: b.expr(s.Cond), Then: []*ir.Node{b.goTo(body)}, Else: []*ir.Node{b.goTo(exit)}})
			b.cur = body
			if err := b.list(s.Body); err != nil {
				return err
			}
			b.emit(b.goTo(head))
			b.cur = exit
		default:
			return fmt.Errorf("%s: await in a %s", s.ID, s.Op)
		}
	}
	return nil
}

// await emits the call c awaited, in the current block, and starts the block
// after it; the result is the expression for the call's value there.
func (b *builder) await(c *ir.Node) (*ir.Node, error) {
	path := c.Pkg
	if path == "" {
		path = b.f.pkg.Path
	}
	if path == rt && c.Func == "Park" {
		next := b.newBlock()
		b.emit(b.suspend(next)...)
		b.cur = next
		return load(fr, b.l.off["arg"]), nil
	}
	if err := b.newFrame(c, path); err != nil {
		return nil, err
	}
	next := b.newBlock()
	wait := append([]*ir.Node{store(child, b.l.off["waiter"], name(fr))}, b.suspend(next)...)
	b.emit(&ir.Node{Op: "if", Cond: &ir.Node{Op: "not", Arg: call(rt, "Start", name(child))}, Then: wait}, b.goTo(next))
	b.cur = next
	return load(child, b.l.off["res"]), nil
}

// newFrame emits $c = a frame for the call c to an async func, its
// arguments stored in its slots. A callee whose first parameter is an
// *ovid/io.Cap gets its frame from that heap; any other, from the caller's.
func (b *builder) newFrame(c *ir.Node, path string) error {
	g := b.l.funcs[path+"."+c.Func]
	if g == nil {
		return fmt.Errorf("%s: %s.%s is not an async func", c.ID, path, c.Func)
	}
	size, id := num(b.l.size(g)), num(int64(g.id))
	args := c.Args
	if len(g.fn.Params) > 0 && len(args) > 0 && b.l.isCap(g, 0) {
		b.emit(assign(child, call(rt, "NewFrameIn", name(fr), b.expr(args[0]), size, id)),
			store(child, b.l.slotOff(0), load(child, b.l.off["cap"])))
		args = args[1:]
		for i, a := range args {
			b.emit(store(child, b.l.slotOff(i+1), b.expr(a)))
		}
		return nil
	}
	b.emit(assign(child, call(rt, "NewFrame", name(fr), size, id)))
	for i, a := range args {
		b.emit(store(child, b.l.slotOff(i), b.expr(a)))
	}
	return nil
}

func (l *lowerer) isCap(g *fnInfo, i int) bool {
	t, err := check.Resolve(g.pkg, g.fn.Params[i].Type, l.pkgs)
	return err == nil && t == "*ovid/io.Cap"
}

// stmt rewrites a statement with no await in it: locals renamed, var as an
// assignment, return as storing the result, spawn as a frame enqueued.
func (b *builder) stmt(s *ir.Node) ([]*ir.Node, error) {
	switch s.Op {
	case "var", "assign":
		target := b.f.slots[b.f.ref[s]].name
		if s.Val == nil {
			return []*ir.Node{assign(target, num(0))}, nil
		}
		if s.Val.Op == "spawn" {
			if err := b.spawn(s.Val.Arg); err != nil {
				return nil, err
			}
			return []*ir.Node{assign(target, name(child))}, nil
		}
		return []*ir.Node{assign(target, b.expr(s.Val))}, nil
	case "var2", "assign2":
		n := &ir.Node{Op: "assign2", Name: "_", Two: &ir.Second{Name: "_"}, Val: b.expr(s.Val)}
		if i, ok := b.f.ref[s]; ok {
			n.Name = b.f.slots[i].name
		}
		if i, ok := b.f.ref2[s]; ok {
			n.Two.Name = b.f.slots[i].name
		}
		return []*ir.Node{n}, nil
	case "expr":
		if s.Val.Op == "spawn" {
			return nil, b.spawn(s.Val.Arg)
		}
		return []*ir.Node{{Op: "expr", Val: b.expr(s.Val)}}, nil
	case "setfield":
		return []*ir.Node{{Op: "setfield", Base: b.expr(s.Base), Name: s.Name, Val: b.expr(s.Val)}}, nil
	case "store8", "store16", "store32", "store64":
		return []*ir.Node{{Op: s.Op, Addr: b.expr(s.Addr), Val: b.expr(s.Val)}}, nil
	case "return":
		v := num(0)
		if s.Val != nil {
			v = b.expr(s.Val)
		}
		return []*ir.Node{store(fr, b.l.off["res"], v), ret(num(1))}, nil
	case "if":
		th, err := b.stmts(s.Then)
		if err != nil {
			return nil, err
		}
		el, err := b.stmts(s.Else)
		if err != nil {
			return nil, err
		}
		return []*ir.Node{{Op: "if", Cond: b.expr(s.Cond), Then: th, Else: el}}, nil
	case "while":
		body, err := b.stmts(s.Body)
		if err != nil {
			return nil, err
		}
		return []*ir.Node{{Op: "while", Cond: b.expr(s.Cond), Body: body}}, nil
	}
	return nil, fmt.Errorf("%s: statement %s", s.ID, s.Op)
}

// stmts rewrites a nested block with no await in it. A spawn there emits
// into the current block, so it is collected separately.
func (b *builder) stmts(ss []*ir.Node) ([]*ir.Node, error) {
	saved := b.cur
	tmp := b.newBlock()
	b.cur = tmp
	for _, s := range ss {
		if s == nil {
			continue
		}
		ns, err := b.stmt(s)
		if err != nil {
			return nil, err
		}
		b.emit(ns...)
	}
	out := b.blocks[tmp]
	b.blocks = b.blocks[:tmp]
	b.cur = saved
	return out, nil
}

// spawn emits $c = the frame of the call c, enqueued to run.
func (b *builder) spawn(c *ir.Node) error {
	path := c.Pkg
	if path == "" {
		path = b.f.pkg.Path
	}
	if err := b.newFrame(c, path); err != nil {
		return err
	}
	b.emit(&ir.Node{Op: "expr", Val: call(rt, "Enqueue", name(child))})
	return nil
}

// expr copies an expression with its locals renamed and Self() as the frame.
func (b *builder) expr(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	if n.Op == "call" && n.Func == "Self" && len(n.Args) == 0 && (n.Pkg == rt || (n.Pkg == "" && b.f.pkg.Path == rt)) {
		return name(fr)
	}
	c := *n
	c.ID = ""
	if n.Op == "name" && n.Pkg == "" {
		if i, ok := b.f.ref[n]; ok {
			c.Name = b.f.slots[i].name
		}
	}
	c.Left, c.Right, c.Arg, c.Base = b.expr(n.Left), b.expr(n.Right), b.expr(n.Arg), b.expr(n.Base)
	if n.Args != nil {
		c.Args = make([]*ir.Node, len(n.Args))
		for i, a := range n.Args {
			c.Args[i] = b.expr(a)
		}
	}
	return &c
}
