// Package compile lowers a checked Ovid program to a static x86-64 ELF.
// There is no libc and no implicit allocation. main receives *ovid/io.Cap.
package compile

import (
	"fmt"
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
	if p == nil {
		return nil, nil, fmt.Errorf("nil program")
	}
	c := &cg{
		prog:      p,
		strs:      map[string]int{},
		funcLabel: map[string]int{},
		sigs:      map[string]sig{},
		pkgs:      map[string]*ir.Package{},
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
	return elf.Link(c.b.Code, c.ro, 0), c.marks, nil
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
	funcLabel  map[string]int
	sigs       map[string]sig
	pkgs       map[string]*ir.Package
	locals     []local          // the func's params and vars, one per declaration
	ref        map[*ir.Node]int // a var, an assign, or a local's name: its local
	scope      []int            // the locals in scope while binding, innermost last
	live       int              // the most locals in scope at once
	consts     map[string]int64
	localBytes int32
	epi        int
	last       *ir.Node    // the func's final statement when it is a return
	regs       map[int]int // locals that live in a register
	saved      []int       // callee-saved registers the func uses
	saveBase   int32       // their save slots lie below this displacement
	pkg        *ir.Package
	fn         *ir.Func
	marks      []Mark
}

func (c *cg) mark(id string) {
	if n := len(c.marks); n > 0 && c.marks[n-1].Off == len(c.b.Code) {
		c.marks[n-1].ID = id
		return
	}
	c.marks = append(c.marks, Mark{len(c.b.Code), id})
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
	c.saveBase = -(c.localBytes + tempBytes)
	frame := c.localBytes + tempBytes + int32(8*len(c.saved))
	if frame%16 != 0 {
		frame += 8
	}
	lab := c.funcLabel[pkg.Path+"."+fn.Name]
	c.b.Mark(lab)
	c.mark(fn.ID)
	c.epi = c.b.NewLabel()
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
	return nil
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
		c.bindExpr(s.Base)
		c.bindExpr(s.Addr)
		c.bindExpr(s.Cond)
		switch s.Op {
		case "var":
			c.declare(s, s.Name, s.Type)
		case "assign":
			c.bindName(s, s.Name)
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
		c.weighExpr(s.Base, depth, w)
		c.weighExpr(s.Addr, depth, w)
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
		if exprCalls(s.Val) || exprCalls(s.Base) || exprCalls(s.Addr) || exprCalls(s.Cond) {
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
	case "var", "assign", "expr", "return":
		return exprMax(s.Val, 0)
	case "setfield":
		return max2(exprMax(s.Base, 0), exprMax(s.Val, 1), 1)
	case "store8", "store64":
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
	case "int", "bool", "name", "strptr", "strlen", "sizeof":
		return -1
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr", "eq", "ne", "lt", "le", "gt", "ge":
		return max2(exprMax(n.Left, lv), exprMax(n.Right, lv+1), lv)
	case "land", "lor":
		return max1(exprMax(n.Left, lv), exprMax(n.Right, lv))
	case "not", "neg", "bnot", "cast", "load8", "load32", "load64":
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
			return nil
		}
		return c.emitAssign(i, s.Val)
	case "setfield":
		off, err := c.fieldOff(s.Base, s.Name)
		if err != nil {
			return err
		}
		return c.emitStore(s.Base, s.Val, 64, off)
	case "store8":
		return c.emitStore(s.Addr, s.Val, 8, 0)
	case "store64":
		return c.emitStore(s.Addr, s.Val, 64, 0)
	case "expr":
		return c.emitExpr(s.Val, 0)
	case "return":
		if s.Val != nil {
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
	default:
		c.loadOpnd(asm.RCX, k, v)
		c.arithRcx(op)
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
	}
}

// emitArith evaluates a binary arithmetic node into rax. A side that is an
// operand costs no temp; evaluating the other side first is safe because
// nothing an expression does can change a local or a constant.
func (c *cg) emitArith(n *ir.Node, lv int) error {
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
	return map[string]byte{"eq": 0x4, "ne": 0x5, "lt": 0xC, "ge": 0xD, "le": 0xE, "gt": 0xF}[op]
}

// ccSwap is the condition for the operands the other way round.
func ccSwap(cc byte) byte {
	switch cc {
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

func isCmp(op string) bool {
	switch op {
	case "eq", "ne", "lt", "le", "gt", "ge":
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
	return asm.RAX, -1, d, c.emitExpr(n, lv)
}

// addrMode reports whether n, an address with its constant terms taken
// off, is a local in a register or the sum of two.
func (c *cg) addrMode(n *ir.Node) (int, int, bool) {
	if k, v := c.operand(n); k == kReg {
		return int(v), -1, true
	}
	if n != nil && n.Op == "add" {
		kl, vl := c.operand(n.Left)
		kr, vr := c.operand(n.Right)
		if kl == kReg && kr == kReg {
			return int(vl), int(vr), true
		}
	}
	return 0, -1, false
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
	if k, v := c.operand(n); k == kMem {
		if err := c.emitExpr(val, 0); err != nil {
			return err
		}
		c.b.MovRcxMemRbp(int32(v))
		c.b.StoreMemReg(width, asm.RAX, asm.RCX, -1, off+d)
		return nil
	}
	if err := c.emitExpr(n, 0); err != nil {
		return err
	}
	c.storeTemp(0)
	if err := c.emitExpr(val, 1); err != nil {
		return err
	}
	c.loadTempRcx(0)
	c.b.StoreMemReg(width, asm.RAX, asm.RCX, -1, off+d)
	return nil
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
		if err := c.emitExpr(a, lv+i); err != nil {
			return err
		}
		if i != last {
			c.storeTemp(lv + i)
		}
	}
	if last >= 0 && regs[last] != asm.RAX {
		c.b.MovRegReg(regs[last], asm.RAX)
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
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr":
		return c.emitArith(n, lv)
	case "eq", "ne", "lt", "le", "gt", "ge":
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
	case "field":
		off, err := c.fieldOff(n.Base, n.Name)
		if err != nil {
			return err
		}
		base, index, d, err := c.emitAddr(n.Base, lv)
		if err != nil {
			return err
		}
		c.b.LoadMem(64, base, index, off+d)
		return nil
	case "load8", "load32", "load64":
		base, index, d, err := c.emitAddr(n.Arg, lv)
		if err != nil {
			return err
		}
		c.b.LoadMem(map[string]int{"load8": 8, "load32": 32, "load64": 64}[n.Op], base, index, d)
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
		return nil
	case "syscall":
		if len(n.Args) != 7 {
			return fmt.Errorf("syscall arity")
		}
		if err := c.emitArgs(n.Args, []int{asm.RAX, asm.RDI, asm.RSI, asm.RDX, asm.R10, asm.R8, asm.R9}, lv); err != nil {
			return err
		}
		c.b.Syscall()
		return nil
	default:
		return fmt.Errorf("expr %s", n.Op)
	}
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
	case "int", "strptr", "strlen", "sizeof", "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr", "neg", "bnot", "load8", "load32", "load64", "syscall":
		return "i64"
	case "bool", "eq", "ne", "lt", "le", "gt", "ge", "land", "lor", "not":
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
