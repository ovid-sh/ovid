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

// heapSize is the bump heap the runtime maps for every program.
// The self-hosted compiler emits the same size.
const heapSize int64 = 128 << 20

// Compile emits a statically linked executable.
func Compile(p *ir.Program) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("nil program")
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
				return nil, fmt.Errorf("duplicate func %s", key)
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
		return nil, fmt.Errorf("missing %s", mainKey)
	}
	if err := c.emitStartup(c.funcLabel[mainKey]); err != nil {
		return nil, err
	}
	live := reachable(p, mainKey)
	for i := range p.Packages {
		pkg := &p.Packages[i]
		for fi := range pkg.Funcs {
			if !live[pkg.Path+"."+pkg.Funcs[fi].Name] {
				continue
			}
			if err := c.emitFunc(pkg, &pkg.Funcs[fi]); err != nil {
				return nil, err
			}
		}
	}
	if err := c.b.PatchRel(); err != nil {
		return nil, err
	}
	c.b.PatchAbs(elf.RodataVAddr(len(c.b.Code)))
	return elf.Link(c.b.Code, c.ro, 0), nil
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
	locals     map[string]int32
	consts     map[string]int64
	localBytes int32
	epi        int
	pkg        *ir.Package
	fn         *ir.Func
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
	c.b.MovRegImm64(asm.R10, 0x22)
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
	c.b.Mark(fail)
	c.b.MovRegImm64(asm.RDI, 125)
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
	c.locals = map[string]int32{}
	c.consts = map[string]int64{}
	for _, cn := range pkg.Consts {
		c.consts[cn.Name] = cn.Value
	}
	off := int32(8)
	for _, pa := range fn.Params {
		c.locals[pa.Name] = -off
		off += 8
	}
	var vars []named
	collectVars(fn.Body, &vars)
	for _, v := range vars {
		if _, ok := c.locals[v.name]; ok {
			// Sibling blocks may reuse a name. They share one slot.
			continue
		}
		c.locals[v.name] = -off
		off += 8
	}
	c.localBytes = off - 8
	peak := stmtsMax(fn.Body)
	tempBytes := int32(0)
	if peak >= 0 {
		tempBytes = int32((peak + 1) * 8)
	}
	frame := c.localBytes + tempBytes
	if frame%16 != 0 {
		frame += 8
	}
	lab := c.funcLabel[pkg.Path+"."+fn.Name]
	c.b.Mark(lab)
	c.epi = c.b.NewLabel()
	c.b.PushReg(asm.RBP)
	c.b.MovRegReg(asm.RBP, asm.RSP)
	c.b.SubRspImm(frame)
	if frame > 0 {
		c.b.XorRaxRax()
		for d := int32(8); d <= c.localBytes+tempBytes; d += 8 {
			c.b.MovMemRbpRax(-d)
		}
	}
	argRegs := []int{asm.RDI, asm.RSI, asm.RDX, asm.RCX, asm.R8, asm.R9}
	for i, pa := range fn.Params {
		c.b.MovMemRbpReg(argRegs[i], c.locals[pa.Name])
	}
	if err := c.emitStmts(fn.Body); err != nil {
		return err
	}
	c.b.XorRaxRax()
	c.b.Mark(c.epi)
	c.b.MovRegReg(asm.RSP, asm.RBP)
	c.b.PopReg(asm.RBP)
	c.b.Ret()
	return nil
}

type named struct{ name string }

func collectVars(stmts []*ir.Node, out *[]named) {
	for _, s := range stmts {
		if s == nil {
			continue
		}
		switch s.Op {
		case "var":
			*out = append(*out, named{s.Name})
		case "if":
			collectVars(s.Then, out)
			collectVars(s.Else, out)
		case "while":
			collectVars(s.Body, out)
		}
	}
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
		if err := c.emitStmt(s); err != nil {
			return err
		}
	}
	return nil
}

func (c *cg) emitStmt(s *ir.Node) error {
	switch s.Op {
	case "var":
		if s.Val == nil {
			return nil
		}
		if err := c.emitExpr(s.Val, 0); err != nil {
			return err
		}
		disp, ok := c.locals[s.Name]
		if !ok {
			return fmt.Errorf("var %s", s.Name)
		}
		c.b.MovMemRbpRax(disp)
		return nil
	case "assign":
		if err := c.emitExpr(s.Val, 0); err != nil {
			return err
		}
		disp, ok := c.locals[s.Name]
		if !ok {
			return fmt.Errorf("assign %s", s.Name)
		}
		c.b.MovMemRbpRax(disp)
		return nil
	case "setfield":
		off, err := c.fieldOff(s.Base, s.Name)
		if err != nil {
			return err
		}
		if err := c.emitExpr(s.Base, 0); err != nil {
			return err
		}
		c.storeTemp(0)
		if err := c.emitExpr(s.Val, 1); err != nil {
			return err
		}
		c.loadTempRcx(0)
		c.b.MovMemRegDispRax(asm.RCX, off)
		return nil
	case "store8":
		if err := c.emitExpr(s.Addr, 0); err != nil {
			return err
		}
		c.storeTemp(0)
		if err := c.emitExpr(s.Val, 1); err != nil {
			return err
		}
		c.loadTempRcx(0)
		c.b.MovByteRcxAl()
		return nil
	case "store64":
		if err := c.emitExpr(s.Addr, 0); err != nil {
			return err
		}
		c.storeTemp(0)
		if err := c.emitExpr(s.Val, 1); err != nil {
			return err
		}
		c.loadTempRcx(0)
		c.b.MovQwordRcxRax()
		return nil
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
		c.b.Jmp(c.epi)
		return nil
	case "if":
		if err := c.emitExpr(s.Cond, 0); err != nil {
			return err
		}
		c.b.TestRaxRax()
		end := c.b.NewLabel()
		elseL := end
		if len(s.Else) > 0 {
			elseL = c.b.NewLabel()
		}
		c.b.Jz(elseL)
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
		begin := c.b.NewLabel()
		end := c.b.NewLabel()
		c.b.Mark(begin)
		if err := c.emitExpr(s.Cond, 0); err != nil {
			return err
		}
		c.b.TestRaxRax()
		c.b.Jz(end)
		if err := c.emitStmts(s.Body); err != nil {
			return err
		}
		c.b.Jmp(begin)
		c.b.Mark(end)
		return nil
	default:
		return fmt.Errorf("stmt %s", s.Op)
	}
}

func (c *cg) emitExpr(n *ir.Node, lv int) error {
	if n == nil {
		return fmt.Errorf("nil expr in %s.%s", c.pkg.Path, c.fn.Name)
	}
	switch n.Op {
	case "int":
		c.b.MovRegImm64(asm.RAX, n.Int)
		return nil
	case "sizeof":
		sz, err := c.sizeOf(n.Type)
		if err != nil {
			return err
		}
		c.b.MovRegImm64(asm.RAX, sz)
		return nil
	case "bool":
		v := int64(0)
		if n.Bool {
			v = 1
		}
		c.b.MovRegImm64(asm.RAX, v)
		return nil
	case "strptr":
		off := c.intern(n.Str)
		c.b.MovRegImm64(asm.RAX, 0)
		c.b.AbsImmLabel(off)
		return nil
	case "strlen":
		c.b.MovRegImm64(asm.RAX, int64(len(n.Str)))
		return nil
	case "name":
		if n.Pkg != "" {
			v, err := c.pkgConst(n)
			if err != nil {
				return err
			}
			c.b.MovRegImm64(asm.RAX, v)
			return nil
		}
		if disp, ok := c.locals[n.Name]; ok {
			c.b.MovRaxMemRbp(disp)
			return nil
		}
		if v, ok := c.consts[n.Name]; ok {
			c.b.MovRegImm64(asm.RAX, v)
			return nil
		}
		return fmt.Errorf("name %s", n.Name)
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr":
		if err := c.emitExpr(n.Left, lv); err != nil {
			return err
		}
		c.storeTemp(lv)
		if err := c.emitExpr(n.Right, lv+1); err != nil {
			return err
		}
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.loadTempRax(lv)
		switch n.Op {
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
		return nil
	case "eq", "ne", "lt", "le", "gt", "ge":
		if err := c.emitExpr(n.Left, lv); err != nil {
			return err
		}
		c.storeTemp(lv)
		if err := c.emitExpr(n.Right, lv+1); err != nil {
			return err
		}
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.loadTempRax(lv)
		c.b.CmpRaxRcx()
		cc := map[string]byte{"eq": 0x94, "ne": 0x95, "lt": 0x9C, "ge": 0x9D, "le": 0x9E, "gt": 0x9F}[n.Op]
		c.b.SetccAl(cc)
		c.b.MovzxRaxAl()
		return nil
	case "land":
		no := c.b.NewLabel()
		end := c.b.NewLabel()
		if err := c.emitExpr(n.Left, lv); err != nil {
			return err
		}
		c.b.TestRaxRax()
		c.b.Jz(no)
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
		if err := c.emitExpr(n.Left, lv); err != nil {
			return err
		}
		c.b.TestRaxRax()
		c.b.Jnz(yes)
		if err := c.emitExpr(n.Right, lv); err != nil {
			return err
		}
		c.b.Jmp(end)
		c.b.Mark(yes)
		c.b.MovRegImm64(asm.RAX, 1)
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
		if err := c.emitExpr(n.Base, lv); err != nil {
			return err
		}
		c.b.MovRaxMemRegDisp(asm.RAX, off)
		return nil
	case "load8":
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.b.MovzxRaxByteRcx()
		return nil
	case "load32":
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.b.MovEaxDwordRcx()
		return nil
	case "load64":
		if err := c.emitExpr(n.Arg, lv); err != nil {
			return err
		}
		c.b.MovRegReg(asm.RCX, asm.RAX)
		c.b.MovRaxQwordRcx()
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
		for i, a := range n.Args {
			if err := c.emitExpr(a, lv+i); err != nil {
				return err
			}
			c.storeTemp(lv + i)
		}
		regs := []int{asm.RDI, asm.RSI, asm.RDX, asm.RCX, asm.R8, asm.R9}
		for i := range n.Args {
			c.loadTempReg(regs[i], lv+i)
		}
		c.b.Call(lab)
		return nil
	case "syscall":
		if len(n.Args) != 7 {
			return fmt.Errorf("syscall arity")
		}
		for i, a := range n.Args {
			if err := c.emitExpr(a, lv+i); err != nil {
				return err
			}
			c.storeTemp(lv + i)
		}
		regs := []int{asm.RAX, asm.RDI, asm.RSI, asm.RDX, asm.R10, asm.R8, asm.R9}
		for i := range n.Args {
			c.loadTempReg(regs[i], lv+i)
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
		if _, ok := c.locals[n.Name]; ok {
			// Recover the declared type from params and vars by scanning.
			return c.localType(n.Name)
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

func (c *cg) localType(name string) string {
	for _, pa := range c.fn.Params {
		if pa.Name == name {
			return c.resolve(c.pkg, pa.Type)
		}
	}
	var found string
	var walk func(stmts []*ir.Node)
	walk = func(stmts []*ir.Node) {
		for _, s := range stmts {
			if s == nil {
				continue
			}
			if s.Op == "var" && s.Name == name {
				found = c.resolve(c.pkg, s.Type)
			}
			if s.Op == "if" {
				walk(s.Then)
				walk(s.Else)
			}
			if s.Op == "while" {
				walk(s.Body)
			}
		}
	}
	walk(c.fn.Body)
	if found == "" {
		return "invalid"
	}
	return found
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
