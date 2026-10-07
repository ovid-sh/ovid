// Package compile lowers a checked Ovid program to a static x86-64 ELF.
// There is no libc and no implicit allocation. main receives *ovid/io.Cap.
package compile

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"ovid/internal/lower"
	"sort"
	"strings"

	"ovid/internal/asm"
	"ovid/internal/elf"
	"ovid/internal/ir"
)

// heapSize is the first heap region, which the runtime maps for every
// program; ovid/io.Alloc maps more of the same size when it is spent.
// The self-hosted compiler emits the same size.
const heapSize int64 = 128 << 20

// ExitOOM is the exit code of a program the kernel refused memory, here
// and in ovid/io.Map (EX_OSERR).
const ExitOOM = 71

// Compile emits a statically linked executable.
func Compile(p *ir.Program) ([]byte, error) {
	bin, _, err := CompileMap(p)
	return bin, err
}

// Mark says the code from Off (an offset into the code, which starts at
// elf.CodeVAddr) up to the next mark belongs to the func or statement ID.
type Mark struct {
	Off int
	ID  string
}

// CompileMap is Compile plus the code map, in increasing Off order, which
// turns a crash address back into a statement.
func CompileMap(p *ir.Program) ([]byte, []Mark, error) {
	o, err := CompileAll(p)
	if err != nil {
		return nil, nil, err
	}
	return o.Bin, o.Marks, nil
}

// Output is a compiled program and what the compiler knows about it.
type Output struct {
	Bin   []byte
	Marks []Mark
	// Syscalls are the numbers of the system calls the program can make,
	// in increasing order: those of every syscall in a func reachable from
	// main, and the three the startup code makes. A call listed is
	// reachable, not necessarily made.
	Syscalls []int64
	// SyscallsUnknown counts the reachable syscalls whose number is not a
	// constant, which Syscalls therefore cannot name.
	SyscallsUnknown int
}

// CompileAll is CompileMap plus the system calls the program can make.
func CompileAll(p *ir.Program) (*Output, error) {
	// A bytes is two words; the code generator sees only one per value,
	// in a copy of the tree, since the module's own is read by id after.
	p = lower.Clone(p)
	if err := lower.Program(p); err != nil {
		return nil, err
	}
	bin, c, err := compileProg(p)
	if err != nil {
		return nil, err
	}
	o := &Output{Bin: bin, Marks: c.marks, SyscallsUnknown: c.sysUnknown}
	for n := range c.sys {
		o.Syscalls = append(o.Syscalls, n)
	}
	sort.Slice(o.Syscalls, func(i, j int) bool { return o.Syscalls[i] < o.Syscalls[j] })
	return o, nil
}

func compileProg(p *ir.Program) ([]byte, *cg, error) {
	if p == nil {
		return nil, nil, fmt.Errorf("nil program")
	}
	c := &cg{
		prog:      p,
		strs:      map[string]int{},
		tables:    map[string]tableRef{},
		funcLabel: map[string]int{},
		sigs:      map[string]sig{},
		pkgs:      map[string]*ir.Package{},
		// The startup code maps the heap, exits with main's result, and
		// writes "out of memory" when the heap is refused.
		sys: map[int64]bool{1: true, 9: true, 60: true},
	}
	for i := range p.Packages {
		pkg := &p.Packages[i]
		c.pkgs[pkg.Path] = pkg
		for _, fn := range pkg.Funcs {
			key := pkg.Path + "." + fn.Name
			if _, ok := c.funcLabel[key]; ok {
				return nil, nil, fmt.Errorf("duplicate func %s", key)
			}
			c.funcLabel[key] = c.b.NewLabel()
			var ps []string
			for _, pa := range fn.Params {
				ps = append(ps, c.resolve(pkg, pa.Type))
			}
			c.sigs[key] = sig{params: ps, result: c.resolve(pkg, fn.Result)}
		}
	}
	mainKey := p.Entry + ".main"
	if _, ok := c.funcLabel[mainKey]; !ok {
		return nil, nil, fmt.Errorf("missing %s", mainKey)
	}
	if err := c.emitStartup(c.funcLabel[mainKey]); err != nil {
		return nil, nil, err
	}
	live := reachable(p, mainKey)
	for i := range p.Packages {
		pkg := &p.Packages[i]
		for fi := range pkg.Funcs {
			if !live[pkg.Path+"."+pkg.Funcs[fi].Name] {
				continue
			}
			if err := c.emitFunc(pkg, &pkg.Funcs[fi]); err != nil {
				return nil, nil, err
			}
		}
	}
	if err := c.b.PatchRel(); err != nil {
		return nil, nil, err
	}
	c.b.PatchAbs(elf.RodataVAddr(len(c.b.Code)))
	return elf.Link(c.b.Code, c.ro, 0), c, nil
}

type sig struct {
	params []string
	result string
}

type cg struct {
	prog       *ir.Program
	b          asm.Buf
	ro         []byte
	strs       map[string]int
	tables     map[string]tableRef
	funcLabel  map[string]int
	sigs       map[string]sig
	pkgs       map[string]*ir.Package
	locals     []local          // the func's params and vars, one per declaration
	ref2       map[*ir.Node]int // the second local of a var2 or assign2
	ref3       map[*ir.Node]int // the third, when a lowered bytes result has one
	ref        map[*ir.Node]int // a var, an assign, or a local's name: its local
	scope      []int            // the locals in scope while binding, innermost last
	live       int              // the most locals in scope at once
	consts     map[string]int64
	localBytes int32
	epi        int
	traps      []trap      // the func's failed-check stubs, emitted after its ret
	last       *ir.Node    // the func's final statement when it is a return
	regs       map[int]int // locals that live in a register
	saved      []int       // callee-saved registers the func uses
	saveBase   int32       // their save slots lie below this displacement
	tregs      []int       // registers for temps at levels 0, 1, ...
	// The last quotient of a local by a constant, kept in qreg (-1 for
	// none) for the next division of the same local by the same constant,
	// while the local is not assigned and no label is reached.
	qreg       int
	qloc       int
	qdiv       int64
	qmark      int  // b.Marks when it was kept; -1 for none
	qcall      bool // qreg is a temp register, which calls clobber
	pkg        *ir.Package
	fn         *ir.Func
	marks      []Mark
	sys        map[int64]bool // the numbers of the system calls emitted
	sysUnknown int            // syscalls whose number is not a constant
}

func (c *cg) mark(id string) {
	if n := len(c.marks); n > 0 && c.marks[n-1].Off == len(c.b.Code) {
		c.marks[n-1].ID = id
		return
	}
	c.marks = append(c.marks, Mark{len(c.b.Code), id})
}

// table places the elements of the table path.Name in rodata on first use
// and returns their offset and count.
func (c *cg) table(n *ir.Node) (int, int, error) {
	path := n.Pkg
	if path == "" {
		path = c.pkg.Path
	}
	key := path + "." + n.Name
	if t, ok := c.tables[key]; ok {
		return t.off, t.n, nil
	}
	pkg := c.pkgs[path]
	if pkg == nil {
		return 0, 0, fmt.Errorf("table %s", key)
	}
	for i := range pkg.Consts {
		cn := &pkg.Consts[i]
		if cn.Name != n.Name || !cn.Table {
			continue
		}
		off := len(c.ro)
		for _, v := range cn.Values {
			c.ro = binary.LittleEndian.AppendUint64(c.ro, uint64(v))
		}
		c.tables[key] = tableRef{off, len(cn.Values)}
		return off, len(cn.Values), nil
	}
	return 0, 0, fmt.Errorf("table %s", key)
}

// tableRef is where a table's elements sit in rodata, and how many.
type tableRef struct{ off, n int }

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

func (c *cg) resolve(pkg *ir.Package, t string) string {
	if t == "i64" || t == "bool" {
		return t
	}
	star := false
	if strings.HasPrefix(t, "*") {
		star = true
		t = t[1:]
	}
	tpkg := pkg.Path
	name := t
	if i := strings.LastIndex(t, "."); i >= 0 {
		tpkg = t[:i]
		name = t[i+1:]
	}
	full := tpkg + "." + name
	if star {
		return "*" + full
	}
	return full
}

func (c *cg) emitStartup(mainLab int) error {
	argc, err := capOff(c.prog, "argc")
	if err != nil {
		return err
	}
	argv, err := capOff(c.prog, "argv")
	if err != nil {
		return err
	}
	heap, err := capOff(c.prog, "heap")
	if err != nil {
		return err
	}
	used, err := capOff(c.prog, "used")
	if err != nil {
		return err
	}
	size, err := capOff(c.prog, "size")
	if err != nil {
		return err
	}
	maps, err := capOff(c.prog, "maps")
	if err != nil {
		return err
	}
	fail := c.b.NewLabel()
	c.b.MovR12MemRsp()
	c.b.LeaR13RspPlus8()
	c.b.AndRspAlign()
	c.b.MovRegImm64(asm.RDI, 0)
	c.b.MovRegImm64(asm.RSI, heapSize)
	c.b.MovRegImm64(asm.RDX, 3)
	// MAP_PRIVATE|MAP_ANONYMOUS|MAP_NORESERVE: the heap is address space
	// until it is touched, so under the kernel's default overcommit
	// heuristic it does not count against a host with less memory than the
	// heap. Strict overcommit (vm.overcommit_memory=2) ignores the flag.
	c.b.MovRegImm64(asm.R10, 0x4022)
	c.b.MovRegImm64(asm.R8, -1)
	c.b.MovRegImm64(asm.R9, 0)
	c.b.MovRegImm64(asm.RAX, 9)
	c.b.Syscall()
	c.b.MovRegReg(asm.R14, asm.RAX)
	c.b.TestRaxRax()
	c.b.Jl(fail)
	c.b.SubRspImm(48)
	c.b.MovRegReg(asm.RCX, asm.RSP)
	c.b.MovRegReg(asm.RAX, asm.R12)
	c.b.MovMemRegDispRax(asm.RCX, argc)
	c.b.MovRegReg(asm.RAX, asm.R13)
	c.b.MovMemRegDispRax(asm.RCX, argv)
	c.b.MovRegReg(asm.RAX, asm.R14)
	c.b.MovMemRegDispRax(asm.RCX, heap)
	c.b.MovRegImm64(asm.RAX, 0)
	c.b.MovMemRegDispRax(asm.RCX, used)
	c.b.MovMemRegDispRax(asm.RCX, maps)
	c.b.MovRegImm64(asm.RAX, heapSize)
	c.b.MovMemRegDispRax(asm.RCX, size)
	c.b.MovRegReg(asm.RDI, asm.RSP)
	c.b.Call(mainLab)
	c.b.MovRegReg(asm.RDI, asm.RAX)
	c.b.MovRegImm64(asm.RAX, 60)
	c.b.Syscall()
	// The first region was refused: say "out of memory\n" on standard error
	// (the text is pushed, there being no rodata to point at yet) and exit
	// as ovid/io.Map does.
	c.b.Mark(fail)
	c.b.MovRegImm64(asm.RAX, 0xa79726f6d65) // "emory\n"
	c.b.PushReg(asm.RAX)
	c.b.MovRegImm64(asm.RAX, 0x6d20666f2074756f) // "out of m"
	c.b.PushReg(asm.RAX)
	c.b.MovRegImm64(asm.RDI, 2)
	c.b.MovRegReg(asm.RSI, asm.RSP)
	c.b.MovRegImm64(asm.RDX, 14)
	c.b.MovRegImm64(asm.RAX, 1)
	c.b.Syscall()
	c.b.MovRegImm64(asm.RDI, ExitOOM)
	c.b.MovRegImm64(asm.RAX, 60)
	c.b.Syscall()
	return nil
}

func capOff(p *ir.Program, name string) (int32, error) {
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
					return int32(i * 8), nil
				}
			}
			return 0, fmt.Errorf("ovid/io.Cap missing %s", name)
		}
	}
	return 0, fmt.Errorf("missing ovid/io.Cap")
}

func (c *cg) emitFunc(pkg *ir.Package, fn *ir.Func) error {
	c.pkg = pkg
	c.fn = fn
	c.consts = map[string]int64{}
	for _, cn := range pkg.Consts {
		c.consts[cn.Name] = cn.Value
	}
	c.bindFunc(fn)
	c.localBytes = int32(8 * c.live)
	peak := stmtsMax(fn.Body)
	tempBytes := int32(0)
	if peak >= 0 {
		tempBytes = int32((peak + 1) * 8)
	}
	c.allocRegs(fn)
	c.allocTemps()
	c.allocQuot(fn)
	c.saveBase = -(c.localBytes + tempBytes)
	frame := c.localBytes + tempBytes + int32(8*len(c.saved))
	if frame%16 != 0 {
		frame += 8
	}
	lab := c.funcLabel[pkg.Path+"."+fn.Name]
	c.b.Mark(lab)
	c.mark(fn.ID)
	c.epi = c.b.NewLabel()
	c.traps = nil
	c.b.PushReg(asm.RBP)
	c.b.MovRegReg(asm.RBP, asm.RSP)
	// The frame is not cleared: the checker lets no local be read before
	// its declaration has run, a declaration always writes its local (zero
	// when it gives no value), and a temp is written before it is read.
	c.b.SubRspImm(frame)
	for i, r := range c.saved {
		c.b.MovMemRbpReg(r, c.saveBase-int32(8*(i+1)))
	}
	for i := range fn.Params {
		if r, ok := c.regs[i]; !ok {
			c.b.MovMemRbpReg(argRegs[i], c.locals[i].disp)
		} else if r != argRegs[i] {
			c.b.MovRegReg(r, argRegs[i])
		}
	}
	c.last = nil
	for _, s := range fn.Body {
		if s != nil {
			c.last = s
		}
	}
	if c.last != nil && c.last.Op != "return" {
		c.last = nil
	}
	if err := c.emitStmts(fn.Body); err != nil {
		return err
	}
	c.mark(fn.ID)
	if c.last == nil {
		c.b.XorRaxRax()
	}
	c.b.Mark(c.epi)
	for i, r := range c.saved {
		c.b.MovRegMemRbp(r, c.saveBase-int32(8*(i+1)))
	}
	c.b.Leave()
	c.b.Ret()
	for _, t := range c.traps {
		c.b.Mark(t.label)
		c.mark(t.stmt)
		c.b.Ud2()
	}
	return nil
}

// trap is a failed check's ud2: out of line, after the func's ret, and
// marked with the statement the check was emitted under (the current
// mark: a while's condition is the while's) so a crash still names it.
type trap struct {
	label int
	stmt  string
}

// trapLabel is where a failed check jumps. Out of line, the fall-through
// of every check is its load; in line, the ud2 cost a loop over a table
// half again as much (255 ms against 171 ms for 400M reads on a Zen 4).
func (c *cg) trapLabel() int {
	t := trap{c.b.NewLabel(), c.marks[len(c.marks)-1].ID}
	c.traps = append(c.traps, t)
	return t.label
}

// local is a param or a var. Each declaration has its own, with its own
// type, frame slot, and register.
type local struct {
	name string
	typ  string
	disp int32
}

// bindFunc gives every param and var of fn its own local, params first and
// then vars in the order they are declared, and resolves each use of a
// name to the declaration in scope, as the checker scopes: a var's value
// is evaluated before its name is in scope, and a block's vars leave scope
// where it ends. A local's slot is the number of locals in scope when it is
// declared, so vars of sibling blocks share slots, and the frame holds the
// most locals in scope at once.
func (c *cg) bindFunc(fn *ir.Func) {
	c.locals = nil
	c.ref = map[*ir.Node]int{}
	c.ref2 = map[*ir.Node]int{}
	c.ref3 = map[*ir.Node]int{}
	c.scope = nil
	c.live = 0
	for _, pa := range fn.Params {
		c.declare(nil, pa.Name, pa.Type)
	}
	c.bindStmts(fn.Body)
}

// declare puts a new local in scope for the declaration s (nil for a param).
func (c *cg) declare(s *ir.Node, name, t string) {
	i := len(c.locals)
	c.locals = append(c.locals, local{name, c.resolve(c.pkg, t), -int32(8 * (len(c.scope) + 1))})
	c.scope = append(c.scope, i)
	c.live = max1(c.live, len(c.scope))
	if s != nil {
		c.ref[s] = i
	}
}

// bindName resolves n, an assign or a name, to the innermost local called
// name in scope, if there is one.
func (c *cg) bindName(n *ir.Node, name string) {
	for j := len(c.scope) - 1; j >= 0; j-- {
		if c.locals[c.scope[j]].name == name {
			c.ref[n] = c.scope[j]
			return
		}
	}
}

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
		if s.Op == "return" {
			// A lowered return b, e keeps e, the third word, in Args.
			for _, a := range s.Args {
				c.bindExpr(a)
			}
		}
		switch s.Op {
		case "var":
			c.declare(s, s.Name, s.Type)
		case "var2":
			// The second name's local is in ref2; _ declares nothing.
			if s.Name != "_" {
				c.declare(s, s.Name, s.Type)
			}
			if s.Two.Name != "_" {
				c.declare(nil, s.Two.Name, s.Two.Type)
				c.ref2[s] = len(c.locals) - 1
			}
			if len(s.Args) == 1 && s.Args[0].Name != "_" {
				c.declare(nil, s.Args[0].Name, "i64")
				c.ref3[s] = len(c.locals) - 1
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
			if len(s.Args) == 1 {
				for j := len(c.scope) - 1; j >= 0; j-- {
					if c.locals[c.scope[j]].name == s.Args[0].Name {
						c.ref3[s] = c.scope[j]
						break
					}
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

// argRegs are the registers a call's arguments arrive in.
var argRegs = []int{asm.RDI, asm.RSI, asm.RDX, asm.RCX, asm.R8, asm.R9}

// allocRegs decides which locals live in registers. A func that calls nothing keeps its params where
// they arrive (rdi, rsi, r8, r9; rdx and rcx are needed as scratch) and may
// use the other registers a call would clobber, which cost nothing to
// take. After those come the callee-saved registers, which must be saved
// and restored, so only a local used at least three times gets one. The
// most used locals choose first; a use inside a loop counts four times one
// outside it.
func (c *cg) allocRegs(fn *ir.Func) {
	c.regs = map[int]int{}
	c.saved = nil
	weight := make([]int, len(c.locals))
	c.weighStmts(fn.Body, 0, weight)
	var free []int
	if !stmtsCall(fn.Body) {
		for i, r := range argRegs {
			if r == asm.RDX || r == asm.RCX {
				continue
			}
			if i < len(fn.Params) {
				c.regs[i] = r
			} else {
				free = append(free, r)
			}
		}
		free = append(free, asm.R10, asm.R11)
	}
	callee := []int{asm.RBX, asm.R12, asm.R13, asm.R14, asm.R15}
	for {
		// The heaviest local without a register; the first of equals.
		best := -1
		for i, w := range weight {
			if _, ok := c.regs[i]; ok || w == 0 {
				continue
			}
			if best < 0 || w > weight[best] {
				best = i
			}
		}
		if best < 0 {
			return
		}
		if len(free) > 0 {
			c.regs[best] = free[0]
			free = free[1:]
		} else if len(callee) > 0 && weight[best] >= 3 {
			c.regs[best] = callee[0]
			c.saved = append(c.saved, callee[0])
			callee = callee[1:]
		} else {
			return
		}
	}
}

// allocTemps gives the first temp levels the registers a call would
// clobber that no local uses, r11 first. A temp still keeps its slot in
// the frame, for the expressions that cannot use the register.
func (c *cg) allocTemps() {
	used := map[int]bool{}
	for _, r := range c.regs {
		used[r] = true
	}
	c.tregs = nil
	for _, r := range []int{asm.R11, asm.R10, asm.R9, asm.R8, asm.RSI, asm.RDI} {
		if !used[r] {
			c.tregs = append(c.tregs, r)
		}
	}
}

// allocQuot gives a func that divides locals by constants more than once
// a callee-saved register no local uses, to keep a quotient in; calls
// leave it alone. With none left, it takes the last temp register
// instead, and a call drops the quotient.
func (c *cg) allocQuot(fn *ir.Func) {
	c.qreg = -1
	c.qloc = -1
	c.qmark = -1
	c.qcall = false
	callee := []int{asm.RBX, asm.R12, asm.R13, asm.R14, asm.R15}
	if c.quotStmts(fn.Body) < 2 {
		return
	}
	if len(c.saved) < len(callee) {
		c.qreg = callee[len(c.saved)]
		c.saved = append(c.saved, c.qreg)
	} else if len(c.tregs) > 0 {
		c.qreg = c.tregs[len(c.tregs)-1]
		c.tregs = c.tregs[:len(c.tregs)-1]
		c.qcall = true
	}
}

// quotOf reports whether n divides a local by a constant through magic:
// the local, and the constant.
func (c *cg) quotOf(n *ir.Node) (int, int64, bool) {
	if n.Op != "div" && n.Op != "mod" {
		return 0, 0, false
	}
	i, ok := c.ref[uncast(n.Left)]
	k, d := c.operand(n.Right)
	if !ok || k != kImm || (d > -2 && d < 2) || (d > 0 && d&(d-1) == 0) {
		return 0, 0, false
	}
	return i, d, true
}

// quotStmts counts the divisions in stmts that quotOf accepts.
func (c *cg) quotStmts(stmts []*ir.Node) int {
	m := 0
	for _, s := range stmts {
		if s == nil {
			continue
		}
		m += c.quotExpr(s.Val) + c.quotExpr(s.Val2) + c.quotExpr(s.Base) + c.quotExpr(s.Addr) + c.quotExpr(s.Cond)
		m += c.quotStmts(s.Then) + c.quotStmts(s.Else) + c.quotStmts(s.Body)
	}
	return m
}

func (c *cg) quotExpr(n *ir.Node) int {
	if n == nil {
		return 0
	}
	m := c.quotExpr(n.Left) + c.quotExpr(n.Right) + c.quotExpr(n.Arg) + c.quotExpr(n.Base)
	for _, a := range n.Args {
		m += c.quotExpr(a)
	}
	if _, _, ok := c.quotOf(n); ok {
		m++
	}
	return m
}

// weighStmts adds, for each local used or assigned in stmts, 4^depth to
// its weight, depth being the number of loops around the use (at most 5).
func (c *cg) weighStmts(stmts []*ir.Node, depth int, w []int) {
	for _, s := range stmts {
		if s == nil {
			continue
		}
		if i, ok := c.ref[s]; ok {
			w[i] += 1 << (2 * depth)
		}
		c.weighExpr(s.Val, depth, w)
		c.weighExpr(s.Val2, depth, w)
		c.weighExpr(s.Base, depth, w)
		c.weighExpr(s.Addr, depth, w)
		if s.Op == "return" {
			for _, a := range s.Args {
				c.weighExpr(a, depth, w)
			}
		}
		c.weighStmts(s.Then, depth, w)
		c.weighStmts(s.Else, depth, w)
		if s.Op == "while" {
			inner := depth
			if inner < 5 {
				inner++
			}
			c.weighExpr(s.Cond, inner, w)
			c.weighStmts(s.Body, inner, w)
		} else {
			c.weighExpr(s.Cond, depth, w)
		}
	}
}

func (c *cg) weighExpr(n *ir.Node, depth int, w []int) {
	if n == nil {
		return
	}
	if i, ok := c.ref[n]; ok {
		w[i] += 1 << (2 * depth)
	}
	c.weighExpr(n.Left, depth, w)
	c.weighExpr(n.Right, depth, w)
	c.weighExpr(n.Arg, depth, w)
	c.weighExpr(n.Base, depth, w)
	for _, a := range n.Args {
		c.weighExpr(a, depth, w)
	}
}

// stmtsCall reports whether stmts contain a call or a syscall.
func stmtsCall(stmts []*ir.Node) bool {
	for _, s := range stmts {
		if s == nil {
			continue
		}
		if exprCalls(s.Val) || exprCalls(s.Val2) || exprCalls(s.Base) || exprCalls(s.Addr) || exprCalls(s.Cond) {
			return true
		}
		if s.Op == "return" && len(s.Args) == 1 && exprCalls(s.Args[0]) {
			return true
		}
		if stmtsCall(s.Then) || stmtsCall(s.Else) || stmtsCall(s.Body) {
			return true
		}
	}
	return false
}

func exprCalls(n *ir.Node) bool {
	if n == nil {
		return false
	}
	if n.Op == "call" || n.Op == "syscall" {
		return true
	}
	return exprCalls(n.Left) || exprCalls(n.Right) || exprCalls(n.Arg) || exprCalls(n.Base)
}

func stmtsMax(stmts []*ir.Node) int {
	m := -1
	for _, s := range stmts {
		if s == nil {
			continue
		}
		if x := stmtMax(s); x > m {
			m = x
		}
	}
	return m
}

func stmtMax(s *ir.Node) int {
	switch s.Op {
	case "var", "assign", "expr", "var2", "assign2":
		return exprMax(s.Val, 0)
	case "return":
		if len(s.Args) == 1 {
			return max2(max2(exprMax(s.Args[0], 0), exprMax(s.Val2, 1), 1), exprMax(s.Val, 2), 1)
		}
		if s.Val2 != nil {
			// The error code waits in temp 0 while the value is evaluated.
			return max2(exprMax(s.Val2, 0), exprMax(s.Val, 1), 0)
		}
		return exprMax(s.Val, 0)
	case "setfield":
		return max2(exprMax(s.Base, 0), exprMax(s.Val, 1), 1)
	case "store8", "store16", "store32", "store64", "chk":
		return max2(exprMax(s.Addr, 0), exprMax(s.Val, 1), 1)
	case "if":
		m := exprMax(s.Cond, 0)
		m = max1(m, stmtsMax(s.Then))
		m = max1(m, stmtsMax(s.Else))
		return m
	case "while":
		return max1(exprMax(s.Cond, 0), stmtsMax(s.Body))
	default:
		return -1
	}
}

func exprMax(n *ir.Node, lv int) int {
	if n == nil {
		return -1
	}
	switch n.Op {
	case "int", "bool", "name", "strptr", "strlen", "sizeof", "len":
		return -1
	case "index":
		return exprMax(n.Arg, lv)
	case "bload":
		return max2(max2(exprMax(n.Base, lv), exprMax(n.Left, lv+1), lv+1), exprMax(n.Right, lv+1), lv+1)
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr", "ushr", "umulhi", "udiv", "urem", "ult", "eq", "ne", "lt", "le", "gt", "ge":
		return max2(exprMax(n.Left, lv), exprMax(n.Right, lv+1), lv)
	case "land", "lor":
		return max1(exprMax(n.Left, lv), exprMax(n.Right, lv))
	case "not", "neg", "bnot", "cast", "load8", "load16", "load32", "load64", "bswap16", "bswap32", "bswap64":
		return exprMax(n.Arg, lv)
	case "field":
		return exprMax(n.Base, lv)
	case "call", "syscall":
		m := -1
		for i, a := range n.Args {
			m = max1(m, exprMax(a, lv+i))
			m = max1(m, lv+i)
		}
		return m
	default:
		return lv
	}
}

func max1(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func max2(a, b, c int) int { return max1(a, max1(b, c)) }

func (c *cg) tempDisp(lv int) int32 {
	return -(c.localBytes + int32((lv+1)*8))
}

func (c *cg) storeTemp(lv int) { c.b.MovMemRbpRax(c.tempDisp(lv)) }

func (c *cg) loadTempRax(lv int) { c.b.MovRaxMemRbp(c.tempDisp(lv)) }

func (c *cg) loadTempRcx(lv int) { c.b.MovRcxMemRbp(c.tempDisp(lv)) }

func (c *cg) loadTempReg(reg, lv int) { c.b.MovRegMemRbp(reg, c.tempDisp(lv)) }

func (c *cg) emitStmts(stmts []*ir.Node) error {
	for _, s := range stmts {
		if s == nil {
			continue
		}
		c.mark(s.ID)
		if err := c.emitStmt(s); err != nil {
			return err
		}
		// Code after a nested block (a loop's jump back) is the statement's.
		if len(s.Then) > 0 || len(s.Else) > 0 || len(s.Body) > 0 {
			c.mark(s.ID)
		}
	}
	return nil
}

func (c *cg) emitStmt(s *ir.Node) error {
	switch s.Op {
	case "var", "assign":
		i, ok := c.ref[s]
		if !ok {
			return fmt.Errorf("%s %s", s.Op, s.Name)
		}
		if s.Val == nil && s.Op == "var" {
			// A declaration without a value makes the local zero, each
			// time it runs: the frame is not cleared on entry, and the
			// slot may have been a sibling block's.
			if r, ok := c.regs[i]; ok {
				c.b.MovRegImm(r, 0)
			} else {
				c.b.MovMemRbpImm(c.locals[i].disp, 0)
			}
			if c.qloc == i {
				c.qmark = -1
			}
			return nil
		}
		if err := c.emitAssign(i, s.Val); err != nil {
			return err
		}
		if c.qloc == i {
			c.qmark = -1
		}
		return nil
	case "var2", "assign2":
		// The call leaves its results in rax and rdx (and rcx for a
		// lowered (bytes, i64)), which no local lives in, so storing one
		// cannot disturb another.
		if err := c.emitExpr(s.Val, 0); err != nil {
			return err
		}
		if k, ok := c.ref3[s]; ok {
			c.setLocal(k, asm.RCX)
		}
		if j, ok := c.ref2[s]; ok {
			c.setLocal(j, asm.RDX)
		}
		if i, ok := c.ref[s]; ok {
			c.setLocal(i, asm.RAX)
		}
		return nil
	case "setfield":
		off, err := c.fieldOff(s.Base, s.Name)
		if err != nil {
			return err
		}
		return c.emitStore(s.Base, s.Val, 64, off)
	case "store8", "store16", "store32", "store64":
		return c.emitStore(s.Addr, s.Val, storeWidth[s.Op], 0)
	case "chk":
		// A lowered bounds check: Addr < Val unsigned, or the trap.
		cc, err := c.emitCmp(&ir.Node{Op: "ult", Left: s.Addr, Right: s.Val}, 0)
		if err != nil {
			return err
		}
		c.b.Jcc(0x80|(cc^1), c.trapLabel())
		return nil
	case "expr":
		return c.emitExpr(s.Val, 0)
	case "return":
		if len(s.Args) == 1 {
			// A lowered return b, e: the error code into rcx by way of
			// temp 0, the length into rdx by way of temp 1, the address
			// into rax.
			if err := c.emitExpr(s.Args[0], 0); err != nil {
				return err
			}
			c.storeTemp(0)
			if err := c.emitExpr(s.Val2, 1); err != nil {
				return err
			}
			c.storeTemp(1)
			if err := c.emitExpr(s.Val, 2); err != nil {
				return err
			}
			c.loadTempReg(asm.RDX, 1)
			c.loadTempReg(asm.RCX, 0)
		} else if s.Val2 != nil {
			// return v, e: e into rdx by way of temp 0, then v into rax.
			if err := c.emitExpr(s.Val2, 0); err != nil {
				return err
			}
			c.storeTemp(0)
			if err := c.emitExpr(s.Val, 1); err != nil {
				return err
			}
			c.loadTempReg(asm.RDX, 0)
		} else if s.Val != nil {
			if err := c.emitExpr(s.Val, 0); err != nil {
				return err
			}
		} else {
			c.b.XorRaxRax()
		}
		// The func's last statement falls into the epilogue.
		if s != c.last {
			c.b.Jmp(c.epi)
		}
		return nil
	case "if":
		end := c.b.NewLabel()
		elseL := end
		if len(s.Else) > 0 {
			elseL = c.b.NewLabel()
		}
		if err := c.emitJump(s.Cond, 0, elseL, false); err != nil {
			return err
		}
		if err := c.emitStmts(s.Then); err != nil {
			return err
		}
		if len(s.Else) > 0 {
			c.b.Jmp(end)
			c.b.Mark(elseL)
			if err := c.emitStmts(s.Else); err != nil {
				return err
			}
		}
		c.b.Mark(end)
		return nil
	case "while":
		// The condition sits below the body, so each turn of the loop
		// takes one jump: jmp test; body: ...; test: if cond goto body.
		body := c.b.NewLabel()
		test := c.b.NewLabel()
		c.b.Jmp(test)
		// The body starts on a 32-byte boundary, so where the loop falls
		// does not decide how fast it runs. The padding follows a jmp and
		// never runs.
		for (elf.CodeVAddr()+uint64(c.b.Pos()))%32 != 0 {
			c.b.Int3()
		}
		c.b.Mark(body)
		if err := c.emitStmts(s.Body); err != nil {
			return err
		}
		// The test is the while statement's code, not its body's last
		// statement's: a fault in the condition is reported at the while.
		c.mark(s.ID)
		c.b.Mark(test)
		return c.emitJump(s.Cond, 0, body, true)
	default:
		return fmt.Errorf("stmt %s", s.Op)
	}
}

// An operand is a value that needs no code of its own: an instruction can
// name it. kImm is a constant that fits a signed 32-bit immediate; kMem is
// a local in the frame, by its rbp displacement; kReg is a local in a
// register, by the register's number.
const (
	kNone = iota
	kImm
	kMem
	kReg
)

// The x86 group-1 operations, by their /digit.
const (
	aluAdd = 0
	aluOr  = 1
	aluAnd = 4
	aluSub = 5
	aluXor = 6
	aluCmp = 7
)

func uncast(n *ir.Node) *ir.Node {
	for n != nil && n.Op == "cast" {
		n = n.Arg
	}
	return n
}

// operand classifies n: its kind, and the immediate or the displacement.
func (c *cg) operand(n *ir.Node) (int, int64) {
	n = uncast(n)
	if n == nil {
		return kNone, 0
	}
	var v int64
	switch n.Op {
	case "int":
		v = n.Int
	case "bool":
		if n.Bool {
			v = 1
		}
	case "strlen":
		v = int64(len(n.Str))
	case "sizeof":
		sz, err := c.sizeOf(n.Type)
		if err != nil {
			return kNone, 0
		}
		v = sz
	case "name":
		if n.Pkg != "" {
			pv, err := c.pkgConst(n)
			if err != nil {
				return kNone, 0
			}
			v = pv
		} else if i, ok := c.ref[n]; ok {
			if r, ok := c.regs[i]; ok {
				return kReg, int64(r)
			}
			return kMem, int64(c.locals[i].disp)
		} else if cv, ok := c.consts[n.Name]; ok {
			v = cv
		} else {
			return kNone, 0
		}
	default:
		return kNone, 0
	}
	if v < -0x80000000 || v > 0x7fffffff {
		return kNone, 0
	}
	return kImm, v
}

// loadOpnd moves an operand into reg.
func (c *cg) loadOpnd(reg, k int, v int64) {
	switch k {
	case kImm:
		c.b.MovRegImm(reg, v)
	case kMem:
		c.b.MovRegMemRbp(reg, int32(v))
	default:
		if reg != int(v) {
			c.b.MovRegReg(reg, int(v))
		}
	}
}

// aluOpnd emits op reg, operand for the group-1 operation n.
func (c *cg) aluOpnd(n, reg, k int, v int64) {
	switch k {
	case kImm:
		c.b.AluRegImm(n, reg, int32(v))
	case kMem:
		c.b.AluRegMem(n, reg, int32(v))
	default:
		c.b.AluRegReg(n, reg, int(v))
	}
}

func aluOf(op string) int {
	switch op {
	case "add":
		return aluAdd
	case "sub":
		return aluSub
	case "and":
		return aluAnd
	case "or":
		return aluOr
	case "xor":
		return aluXor
	}
	return -1
}

func commutes(op string) bool {
	return op == "add" || op == "mul" || op == "and" || op == "or" || op == "xor"
}

// arithOpnd emits rax = rax op operand.
func (c *cg) arithOpnd(op string, k int, v int64) {
	switch op {
	case "add", "sub", "and", "or", "xor":
		c.aluOpnd(aluOf(op), asm.RAX, k, v)
	case "mul":
		switch k {
		case kImm:
			c.b.ImulRaxImm(int32(v))
		case kMem:
			c.b.ImulRaxMem(int32(v))
		default:
			c.b.ImulRaxReg(int(v))
		}
	case "shl", "shr":
		if k == kImm {
			// The same count the cl form would use: the low six bits.
			c.b.ShiftRaxImm(op == "shr", byte(v&63))
			return
		}
		c.loadOpnd(asm.RCX, k, v)
		c.arithRcx(op)
	case "div", "mod":
		// Dividing by 0 or -1 keeps idiv, which traps where it should.
		if k == kImm && (v >= 2 || v <= -2) {
			c.divConst(op == "mod", v)
			return
		}
		c.loadOpnd(asm.RCX, k, v)
		c.arithRcx(op)
	default:
		c.loadOpnd(asm.RCX, k, v)
		c.arithRcx(op)
	}
}

// divConst emits rax = rax / d, or rax % d, truncating, for a constant d
// with |d| >= 2, without idiv. A power of two is a shift, after adding
// d-1 to a negative dividend; any other d is a multiply by a magic
// reciprocal (Hacker's Delight 10-1). x % d is x - (x/d)*d. Uses rcx and
// rdx.
func (c *cg) divConst(mod bool, d int64) {
	if d > 0 && d&(d-1) == 0 {
		k := byte(bits.TrailingZeros64(uint64(d)))
		// rcx = x + (x < 0 ? d-1 : 0)
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.b.SarRegImm(asm.RCX, 63)
		c.b.ShrRegImm(asm.RCX, 64-k)
		c.b.AluRegReg(aluAdd, asm.RCX, asm.RAX)
		if mod {
			c.b.AluRegImm(aluAnd, asm.RCX, int32(-d))
			c.b.AluRegReg(aluSub, asm.RAX, asm.RCX)
			return
		}
		c.b.MovRegReg(asm.RAX, asm.RCX)
		c.b.SarRegImm(asm.RAX, k)
		return
	}
	c.quotient(d)
	if mod {
		c.remainder(d)
	}
}

// quotient emits rcx = rax and rax = rax / d, for a d that magic takes.
// Uses rdx.
func (c *cg) quotient(d int64) {
	m, s := magic(d)
	c.b.MovRegReg(asm.RCX, asm.RAX)
	c.b.MovRegImm(asm.RAX, m)
	c.b.ImulRcx()
	if d > 0 && m < 0 {
		c.b.AluRegReg(aluAdd, asm.RDX, asm.RCX)
	} else if d < 0 && m > 0 {
		c.b.AluRegReg(aluSub, asm.RDX, asm.RCX)
	}
	if s > 0 {
		c.b.SarRegImm(asm.RDX, s)
	}
	// The quotient rounds toward zero: add one when it is negative.
	c.b.MovRegReg(asm.RAX, asm.RDX)
	c.b.ShrRegImm(asm.RAX, 63)
	c.b.AluRegReg(aluAdd, asm.RAX, asm.RDX)
}

// remainder emits rax = rcx - rax*d: the remainder of rcx by d, when
// rax is their quotient.
func (c *cg) remainder(d int64) {
	c.b.ImulRaxImm(int32(d))
	c.b.AluRegReg(aluSub, asm.RCX, asm.RAX)
	c.b.MovRegReg(asm.RAX, asm.RCX)
}

// emitQuot emits x / d or x % d of local i, which is n's left side,
// dividing only when qreg does not have the quotient already.
func (c *cg) emitQuot(n *ir.Node, i int, d int64) {
	k, v := c.operand(n.Left)
	if c.qmark == c.b.Marks && c.qloc == i && c.qdiv == d {
		c.b.MovRegReg(asm.RAX, c.qreg)
		if n.Op == "mod" {
			c.loadOpnd(asm.RCX, k, v)
			c.remainder(d)
		}
		return
	}
	c.loadOpnd(asm.RAX, k, v)
	c.quotient(d)
	c.b.MovRegReg(c.qreg, asm.RAX)
	c.qloc, c.qdiv, c.qmark = i, d, c.b.Marks
	if n.Op == "mod" {
		c.remainder(d)
	}
}

// magic is the multiplier m and the shift s for signed division by d,
// 2 <= |d| <= 2^31 and d not a positive power of two: x/d is the high
// half of x*m, plus x when d > 0 > m or minus x when d < 0 < m, shifted
// right by s, plus one if negative (Hacker's Delight 10-1). m is
// 2^p/|d| + 1 for the least p >= 64 with 2^p > nc*e, where e is
// |d| - 2^p mod |d|, and nc is 2^63 - 1 - 2^63 mod |d| for d > 0 and
// 2^63 - (2^63+1) mod |d| for d < 0. As e < 2^31, that is 2^(p-63) > e,
// or 2^(p-63) = e with nc < 2^63, so the arithmetic fits in signed 64
// bits, the only kind the self-hosted compiler has.
func magic(d int64) (int64, byte) {
	ad := d
	if d < 0 {
		ad = -d
	}
	// 2^p = q*ad + r, with q modulo 2^64.
	q, r := int64(0), int64(1)
	exact := false
	for p := 1; ; p++ {
		q, r = 2*q, 2*r
		if r >= ad {
			q, r = q+1, r-ad
		}
		if p == 63 {
			// nc is 2^63 when d < 0 and |d| divides 2^63+1.
			exact = d < 0 && (r+1)%ad == 0
		}
		if p < 64 {
			continue
		}
		e, j := ad-r, int64(1)<<(p-63)
		if j > e || (j == e && !exact) {
			m := q + 1
			if d < 0 {
				m = -m
			}
			return m, byte(p - 64)
		}
	}
}

// arithRcx emits rax = rax op rcx.
func (c *cg) arithRcx(op string) {
	switch op {
	case "add":
		c.b.AddRaxRcx()
	case "sub":
		c.b.SubRaxRcx()
	case "mul":
		c.b.ImulRaxRcx()
	case "and":
		c.b.AndRaxRcx()
	case "or":
		c.b.OrRaxRcx()
	case "xor":
		c.b.XorRaxRcx()
	case "shl":
		c.b.ShlRaxCl()
	case "shr":
		c.b.SarRaxCl()
	case "div":
		c.b.Cqo()
		c.b.IdivRcx()
	case "mod":
		c.b.Cqo()
		c.b.IdivRcx()
		c.b.MovRegReg(asm.RAX, asm.RDX)
	case "ushr":
		c.b.ShrRaxCl()
	case "umulhi":
		c.b.MulRcx()
		c.b.MovRegReg(asm.RAX, asm.RDX)
	case "udiv":
		c.b.XorEdxEdx()
		c.b.DivRcx()
	case "urem":
		c.b.XorEdxEdx()
		c.b.DivRcx()
		c.b.MovRegReg(asm.RAX, asm.RDX)
	}
}

// emitArith evaluates a binary arithmetic node into rax. A side that is an
// operand costs no temp; evaluating the other side first is safe because
// nothing an expression does can change a local or a constant. Otherwise
// the left side waits in a temp: a register when tempReg has one, else a
// slot in the frame.
func (c *cg) emitArith(n *ir.Node, lv int) error {
	if i, d, ok := c.quotOf(n); ok && c.qreg >= 0 {
		c.emitQuot(n, i, d)
		return nil
	}
	if k, v := c.operand(n.Right); k != kNone {
		if err := c.emitExpr(n.Left, lv); err != nil {
			return err
		}
		c.arithOpnd(n.Op, k, v)
		return nil
	}
	if k, v := c.operand(n.Left); k != kNone {
		if err := c.emitExpr(n.Right, lv); err != nil {
			return err
		}
		if commutes(n.Op) {
			c.arithOpnd(n.Op, k, v)
			return nil
		}
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.loadOpnd(asm.RAX, k, v)
		c.arithRcx(n.Op)
		return nil
	}
	if r, ok := c.tempReg(lv, n.Right); ok {
		if err := c.emitExpr(n.Left, lv); err != nil {
			return err
		}
		c.b.MovRegReg(r, asm.RAX)
		if err := c.emitExpr(n.Right, lv+1); err != nil {
			return err
		}
		if commutes(n.Op) {
			c.arithOpnd(n.Op, kReg, int64(r))
			return nil
		}
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.b.MovRegReg(asm.RAX, r)
		c.arithRcx(n.Op)
		return nil
	}
	if err := c.emitExpr(n.Left, lv); err != nil {
		return err
	}
	c.storeTemp(lv)
	if err := c.emitExpr(n.Right, lv+1); err != nil {
		return err
	}
	if commutes(n.Op) {
		c.arithOpnd(n.Op, kMem, int64(c.tempDisp(lv)))
		return nil
	}
	c.b.MovRegReg(asm.RCX, asm.RAX)
	c.loadTempRax(lv)
	c.arithRcx(n.Op)
	return nil
}

// Condition codes: the low nibble shared by setcc (0F 9x) and jcc (0F 8x).
// Flipping bit 0 negates one.
func ccOf(op string) byte {
	return map[string]byte{"eq": 0x4, "ne": 0x5, "lt": 0xC, "ge": 0xD, "le": 0xE, "gt": 0xF, "ult": 0x2}[op]
}

// ccSwap is the condition for the operands the other way round.
func ccSwap(cc byte) byte {
	switch cc {
	case 0x2:
		return 0x7
	case 0x7:
		return 0x2
	case 0xC:
		return 0xF
	case 0xF:
		return 0xC
	case 0xD:
		return 0xE
	case 0xE:
		return 0xD
	}
	return cc
}

// emitCmp sets the flags for a comparison node and returns the condition
// code under which it is true.
func (c *cg) emitCmp(n *ir.Node, lv int) (byte, error) {
	cc := ccOf(n.Op)
	if k, v := c.operand(n.Right); k != kNone {
		// A local in a register is compared where it is.
		if kl, vl := c.operand(n.Left); kl == kReg {
			c.aluOpnd(aluCmp, int(vl), k, v)
			return cc, nil
		}
		if err := c.emitExpr(n.Left, lv); err != nil {
			return 0, err
		}
		c.aluOpnd(aluCmp, asm.RAX, k, v)
		return cc, nil
	}
	if k, v := c.operand(n.Left); k != kNone {
		if err := c.emitExpr(n.Right, lv); err != nil {
			return 0, err
		}
		c.aluOpnd(aluCmp, asm.RAX, k, v)
		return ccSwap(cc), nil
	}
	if r, ok := c.tempReg(lv, n.Right); ok {
		if err := c.emitExpr(n.Left, lv); err != nil {
			return 0, err
		}
		c.b.MovRegReg(r, asm.RAX)
		if err := c.emitExpr(n.Right, lv+1); err != nil {
			return 0, err
		}
		c.b.AluRegReg(aluCmp, r, asm.RAX)
		return cc, nil
	}
	if err := c.emitExpr(n.Left, lv); err != nil {
		return 0, err
	}
	c.storeTemp(lv)
	if err := c.emitExpr(n.Right, lv+1); err != nil {
		return 0, err
	}
	c.b.AluMemReg(aluCmp, c.tempDisp(lv), asm.RAX)
	return cc, nil
}

// tempReg is the register temp level lv may use while right is evaluated:
// none when right calls something, which may clobber it.
func (c *cg) tempReg(lv int, right *ir.Node) (int, bool) {
	if lv >= len(c.tregs) || exprCalls(right) {
		return 0, false
	}
	return c.tregs[lv], true
}

func isCmp(op string) bool {
	switch op {
	case "eq", "ne", "lt", "le", "gt", "ge", "ult":
		return true
	}
	return false
}

// emitJump jumps to lab when the condition n is when, and falls through
// otherwise, without making the bool a value.
func (c *cg) emitJump(n *ir.Node, lv int, lab int, when bool) error {
	n = uncast(n)
	if n == nil {
		return fmt.Errorf("nil condition in %s.%s", c.pkg.Path, c.fn.Name)
	}
	switch {
	case isCmp(n.Op):
		cc, err := c.emitCmp(n, lv)
		if err != nil {
			return err
		}
		if !when {
			cc ^= 1
		}
		c.b.Jcc(0x80|cc, lab)
		return nil
	case n.Op == "not":
		return c.emitJump(n.Arg, lv, lab, !when)
	case n.Op == "land" && !when, n.Op == "lor" && when:
		// Either side decides: both jump to lab.
		if err := c.emitJump(n.Left, lv, lab, when); err != nil {
			return err
		}
		return c.emitJump(n.Right, lv, lab, when)
	case n.Op == "land", n.Op == "lor":
		// The left side can only rule the jump out.
		skip := c.b.NewLabel()
		if err := c.emitJump(n.Left, lv, skip, !when); err != nil {
			return err
		}
		if err := c.emitJump(n.Right, lv, lab, when); err != nil {
			return err
		}
		c.b.Mark(skip)
		return nil
	case n.Op == "bool":
		if n.Bool == when {
			c.b.Jmp(lab)
		}
		return nil
	}
	if err := c.emitExpr(n, lv); err != nil {
		return err
	}
	c.b.TestRaxRax()
	if when {
		c.b.Jnz(lab)
	} else {
		c.b.Jz(lab)
	}
	return nil
}

// emitAddr makes an address usable as a memory operand and returns its
// base register, index register (-1 for none), and displacement. Locals
// in registers serve as they are; anything else is evaluated into rax.
func (c *cg) emitAddr(n *ir.Node, lv int) (int, int, int32, error) {
	n, d := c.splitAddr(n)
	if base, index, ok := c.addrMode(n); ok {
		return base, index, d, nil
	}
	// Any other base plus an index: the base is evaluated into rax.
	if n != nil && n.Op == "add" {
		if index, ok := c.indexOf(n.Right); ok {
			return asm.RAX, index, d, c.emitExpr(n.Left, lv)
		}
		if index, ok := c.indexOf(n.Left); ok {
			return asm.RAX, index, d, c.emitExpr(n.Right, lv)
		}
	}
	return asm.RAX, -1, d, c.emitExpr(n, lv)
}

// addrMode reports whether n, an address with its constant terms taken
// off, is a local in a register, or one plus an index (see indexOf).
func (c *cg) addrMode(n *ir.Node) (int, int, bool) {
	if k, v := c.operand(n); k == kReg {
		return int(v), -1, true
	}
	if n != nil && n.Op == "add" {
		if k, v := c.operand(n.Left); k == kReg {
			if index, ok := c.indexOf(n.Right); ok {
				return int(v), index, true
			}
		}
		if k, v := c.operand(n.Right); k == kReg {
			if index, ok := c.indexOf(n.Left); ok {
				return int(v), index, true
			}
		}
	}
	return 0, -1, false
}

// indexOf reports whether n can be an address's index: a local in a
// register, or one times 2, 4, or 8 or shifted left by 1 to 3. The
// result is the register, with the shift in bits 4 and 5 for asm.
func (c *cg) indexOf(n *ir.Node) (int, bool) {
	if k, v := c.operand(n); k == kReg {
		return int(v), true
	}
	n = uncast(n)
	if n == nil || (n.Op != "mul" && n.Op != "shl") {
		return 0, false
	}
	r, s := n.Left, n.Right
	if k, _ := c.operand(r); k != kReg && n.Op == "mul" {
		r, s = s, r
	}
	kr, vr := c.operand(r)
	ks, vs := c.operand(s)
	if kr != kReg || ks != kImm {
		return 0, false
	}
	sh := int64(0)
	if n.Op == "shl" {
		sh = vs
	} else if vs == 2 || vs == 4 || vs == 8 {
		sh = int64(bits.TrailingZeros64(uint64(vs)))
	}
	if sh < 1 || sh > 3 {
		return 0, false
	}
	return int(vr) | int(sh)<<4, true
}

// splitAddr peels the constant terms off an address expression.
func (c *cg) splitAddr(n *ir.Node) (*ir.Node, int32) {
	d := int64(0)
	n = uncast(n)
	for n != nil && (n.Op == "add" || n.Op == "sub") {
		k, v := c.operand(n.Right)
		if k != kImm {
			break
		}
		if n.Op == "sub" {
			v = -v
		}
		if d+v < -0x80000000 || d+v > 0x7fffffff {
			break
		}
		d += v
		n = uncast(n.Left)
	}
	return n, int32(d)
}

// The widths of the memory builtins, in bits.
var (
	loadWidth  = map[string]int{"load8": 8, "load16": 16, "load32": 32, "load64": 64}
	storeWidth = map[string]int{"store8": 8, "store16": 16, "store32": 32, "store64": 64}
)

// emitStore stores val, width bits of it, at addr + off.
func (c *cg) emitStore(addr, val *ir.Node, width int, off int32) error {
	if k, v := c.operand(val); k != kNone {
		base, index, d, err := c.emitAddr(addr, 0)
		if err != nil {
			return err
		}
		switch k {
		case kImm:
			c.b.StoreMemImm(width, base, index, off+d, int32(v))
		case kReg:
			c.b.StoreMemReg(width, int(v), base, index, off+d)
		default:
			c.b.MovRcxMemRbp(int32(v))
			c.b.StoreMemReg(width, asm.RCX, base, index, off+d)
		}
		return nil
	}
	n, d := c.splitAddr(addr)
	if base, index, ok := c.addrMode(n); ok {
		if err := c.emitExpr(val, 0); err != nil {
			return err
		}
		c.b.StoreMemReg(width, asm.RAX, base, index, off+d)
		return nil
	}
	// Any other base plus an index: only the base goes through rcx.
	index := -1
	if n != nil && n.Op == "add" {
		if x, ok := c.indexOf(n.Right); ok {
			n, index = n.Left, x
		} else if x, ok := c.indexOf(n.Left); ok {
			n, index = n.Right, x
		}
	}
	if k, v := c.operand(n); k == kMem {
		if err := c.emitExpr(val, 0); err != nil {
			return err
		}
		c.b.MovRcxMemRbp(int32(v))
		c.b.StoreMemReg(width, asm.RAX, asm.RCX, index, off+d)
		return nil
	}
	if err := c.emitExpr(n, 0); err != nil {
		return err
	}
	if r, ok := c.tempReg(0, val); ok {
		c.b.MovRegReg(r, asm.RAX)
		if err := c.emitExpr(val, 1); err != nil {
			return err
		}
		c.b.StoreMemReg(width, asm.RAX, r, index, off+d)
		return nil
	}
	c.storeTemp(0)
	if err := c.emitExpr(val, 1); err != nil {
		return err
	}
	c.loadTempRcx(0)
	c.b.StoreMemReg(width, asm.RAX, asm.RCX, index, off+d)
	return nil
}

// setLocal writes register src to local i, and forgets its cached quotient.
func (c *cg) setLocal(i, src int) {
	if r, ok := c.regs[i]; ok {
		if r != src {
			c.b.MovRegReg(r, src)
		}
	} else {
		c.b.MovMemRbpReg(src, c.locals[i].disp)
	}
	if c.qloc == i {
		c.qmark = -1
	}
}

// emitAssign sets local i to val.
func (c *cg) emitAssign(i int, val *ir.Node) error {
	reg, inReg := c.regs[i]
	disp := c.locals[i].disp
	k, v := c.operand(val)
	if inReg && k != kNone {
		c.loadOpnd(reg, k, v)
		return nil
	}
	if k == kImm {
		c.b.MovMemRbpImm(disp, int32(v))
		return nil
	}
	if k == kReg {
		c.b.MovMemRbpReg(int(v), disp)
		return nil
	}
	// x = x op operand works on x in place.
	if n := uncast(val); n != nil && aluOf(n.Op) >= 0 {
		if j, ok := c.ref[uncast(n.Left)]; ok && j == i {
			if k, v := c.operand(n.Right); k != kNone {
				switch {
				case inReg:
					c.aluOpnd(aluOf(n.Op), reg, k, v)
				case k == kImm:
					c.b.AluMemImm(aluOf(n.Op), disp, int32(v))
				case k == kReg:
					c.b.AluMemReg(aluOf(n.Op), disp, int(v))
				default:
					c.b.MovRaxMemRbp(int32(v))
					c.b.AluMemReg(aluOf(n.Op), disp, asm.RAX)
				}
				return nil
			}
		}
	}
	if inReg && isLoad(val) {
		return c.emitLoad(val, 0, reg)
	}
	if err := c.emitExpr(val, 0); err != nil {
		return err
	}
	if inReg {
		c.b.MovRegReg(reg, asm.RAX)
	} else {
		c.b.MovMemRbpRax(disp)
	}
	return nil
}

// emitArgs evaluates a call's arguments into regs. An argument that is an
// operand is loaded last, straight into its register; the others go
// through temps, except the last of them, which is still in rax.
func (c *cg) emitArgs(args []*ir.Node, regs []int, lv int) error {
	kinds := make([]int, len(args))
	vals := make([]int64, len(args))
	last := -1
	for i, a := range args {
		kinds[i], vals[i] = c.operand(a)
		if kinds[i] == kNone {
			last = i
		}
	}
	for i, a := range args {
		if kinds[i] != kNone {
			continue
		}
		// The last of them, if it reads memory, goes straight into its
		// register: no other argument's register is set yet.
		if i == last && regs[i] != asm.RAX && isLoad(a) {
			if err := c.emitLoad(a, lv+i, regs[i]); err != nil {
				return err
			}
			continue
		}
		if err := c.emitExpr(a, lv+i); err != nil {
			return err
		}
		if i != last {
			c.storeTemp(lv + i)
		} else if regs[i] != asm.RAX {
			c.b.MovRegReg(regs[i], asm.RAX)
		}
	}
	for i := range args {
		if kinds[i] == kNone && i != last {
			c.loadTempReg(regs[i], lv+i)
		}
	}
	for i := range args {
		if kinds[i] != kNone {
			c.loadOpnd(regs[i], kinds[i], vals[i])
		}
	}
	return nil
}

func (c *cg) emitExpr(n *ir.Node, lv int) error {
	if n == nil {
		return fmt.Errorf("nil expr in %s.%s", c.pkg.Path, c.fn.Name)
	}
	switch n.Op {
	case "int":
		c.b.MovRegImm(asm.RAX, n.Int)
		return nil
	case "sizeof":
		sz, err := c.sizeOf(n.Type)
		if err != nil {
			return err
		}
		c.b.MovRegImm(asm.RAX, sz)
		return nil
	case "bool":
		v := int64(0)
		if n.Bool {
			v = 1
		}
		c.b.MovRegImm(asm.RAX, v)
		return nil
	case "strptr":
		off := c.intern(n.Str)
		c.b.MovRegImm64(asm.RAX, 0)
		c.b.AbsImmLabel(off)
		return nil
	case "len":
		_, cnt, err := c.table(n)
		if err != nil {
			return err
		}
		c.b.MovRegImm(asm.RAX, int64(cnt))
		return nil
	case "index":
		// rax = table[rax], after an unsigned check of rax against the
		// length, so a negative index trips it too; the trap is the
		// func's ud2.
		off, cnt, err := c.table(n)
		if err != nil {
			return err
		}
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.MovRegImm64(asm.RCX, 0)
		c.b.AbsImmLabel(off)
		c.b.AluRegImm(aluCmp, asm.RAX, int32(cnt))
		c.b.Jcc(0x83, c.trapLabel())
		c.b.LoadMem(64, asm.RCX, asm.RAX|3<<4, 0)
		return nil
	case "strlen":
		c.b.MovRegImm(asm.RAX, int64(len(n.Str)))
		return nil
	case "name":
		if n.Pkg != "" {
			v, err := c.pkgConst(n)
			if err != nil {
				return err
			}
			c.b.MovRegImm(asm.RAX, v)
			return nil
		}
		if i, ok := c.ref[n]; ok {
			if r, ok := c.regs[i]; ok {
				c.b.MovRegReg(asm.RAX, r)
			} else {
				c.b.MovRaxMemRbp(c.locals[i].disp)
			}
			return nil
		}
		if v, ok := c.consts[n.Name]; ok {
			c.b.MovRegImm(asm.RAX, v)
			return nil
		}
		return fmt.Errorf("name %s", n.Name)
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr", "ushr", "umulhi", "udiv", "urem":
		return c.emitArith(n, lv)
	case "eq", "ne", "lt", "le", "gt", "ge", "ult":
		cc, err := c.emitCmp(n, lv)
		if err != nil {
			return err
		}
		c.b.SetccAl(0x90 | cc)
		c.b.MovzxRaxAl()
		return nil
	case "land":
		no := c.b.NewLabel()
		end := c.b.NewLabel()
		if err := c.emitJump(n.Left, lv, no, false); err != nil {
			return err
		}
		if err := c.emitExpr(n.Right, lv); err != nil {
			return err
		}
		c.b.Jmp(end)
		c.b.Mark(no)
		c.b.XorRaxRax()
		c.b.Mark(end)
		return nil
	case "lor":
		yes := c.b.NewLabel()
		end := c.b.NewLabel()
		if err := c.emitJump(n.Left, lv, yes, true); err != nil {
			return err
		}
		if err := c.emitExpr(n.Right, lv); err != nil {
			return err
		}
		c.b.Jmp(end)
		c.b.Mark(yes)
		c.b.MovRegImm(asm.RAX, 1)
		c.b.Mark(end)
		return nil
	case "not":
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.TestRaxRax()
		c.b.SetccAl(0x94)
		c.b.MovzxRaxAl()
		return nil
	case "neg":
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.NegRax()
		return nil
	case "bnot":
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.NotRax()
		return nil
	case "cast":
		return c.emitExpr(n.Arg, lv)
	case "field", "load8", "load16", "load32", "load64":
		return c.emitLoad(n, lv, asm.RAX)
	case "bload":
		// A lowered b[i]: b is (Base, Right) and i is Left, all simple
		// operands, so cmp i, n; jae trap; movzx rax, [p + i].
		cc, err := c.emitCmp(&ir.Node{Op: "ult", Left: n.Left, Right: n.Right}, lv)
		if err != nil {
			return err
		}
		c.b.Jcc(0x80|(cc^1), c.trapLabel())
		return c.emitLoad(&ir.Node{Op: "load8", Arg: &ir.Node{Op: "add", Left: n.Base, Right: n.Left}}, lv, asm.RAX)
	case "bswap16", "bswap32", "bswap64":
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.BswapRax(map[string]int{"bswap16": 16, "bswap32": 32, "bswap64": 64}[n.Op])
		return nil
	case "call":
		path := n.Pkg
		if path == "" {
			path = c.pkg.Path
		}
		lab, ok := c.funcLabel[path+"."+n.Func]
		if !ok {
			return fmt.Errorf("call %s.%s", path, n.Func)
		}
		if len(n.Args) > 6 {
			return fmt.Errorf("arity %s", n.Func)
		}
		if err := c.emitArgs(n.Args, argRegs, lv); err != nil {
			return err
		}
		c.b.Call(lab)
		if c.qcall {
			c.qmark = -1
		}
		return nil
	case "syscall":
		if len(n.Args) != 7 {
			return fmt.Errorf("syscall arity")
		}
		// The number is the first argument; record it for the receipt.
		if k, v := c.operand(n.Args[0]); k == kImm {
			c.sys[v] = true
		} else {
			c.sysUnknown++
		}
		if err := c.emitArgs(n.Args, []int{asm.RAX, asm.RDI, asm.RSI, asm.RDX, asm.R10, asm.R8, asm.R9}, lv); err != nil {
			return err
		}
		c.b.Syscall()
		if c.qcall {
			c.qmark = -1
		}
		return nil
	default:
		return fmt.Errorf("expr %s", n.Op)
	}
}

// isLoad reports whether n reads memory and nothing else: a field or a
// load, which emitLoad can put in any register.
func isLoad(n *ir.Node) bool {
	n = uncast(n)
	return n != nil && (n.Op == "field" || loadWidth[n.Op] != 0)
}

// emitLoad evaluates n, a field or a load, into dst. Its address may be
// evaluated into rax first.
func (c *cg) emitLoad(n *ir.Node, lv int, dst int) error {
	n = uncast(n)
	if n.Op == "field" {
		off, err := c.fieldOff(n.Base, n.Name)
		if err != nil {
			return err
		}
		base, index, d, err := c.emitAddr(n.Base, lv)
		if err != nil {
			return err
		}
		c.b.LoadMemReg(64, dst, base, index, off+d)
		return nil
	}
	base, index, d, err := c.emitAddr(n.Arg, lv)
	if err != nil {
		return err
	}
	c.b.LoadMemReg(loadWidth[n.Op], dst, base, index, d)
	return nil
}

// sizeOf is the byte size of struct type t (pkg.T): 8 per field.
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

func (c *cg) fieldOff(base *ir.Node, field string) (int32, error) {
	bt := c.typeOf(base)
	if !strings.HasPrefix(bt, "*") {
		return 0, fmt.Errorf("field %s on %s", field, bt)
	}
	full := strings.TrimPrefix(bt, "*")
	i := strings.LastIndex(full, ".")
	if i < 0 {
		return 0, fmt.Errorf("type %s", bt)
	}
	pkg := c.pkgs[full[:i]]
	if pkg == nil {
		return 0, fmt.Errorf("pkg %s", full[:i])
	}
	name := full[i+1:]
	for _, t := range pkg.Types {
		if t.Name != name {
			continue
		}
		for i, f := range t.Fields {
			if f.Name == field {
				return int32(i * 8), nil
			}
		}
	}
	return 0, fmt.Errorf("field %s.%s", name, field)
}

func (c *cg) typeOf(n *ir.Node) string {
	if n == nil {
		return "invalid"
	}
	switch n.Op {
	case "int", "strptr", "strlen", "sizeof", "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr", "ushr", "umulhi", "udiv", "urem", "neg", "bnot", "index", "len", "load8", "load16", "load32", "load64", "bswap16", "bswap32", "bswap64", "syscall", "bload":
		return "i64"
	case "bool", "eq", "ne", "lt", "le", "gt", "ge", "ult", "land", "lor", "not":
		return "bool"
	case "name":
		if n.Pkg != "" {
			return "i64"
		}
		if i, ok := c.ref[n]; ok {
			return c.locals[i].typ
		}
		if _, ok := c.consts[n.Name]; ok {
			return "i64"
		}
		return "invalid"
	case "cast":
		return c.resolve(c.pkg, n.Type)
	case "field":
		bt := c.typeOf(n.Base)
		ft, err := c.fieldType(bt, n.Name)
		if err != nil {
			return "invalid"
		}
		return ft
	case "call":
		path := n.Pkg
		if path == "" {
			path = c.pkg.Path
		}
		if sg, ok := c.sigs[path+"."+n.Func]; ok {
			return sg.result
		}
		return "invalid"
	default:
		return "invalid"
	}
}

func (c *cg) fieldType(baseType, field string) (string, error) {
	if !strings.HasPrefix(baseType, "*") {
		return "", fmt.Errorf("not ptr")
	}
	full := strings.TrimPrefix(baseType, "*")
	i := strings.LastIndex(full, ".")
	pkg := c.pkgs[full[:i]]
	name := full[i+1:]
	for _, t := range pkg.Types {
		if t.Name != name {
			continue
		}
		for _, f := range t.Fields {
			if f.Name == field {
				return c.resolve(pkg, f.Type), nil
			}
		}
	}
	return "", fmt.Errorf("no field")
}

// pkgConst is the value of another package's const, path.Name.
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

// reachable is the set of funcs main can call, directly or not. Only these
// are emitted.
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
		var walk func(n *ir.Node)
		walk = func(n *ir.Node) {
			if n == nil {
				return
			}
			if n.Op == "call" {
				callee := n.Pkg
				if callee == "" {
					callee = pkg
				}
				work = append(work, callee+"."+n.Func)
			}
			for _, ch := range n.Children() {
				walk(ch)
			}
		}
		for _, st := range funcs[key].Body {
			walk(st)
		}
	}
	return live
}
