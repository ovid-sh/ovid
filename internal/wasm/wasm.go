// Package wasm compiles an Ovid program to a WebAssembly module, a proof of
// concept beside the x86-64 backend in internal/compile.
//
// Every Ovid value is an i64 in a wasm local; a pointer is a linear-memory
// address held in an i64 and wrapped to i32 at each load and store. syscall
// is the module's one import, env.syscall(n, a1..a6) -> i64, so the host
// decides what read, write, mmap, and exit mean: on a Cloudflare Worker,
// standard input is the HTTP request and standard output the response.
//
// The module exports its memory and _start(argc, argv) -> i64, which maps
// the heap with the host's mmap, fills in an ovid/io.Cap, calls main, and
// passes its result to exit (syscall 60) before returning it. argv is the
// address of argc pointers to NUL-terminated strings the host wrote into
// memory (it may grow memory for them), or 0 with argc 0.
package wasm

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"

	"ovid/internal/ir"
)

// HeapSize is the first heap region _start maps. It is smaller than the
// native 128 MiB: a Worker isolate has 128 MiB in all. OVID_WASM_HEAP (a
// byte count) overrides it, for the async experiments (#75, POC D).
var HeapSize int64 = heapSize()

func heapSize() int64 {
	if v, err := strconv.ParseInt(os.Getenv("OVID_WASM_HEAP"), 10, 64); err == nil && v > 0 {
		return v
	}
	return 32 << 20
}

// roBase is where rodata starts. The bytes below it stay zero, so a load
// through a null pointer reads zeros rather than rodata.
const roBase = 1024

// Function indices: the syscall import, then the runtime's own funcs, then
// the program's.
const (
	fnSyscall = iota
	fnStart
	fnUmulhi
	fnBswap64
	fnFirst
)

// Compile emits a wasm module for p.
func Compile(p *ir.Program) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("nil program")
	}
	c := &cg{
		prog:   p,
		strs:   map[string]int{},
		tables: map[string]tableRef{},
		funcs:  map[string]int{},
		sigs:   map[string]sig{},
		pkgs:   map[string]*ir.Package{},
	}
	mainKey := p.Entry + ".main"
	live := reachable(p, mainKey)
	type item struct {
		pkg *ir.Package
		fn  *ir.Func
	}
	var order []item
	for i := range p.Packages {
		pkg := &p.Packages[i]
		c.pkgs[pkg.Path] = pkg
	}
	for i := range p.Packages {
		pkg := &p.Packages[i]
		for fi := range pkg.Funcs {
			fn := &pkg.Funcs[fi]
			key := pkg.Path + "." + fn.Name
			var ps []string
			for _, pa := range fn.Params {
				ps = append(ps, c.resolve(pkg, pa.Type))
			}
			nres := 1
			if fn.Result2 != "" {
				nres = 2
			}
			c.sigs[key] = sig{params: ps, result: c.resolve(pkg, fn.Result), nres: nres}
			if live[key] {
				c.funcs[key] = fnFirst + len(order)
				order = append(order, item{pkg, fn})
			}
		}
	}
	if _, ok := c.funcs[mainKey]; !ok {
		return nil, fmt.Errorf("missing %s", mainKey)
	}

	tSys := c.typeIdx(7, 1)
	funcTypes := []int{c.typeIdx(2, 1), c.typeIdx(2, 1), c.typeIdx(1, 1)}
	var bodies [][]byte
	oom := c.intern("out of memory\n")
	for _, it := range order {
		body, err := c.emitFunc(it.pkg, it.fn)
		if err != nil {
			return nil, err
		}
		s := c.sigs[it.pkg.Path+"."+it.fn.Name]
		funcTypes = append(funcTypes, c.typeIdx(len(s.params), s.nres))
		bodies = append(bodies, body)
	}
	// Rodata is final: the Cap goes after it.
	capAddr := (roBase + len(c.ro) + 7) &^ 7
	start, err := c.startup(c.funcs[mainKey], int64(capAddr), int64(roBase+oom))
	if err != nil {
		return nil, err
	}
	bodies = append([][]byte{start, umulhiBody(), bswap64Body()}, bodies...)

	m := []byte{0x00, 0x61, 0x73, 0x6d, 1, 0, 0, 0}
	var ts []byte
	ts = uleb(ts, uint64(len(c.types)))
	for _, t := range c.types {
		ts = append(ts, 0x60)
		ts = uleb(ts, uint64(t[0]))
		for i := 0; i < t[0]; i++ {
			ts = append(ts, i64)
		}
		ts = uleb(ts, uint64(t[1]))
		for i := 0; i < t[1]; i++ {
			ts = append(ts, i64)
		}
	}
	m = section(m, 1, ts)
	var is []byte
	is = uleb(is, 1)
	is = name(is, "env")
	is = name(is, "syscall")
	is = append(is, 0x00)
	is = uleb(is, uint64(tSys))
	m = section(m, 2, is)
	var fs []byte
	fs = uleb(fs, uint64(len(funcTypes)))
	for _, t := range funcTypes {
		fs = uleb(fs, uint64(t))
	}
	m = section(m, 3, fs)
	// Enough pages for rodata and the Cap; the heap is mapped at run time
	// by the host growing memory.
	pages := (capAddr + 64 + 0xffff) / 0x10000
	m = section(m, 5, uleb([]byte{1, 0x00}, uint64(pages)))
	var es []byte
	es = uleb(es, 2)
	es = name(es, "memory")
	es = append(es, 0x02, 0)
	es = name(es, "_start")
	es = append(es, 0x00)
	es = uleb(es, fnStart)
	m = section(m, 7, es)
	var cs []byte
	cs = uleb(cs, uint64(len(bodies)))
	for _, b := range bodies {
		cs = uleb(cs, uint64(len(b)))
		cs = append(cs, b...)
	}
	m = section(m, 10, cs)
	var ds []byte
	ds = uleb(ds, 1)
	ds = append(ds, 0x00, opI32Const)
	ds = sleb(ds, roBase)
	ds = append(ds, opEnd)
	ds = uleb(ds, uint64(len(c.ro)))
	ds = append(ds, c.ro...)
	m = section(m, 11, ds)
	return m, nil
}

type sig struct {
	params []string
	result string
	nres   int
}

type tableRef struct{ off, n int }

type cg struct {
	prog   *ir.Program
	pkgs   map[string]*ir.Package
	funcs  map[string]int
	sigs   map[string]sig
	types  [][2]int
	ro     []byte
	strs   map[string]int
	tables map[string]tableRef

	// The func being compiled.
	pkg     *ir.Package
	fn      *ir.Func
	consts  map[string]int64
	locals  []local
	ref     map[*ir.Node]int
	ref2    map[*ir.Node]int
	scope   []int
	scratch int // a local for an index's bounds check
	b       []byte
}

type local struct {
	name string
	typ  string
}

func (c *cg) typeIdx(params, results int) int {
	for i, t := range c.types {
		if t == [2]int{params, results} {
			return i
		}
	}
	c.types = append(c.types, [2]int{params, results})
	return len(c.types) - 1
}

func (c *cg) intern(s string) int {
	if off, ok := c.strs[s]; ok {
		return off
	}
	off := len(c.ro)
	c.ro = append(c.ro, s...)
	c.ro = append(c.ro, 0)
	c.strs[s] = off
	return off
}

func (c *cg) table(n *ir.Node) (int, int, error) {
	path := n.Pkg
	if path == "" {
		path = c.pkg.Path
	}
	key := path + "." + n.Name
	if t, ok := c.tables[key]; ok {
		return t.off, t.n, nil
	}
	if pkg := c.pkgs[path]; pkg != nil {
		for i := range pkg.Consts {
			cn := &pkg.Consts[i]
			if cn.Name != n.Name || !cn.Table {
				continue
			}
			for len(c.ro)%8 != 0 {
				c.ro = append(c.ro, 0)
			}
			off := len(c.ro)
			for _, v := range cn.Values {
				c.ro = binary.LittleEndian.AppendUint64(c.ro, uint64(v))
			}
			c.tables[key] = tableRef{off, len(cn.Values)}
			return off, len(cn.Values), nil
		}
	}
	return 0, 0, fmt.Errorf("table %s", key)
}

// startup is _start's body: map the heap, fill in the Cap at capAddr, call
// main, exit with its result. A refused heap writes "out of memory" (at
// oom) to standard error and exits 71, as the native startup does.
func (c *cg) startup(mainIdx int, capAddr, oom int64) ([]byte, error) {
	var offs [6]uint64
	for i, f := range []string{"argc", "argv", "heap", "used", "size", "maps"} {
		o, err := capOff(c.prog, f)
		if err != nil {
			return nil, err
		}
		offs[i] = uint64(o)
	}
	// Params 0 argc, 1 argv; locals 2 the heap, 3 main's result.
	c.b = []byte{1, 2, i64}
	sys := func(n int64, args ...int64) {
		c.i64(n)
		for i := 0; i < 6; i++ {
			var a int64
			if i < len(args) {
				a = args[i]
			}
			c.i64(a)
		}
		c.op(opCall)
		c.b = uleb(c.b, fnSyscall)
	}
	sys(9, 0, HeapSize, 3, 0x4022, -1, 0)
	c.op(opLocalTee, 2)
	c.i64(0)
	c.op(opI64LtS, opIf, blockVoid)
	sys(1, 2, oom, 14)
	c.op(opDrop)
	sys(60, 71)
	c.op(opDrop, opUnreachable, opEnd)
	for i, off := range offs {
		c.i64(capAddr)
		c.op(opI32WrapI64)
		switch i {
		case 0, 1:
			c.op(opLocalGet, byte(i))
		case 2:
			c.op(opLocalGet, 2)
		case 4:
			c.i64(HeapSize)
		default:
			c.i64(0)
		}
		c.mem(opI64Store, off)
	}
	c.i64(capAddr)
	c.op(opCall)
	c.b = uleb(c.b, uint64(mainIdx))
	c.op(opLocalSet, 3)
	c.i64(60)
	c.op(opLocalGet, 3)
	for i := 0; i < 5; i++ {
		c.i64(0)
	}
	c.op(opCall)
	c.b = uleb(c.b, fnSyscall)
	c.op(opDrop, opLocalGet, 3, opEnd)
	return c.b, nil
}

func capOff(p *ir.Program, name string) (int, error) {
	for _, pkg := range p.Packages {
		if pkg.Path != "ovid/io" {
			continue
		}
		for _, t := range pkg.Types {
			if t.Name != "Cap" {
				continue
			}
			for i, f := range t.Fields {
				if f.Name == name {
					return i * 8, nil
				}
			}
			return 0, fmt.Errorf("ovid/io.Cap missing %s", name)
		}
	}
	return 0, fmt.Errorf("missing ovid/io.Cap")
}

// umulhiBody is the high 64 bits of the unsigned product of params 0 and
// 1, from 32-bit halves (wasm has no wide multiply). Locals 2..5 are
// alo, ahi, blo, bhi; 6 is t, 7 is u.
func umulhiBody() []byte {
	c := &cg{b: []byte{1, 6, i64}}
	half := func(src, lo, hi byte) {
		c.op(opLocalGet, src)
		c.i64(0xffffffff)
		c.op(opI64And, opLocalSet, lo, opLocalGet, src)
		c.i64(32)
		c.op(opI64ShrU, opLocalSet, hi)
	}
	half(0, 2, 3)
	half(1, 4, 5)
	// t = alo*blo
	c.op(opLocalGet, 2, opLocalGet, 4, opI64Mul, opLocalSet, 6)
	// u = ahi*blo + t>>32
	c.op(opLocalGet, 3, opLocalGet, 4, opI64Mul, opLocalGet, 6)
	c.i64(32)
	c.op(opI64ShrU, opI64Add, opLocalSet, 7)
	// t = alo*bhi + (u & 0xffffffff)   (t reused as v)
	c.op(opLocalGet, 2, opLocalGet, 5, opI64Mul, opLocalGet, 7)
	c.i64(0xffffffff)
	c.op(opI64And, opI64Add, opLocalSet, 6)
	// ahi*bhi + u>>32 + v>>32
	c.op(opLocalGet, 3, opLocalGet, 5, opI64Mul, opLocalGet, 7)
	c.i64(32)
	c.op(opI64ShrU, opI64Add, opLocalGet, 6)
	c.i64(32)
	c.op(opI64ShrU, opI64Add, opEnd)
	return c.b
}

// bswap64Body reverses the bytes of param 0.
func bswap64Body() []byte {
	c := &cg{b: []byte{0}}
	for i := 0; i < 8; i++ {
		c.op(opLocalGet, 0)
		c.i64(int64(8 * i))
		c.op(opI64ShrU)
		c.i64(0xff)
		c.op(opI64And)
		c.i64(int64(56 - 8*i))
		c.op(opI64Shl)
		if i > 0 {
			c.op(opI64Or)
		}
	}
	c.op(opEnd)
	return c.b
}

func (c *cg) emitFunc(pkg *ir.Package, fn *ir.Func) ([]byte, error) {
	c.pkg = pkg
	c.fn = fn
	c.consts = map[string]int64{}
	for _, cn := range pkg.Consts {
		c.consts[cn.Name] = cn.Value
	}
	c.locals = nil
	c.ref = map[*ir.Node]int{}
	c.ref2 = map[*ir.Node]int{}
	c.scope = nil
	for _, pa := range fn.Params {
		c.declare(nil, pa.Name, pa.Type)
	}
	c.bindStmts(fn.Body)
	c.scratch = len(c.locals)
	nvars := len(c.locals) + 1 - len(fn.Params)
	c.b = nil
	c.b = uleb(c.b, 1)
	c.b = uleb(c.b, uint64(nvars))
	c.b = append(c.b, i64)
	if err := c.emitStmts(fn.Body); err != nil {
		return nil, err
	}
	// Falling off the end returns zeros, as the native code returns 0.
	for i := 0; i < c.sigs[pkg.Path+"."+fn.Name].nres; i++ {
		c.i64(0)
	}
	c.op(opEnd)
	return c.b, nil
}

// declare gives the declaration s (nil for a param) a local of its own.
func (c *cg) declare(s *ir.Node, name, t string) {
	i := len(c.locals)
	c.locals = append(c.locals, local{name, c.resolve(c.pkg, t)})
	c.scope = append(c.scope, i)
	if s != nil {
		c.ref[s] = i
	}
}

func (c *cg) bindName(n *ir.Node, name string) {
	for j := len(c.scope) - 1; j >= 0; j-- {
		if c.locals[c.scope[j]].name == name {
			c.ref[n] = c.scope[j]
			return
		}
	}
}

// bindStmts resolves names as the checker scopes them; see the native
// compiler's bindFunc.
func (c *cg) bindStmts(stmts []*ir.Node) {
	n := len(c.scope)
	for _, s := range stmts {
		if s == nil {
			continue
		}
		c.bindExpr(s.Val)
		c.bindExpr(s.Val2)
		c.bindExpr(s.Base)
		c.bindExpr(s.Addr)
		c.bindExpr(s.Cond)
		switch s.Op {
		case "var":
			c.declare(s, s.Name, s.Type)
		case "var2":
			if s.Name != "_" {
				c.declare(s, s.Name, s.Type)
			}
			if s.Two.Name != "_" {
				c.declare(nil, s.Two.Name, s.Two.Type)
				c.ref2[s] = len(c.locals) - 1
			}
		case "assign":
			c.bindName(s, s.Name)
		case "assign2":
			c.bindName(s, s.Name)
			for j := len(c.scope) - 1; j >= 0; j-- {
				if c.locals[c.scope[j]].name == s.Two.Name {
					c.ref2[s] = c.scope[j]
					break
				}
			}
		}
		c.bindStmts(s.Then)
		c.bindStmts(s.Else)
		c.bindStmts(s.Body)
	}
	c.scope = c.scope[:n]
}

func (c *cg) bindExpr(n *ir.Node) {
	if n == nil {
		return
	}
	if n.Op == "name" && n.Pkg == "" {
		c.bindName(n, n.Name)
	}
	c.bindExpr(n.Left)
	c.bindExpr(n.Right)
	c.bindExpr(n.Arg)
	c.bindExpr(n.Base)
	for _, a := range n.Args {
		c.bindExpr(a)
	}
}

func (c *cg) emitStmts(stmts []*ir.Node) error {
	for _, s := range stmts {
		if s == nil {
			continue
		}
		if err := c.emitStmt(s); err != nil {
			return err
		}
	}
	return nil
}

var storeOp = map[string]byte{"store8": opI64Store8, "store16": opI64Store16, "store32": opI64Store32, "store64": opI64Store}
var storeAlign = map[string]uint64{"store8": 0, "store16": 1, "store32": 2, "store64": 3}

func (c *cg) emitStmt(s *ir.Node) error {
	switch s.Op {
	case "var", "assign":
		i, ok := c.ref[s]
		if !ok {
			return fmt.Errorf("%s %s", s.Op, s.Name)
		}
		if s.Val == nil {
			c.i64(0)
		} else if err := c.emitExpr(s.Val); err != nil {
			return err
		}
		c.localSet(i)
	case "var2", "assign2":
		if err := c.emitExpr(s.Val); err != nil {
			return err
		}
		if j, ok := c.ref2[s]; ok {
			c.localSet(j)
		} else {
			c.op(opDrop)
		}
		if i, ok := c.ref[s]; ok {
			c.localSet(i)
		} else {
			c.op(opDrop)
		}
	case "setfield":
		off, err := c.fieldOff(s.Base, s.Name)
		if err != nil {
			return err
		}
		if err := c.addr(s.Base); err != nil {
			return err
		}
		if err := c.emitExpr(s.Val); err != nil {
			return err
		}
		c.memAlign(opI64Store, 3, uint64(off))
	case "store8", "store16", "store32", "store64":
		if err := c.addr(s.Addr); err != nil {
			return err
		}
		if err := c.emitExpr(s.Val); err != nil {
			return err
		}
		c.memAlign(storeOp[s.Op], 0, 0)
	case "expr":
		if err := c.emitExpr(s.Val); err != nil {
			return err
		}
		for i := 0; i < c.results(s.Val); i++ {
			c.op(opDrop)
		}
	case "return":
		nres := c.sigs[c.pkg.Path+"."+c.fn.Name].nres
		if s.Val == nil {
			for i := 0; i < nres; i++ {
				c.i64(0)
			}
		} else {
			if err := c.emitExpr(s.Val); err != nil {
				return err
			}
			if s.Val2 != nil {
				if err := c.emitExpr(s.Val2); err != nil {
					return err
				}
			}
		}
		c.op(opReturn)
	case "if":
		if err := c.cond(s.Cond); err != nil {
			return err
		}
		c.op(opIf, blockVoid)
		if err := c.emitStmts(s.Then); err != nil {
			return err
		}
		if len(s.Else) > 0 {
			c.op(opElse)
			if err := c.emitStmts(s.Else); err != nil {
				return err
			}
		}
		c.op(opEnd)
	case "while":
		c.op(opBlock, blockVoid, opLoop, blockVoid)
		if err := c.cond(s.Cond); err != nil {
			return err
		}
		c.op(opI32Eqz, opBrIf, 1)
		if err := c.emitStmts(s.Body); err != nil {
			return err
		}
		c.op(opBr, 0, opEnd, opEnd)
	default:
		return fmt.Errorf("stmt %s", s.Op)
	}
	return nil
}

// results is how many values n leaves on the stack.
func (c *cg) results(n *ir.Node) int {
	if n.Op == "call" {
		path := n.Pkg
		if path == "" {
			path = c.pkg.Path
		}
		return c.sigs[path+"."+n.Func].nres
	}
	return 1
}

// cond evaluates a bool as an i32, for if and br_if.
func (c *cg) cond(n *ir.Node) error {
	if err := c.emitExpr(n); err != nil {
		return err
	}
	c.op(opI32WrapI64)
	return nil
}

// addr evaluates a pointer as an i32 address.
func (c *cg) addr(n *ir.Node) error {
	if err := c.emitExpr(n); err != nil {
		return err
	}
	c.op(opI32WrapI64)
	return nil
}

var arithOp = map[string]byte{
	"add": opI64Add, "sub": opI64Sub, "mul": opI64Mul, "div": opI64DivS, "mod": opI64RemS,
	"and": opI64And, "or": opI64Or, "xor": opI64Xor, "shl": opI64Shl, "shr": opI64ShrS,
	"ushr": opI64ShrU, "udiv": opI64DivU, "urem": opI64RemU,
}

var cmpOp = map[string]byte{
	"eq": opI64Eq, "ne": opI64Ne, "lt": opI64LtS, "le": opI64LeS, "gt": opI64GtS, "ge": opI64GeS, "ult": opI64LtU,
}

var loadOp = map[string]byte{"load8": opI64Load8U, "load16": opI64Load16U, "load32": opI64Load32U, "load64": opI64Load}

func (c *cg) emitExpr(n *ir.Node) error {
	if n == nil {
		return fmt.Errorf("nil expr in %s.%s", c.pkg.Path, c.fn.Name)
	}
	switch n.Op {
	case "int":
		c.i64(n.Int)
	case "sizeof":
		sz, err := c.sizeOf(n.Type)
		if err != nil {
			return err
		}
		c.i64(sz)
	case "bool":
		if n.Bool {
			c.i64(1)
		} else {
			c.i64(0)
		}
	case "strptr":
		c.i64(int64(roBase + c.intern(n.Str)))
	case "strlen":
		c.i64(int64(len(n.Str)))
	case "len":
		_, cnt, err := c.table(n)
		if err != nil {
			return err
		}
		c.i64(int64(cnt))
	case "index":
		// An unsigned check against the length, so a negative index fails
		// it too; a failed check traps, as the native ud2 does.
		off, cnt, err := c.table(n)
		if err != nil {
			return err
		}
		if err := c.emitExpr(n.Arg); err != nil {
			return err
		}
		c.localTee(c.scratch)
		c.i64(int64(cnt))
		c.op(opI64GeU, opIf, blockVoid, opUnreachable, opEnd)
		c.i64(int64(roBase + off))
		c.localGet(c.scratch)
		c.i64(3)
		c.op(opI64Shl, opI64Add, opI32WrapI64)
		c.memAlign(opI64Load, 3, 0)
	case "name":
		if n.Pkg != "" {
			v, err := c.pkgConst(n)
			if err != nil {
				return err
			}
			c.i64(v)
			return nil
		}
		if i, ok := c.ref[n]; ok {
			c.localGet(i)
			return nil
		}
		if v, ok := c.consts[n.Name]; ok {
			c.i64(v)
			return nil
		}
		return fmt.Errorf("name %s", n.Name)
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr", "ushr", "udiv", "urem":
		if err := c.emitExpr(n.Left); err != nil {
			return err
		}
		if err := c.emitExpr(n.Right); err != nil {
			return err
		}
		c.op(arithOp[n.Op])
	case "umulhi":
		if err := c.emitExpr(n.Left); err != nil {
			return err
		}
		if err := c.emitExpr(n.Right); err != nil {
			return err
		}
		c.op(opCall)
		c.b = uleb(c.b, fnUmulhi)
	case "eq", "ne", "lt", "le", "gt", "ge", "ult":
		if err := c.emitExpr(n.Left); err != nil {
			return err
		}
		if err := c.emitExpr(n.Right); err != nil {
			return err
		}
		c.op(cmpOp[n.Op], opI64ExtendI32U)
	case "land":
		if err := c.cond(n.Left); err != nil {
			return err
		}
		c.op(opIf, i64)
		if err := c.emitExpr(n.Right); err != nil {
			return err
		}
		c.op(opElse)
		c.i64(0)
		c.op(opEnd)
	case "lor":
		if err := c.cond(n.Left); err != nil {
			return err
		}
		c.op(opIf, i64)
		c.i64(1)
		c.op(opElse)
		if err := c.emitExpr(n.Right); err != nil {
			return err
		}
		c.op(opEnd)
	case "not":
		if err := c.emitExpr(n.Arg); err != nil {
			return err
		}
		c.op(opI64Eqz, opI64ExtendI32U)
	case "neg":
		c.i64(0)
		if err := c.emitExpr(n.Arg); err != nil {
			return err
		}
		c.op(opI64Sub)
	case "bnot":
		if err := c.emitExpr(n.Arg); err != nil {
			return err
		}
		c.i64(-1)
		c.op(opI64Xor)
	case "cast":
		return c.emitExpr(n.Arg)
	case "field":
		off, err := c.fieldOff(n.Base, n.Name)
		if err != nil {
			return err
		}
		if err := c.addr(n.Base); err != nil {
			return err
		}
		c.memAlign(opI64Load, 3, uint64(off))
	case "load8", "load16", "load32", "load64":
		if err := c.addr(n.Arg); err != nil {
			return err
		}
		c.memAlign(loadOp[n.Op], 0, 0)
	case "bswap16", "bswap32", "bswap64":
		if err := c.emitExpr(n.Arg); err != nil {
			return err
		}
		c.op(opCall)
		c.b = uleb(c.b, fnBswap64)
		// The low 16 or 32 bits reversed, zero-extended, as the native
		// bswap leaves them.
		if n.Op == "bswap16" {
			c.i64(48)
			c.op(opI64ShrU)
		} else if n.Op == "bswap32" {
			c.i64(32)
			c.op(opI64ShrU)
		}
	case "call":
		path := n.Pkg
		if path == "" {
			path = c.pkg.Path
		}
		idx, ok := c.funcs[path+"."+n.Func]
		if !ok {
			return fmt.Errorf("call %s.%s", path, n.Func)
		}
		for _, a := range n.Args {
			if err := c.emitExpr(a); err != nil {
				return err
			}
		}
		c.op(opCall)
		c.b = uleb(c.b, uint64(idx))
	case "syscall":
		if len(n.Args) != 7 {
			return fmt.Errorf("syscall arity")
		}
		for _, a := range n.Args {
			if err := c.emitExpr(a); err != nil {
				return err
			}
		}
		c.op(opCall)
		c.b = uleb(c.b, fnSyscall)
	default:
		return fmt.Errorf("expr %s", n.Op)
	}
	return nil
}

func (c *cg) resolve(pkg *ir.Package, t string) string {
	if t == "i64" || t == "bool" {
		return t
	}
	star := strings.HasPrefix(t, "*")
	t = strings.TrimPrefix(t, "*")
	full := pkg.Path + "." + t
	if i := strings.LastIndex(t, "."); i >= 0 {
		full = t
	}
	if star {
		return "*" + full
	}
	return full
}

func (c *cg) sizeOf(t string) (int64, error) {
	i := strings.LastIndex(t, ".")
	if i < 0 || c.pkgs[t[:i]] == nil {
		return 0, fmt.Errorf("sizeof %s", t)
	}
	for _, td := range c.pkgs[t[:i]].Types {
		if td.Name == t[i+1:] {
			return int64(8 * len(td.Fields)), nil
		}
	}
	return 0, fmt.Errorf("sizeof %s", t)
}

func (c *cg) fieldOff(base *ir.Node, field string) (int, error) {
	pkg, td, err := c.structOf(c.typeOf(base))
	if err != nil {
		return 0, err
	}
	_ = pkg
	for i, f := range td.Fields {
		if f.Name == field {
			return i * 8, nil
		}
	}
	return 0, fmt.Errorf("field %s.%s", td.Name, field)
}

func (c *cg) structOf(bt string) (*ir.Package, *ir.TypeDecl, error) {
	if !strings.HasPrefix(bt, "*") {
		return nil, nil, fmt.Errorf("not a pointer: %s", bt)
	}
	full := strings.TrimPrefix(bt, "*")
	i := strings.LastIndex(full, ".")
	if i < 0 || c.pkgs[full[:i]] == nil {
		return nil, nil, fmt.Errorf("type %s", bt)
	}
	pkg := c.pkgs[full[:i]]
	for ti := range pkg.Types {
		if pkg.Types[ti].Name == full[i+1:] {
			return pkg, &pkg.Types[ti], nil
		}
	}
	return nil, nil, fmt.Errorf("type %s", bt)
}

func (c *cg) typeOf(n *ir.Node) string {
	if n == nil {
		return "invalid"
	}
	switch n.Op {
	case "name":
		if n.Pkg != "" {
			return "i64"
		}
		if i, ok := c.ref[n]; ok {
			return c.locals[i].typ
		}
		return "i64"
	case "cast":
		return c.resolve(c.pkg, n.Type)
	case "field":
		pkg, td, err := c.structOf(c.typeOf(n.Base))
		if err != nil {
			return "invalid"
		}
		for _, f := range td.Fields {
			if f.Name == n.Name {
				return c.resolve(pkg, f.Type)
			}
		}
		return "invalid"
	case "call":
		path := n.Pkg
		if path == "" {
			path = c.pkg.Path
		}
		return c.sigs[path+"."+n.Func].result
	default:
		return "i64"
	}
}

func (c *cg) pkgConst(n *ir.Node) (int64, error) {
	if pkg := c.pkgs[n.Pkg]; pkg != nil {
		for _, cn := range pkg.Consts {
			if cn.Name == n.Name {
				return cn.Value, nil
			}
		}
	}
	return 0, fmt.Errorf("const %s.%s", n.Pkg, n.Name)
}

// reachable is the set of funcs main can call, directly or not.
func reachable(p *ir.Program, root string) map[string]bool {
	funcs := map[string]*ir.Func{}
	for i := range p.Packages {
		pkg := &p.Packages[i]
		for fi := range pkg.Funcs {
			funcs[pkg.Path+"."+pkg.Funcs[fi].Name] = &pkg.Funcs[fi]
		}
	}
	live := map[string]bool{}
	work := []string{root}
	for len(work) > 0 {
		key := work[len(work)-1]
		work = work[:len(work)-1]
		if live[key] || funcs[key] == nil {
			continue
		}
		live[key] = true
		pkg := key[:strings.LastIndex(key, ".")]
		for _, st := range funcs[key].Body {
			st.Walk(func(n *ir.Node) {
				if n.Op == "call" {
					callee := n.Pkg
					if callee == "" {
						callee = pkg
					}
					work = append(work, callee+"."+n.Func)
				}
			})
		}
	}
	return live
}

// Encoding.

const (
	i64       = 0x7e
	blockVoid = 0x40

	opUnreachable   = 0x00
	opBlock         = 0x02
	opLoop          = 0x03
	opIf            = 0x04
	opElse          = 0x05
	opEnd           = 0x0b
	opBr            = 0x0c
	opBrIf          = 0x0d
	opReturn        = 0x0f
	opCall          = 0x10
	opDrop          = 0x1a
	opLocalGet      = 0x20
	opLocalSet      = 0x21
	opLocalTee      = 0x22
	opI64Load       = 0x29
	opI64Load8U     = 0x31
	opI64Load16U    = 0x33
	opI64Load32U    = 0x35
	opI64Store      = 0x37
	opI64Store8     = 0x3c
	opI64Store16    = 0x3d
	opI64Store32    = 0x3e
	opI32Const      = 0x41
	opI64Const      = 0x42
	opI32Eqz        = 0x45
	opI64Eqz        = 0x50
	opI64Eq         = 0x51
	opI64Ne         = 0x52
	opI64LtS        = 0x53
	opI64LtU        = 0x54
	opI64GtS        = 0x55
	opI64LeS        = 0x57
	opI64GeS        = 0x59
	opI64GeU        = 0x5a
	opI64Add        = 0x7c
	opI64Sub        = 0x7d
	opI64Mul        = 0x7e
	opI64DivS       = 0x7f
	opI64DivU       = 0x80
	opI64RemS       = 0x81
	opI64RemU       = 0x82
	opI64And        = 0x83
	opI64Or         = 0x84
	opI64Xor        = 0x85
	opI64Shl        = 0x86
	opI64ShrS       = 0x87
	opI64ShrU       = 0x88
	opI32WrapI64    = 0xa7
	opI64ExtendI32U = 0xad
)

func (c *cg) op(bs ...byte) { c.b = append(c.b, bs...) }

func (c *cg) i64(v int64) {
	c.b = append(c.b, opI64Const)
	c.b = sleb(c.b, v)
}

func (c *cg) localGet(i int) { c.op(opLocalGet); c.b = uleb(c.b, uint64(i)) }
func (c *cg) localSet(i int) { c.op(opLocalSet); c.b = uleb(c.b, uint64(i)) }
func (c *cg) localTee(i int) { c.op(opLocalTee); c.b = uleb(c.b, uint64(i)) }

// mem emits a 64-bit-aligned load or store.
func (c *cg) mem(op byte, off uint64) { c.memAlign(op, 3, off) }

// memAlign emits a load or store with the alignment hint (log2 bytes) and
// a constant offset. The hint is only a hint: Ovid pointers need not be
// aligned, and wasm allows that.
func (c *cg) memAlign(op byte, align, off uint64) {
	c.op(op)
	c.b = uleb(c.b, align)
	c.b = uleb(c.b, off)
}

func uleb(b []byte, v uint64) []byte {
	for {
		x := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, x|0x80)
			continue
		}
		return append(b, x)
	}
}

func sleb(b []byte, v int64) []byte {
	for {
		x := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && x&0x40 == 0) || (v == -1 && x&0x40 != 0) {
			return append(b, x)
		}
		b = append(b, x|0x80)
	}
}

func name(b []byte, s string) []byte {
	b = uleb(b, uint64(len(s)))
	return append(b, s...)
}

func section(m []byte, id byte, body []byte) []byte {
	m = append(m, id)
	m = uleb(m, uint64(len(body)))
	return append(m, body...)
}
