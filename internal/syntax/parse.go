// Package syntax parses Ovid source text (.ov) into the ir tree. Every node
// gets an id and a source span. Ids follow the same scheme as before:
// statements and expressions are numbered per function in parse order.
package syntax

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"ovid/internal/ir"
)

// Error is a syntax error at byte offset Off of file File.
type Error struct {
	File int
	Off  int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

type parser struct {
	file    int
	src     []byte
	last    int
	i       int
	nl      bool
	skipped bool
	pkg     *ir.Package
	fn      *ir.Func
	ec      int
	sc      int
}

type mark struct {
	i       int
	nl      bool
	skipped bool
	last    int
}

// ParseFile parses one source file. file is the index recorded in spans.
func ParseFile(file int, src []byte) (pkg *ir.Package, err *Error) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(*Error); ok {
				pkg = nil
				err = pe
				return
			}
			panic(r)
		}
	}()
	p := &parser{file: file, src: src}
	return p.parseFile(), nil
}

func (p *parser) errorf(f string, args ...any) {
	p.skip()
	msg := fmt.Sprintf(f, args...)
	if strings.HasPrefix(msg, "expected") {
		msg += ", found " + p.found()
	}
	off := p.i
	if p.i >= len(p.src) || (p.nl && p.i > 0) {
		// Point at the end of the line the problem is on, not the next one.
		off = p.last
	}
	panic(&Error{File: p.file, Off: off, Msg: msg})
}

// found describes the next token for error messages.
func (p *parser) found() string {
	if p.i >= len(p.src) {
		return "end of file"
	}
	if p.nl && p.i > 0 {
		return "newline"
	}
	e := p.i
	if isIdentStart(p.src[e]) || isDigit(p.src[e]) {
		for e < len(p.src) && isIdentCont(p.src[e]) {
			e++
		}
	} else {
		e++
	}
	return strconv.Quote(string(p.src[p.i:e]))
}

// start skips blanks and returns the offset of the next token.
func (p *parser) start() int {
	p.skip()
	return p.i
}

// span runs from s to the end of the last consumed token.
func (p *parser) span(s int) ir.Span {
	return ir.Span{File: p.file, Off: s, End: p.last}
}

func (p *parser) save() mark { return mark{p.i, p.nl, p.skipped, p.last} }

func (p *parser) restore(m mark) {
	p.i = m.i
	p.nl = m.nl
	p.skipped = m.skipped
	p.last = m.last
}

func (p *parser) bump() {
	p.skipped = false
	p.last = p.i
}

func (p *parser) eof() bool {
	p.skip()
	return p.i >= len(p.src)
}

func (p *parser) skip() {
	if p.skipped {
		return
	}
	p.skipped = true
	p.nl = false
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c == ' ' || c == '\t' || c == '\r' {
			p.i++
			continue
		}
		if c == '\n' {
			p.nl = true
			p.i++
			continue
		}
		if c == '/' && p.i+1 < len(p.src) && p.src[p.i+1] == '/' {
			p.i += 2
			for p.i < len(p.src) && p.src[p.i] != '\n' {
				p.i++
			}
			continue
		}
		if c == '/' && p.i+1 < len(p.src) && p.src[p.i+1] == '*' {
			p.i += 2
			for p.i+1 < len(p.src) && !(p.src[p.i] == '*' && p.src[p.i+1] == '/') {
				if p.src[p.i] == '\n' {
					p.nl = true
				}
				p.i++
			}
			if p.i+1 >= len(p.src) {
				p.errorf("unclosed comment")
			}
			p.i += 2
			continue
		}
		return
	}
}

func (p *parser) peekByte(b byte) bool {
	p.skip()
	return p.i < len(p.src) && p.src[p.i] == b
}

func (p *parser) expect(b byte) {
	if !p.peekByte(b) {
		p.errorf("expected %q", string(b))
	}
	p.i++
	p.bump()
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func (p *parser) peekIdent() bool {
	p.skip()
	return p.i < len(p.src) && isIdentStart(p.src[p.i])
}

func (p *parser) ident() string {
	p.skip()
	if p.i >= len(p.src) || !isIdentStart(p.src[p.i]) {
		p.errorf("expected identifier")
	}
	s := p.i
	p.i++
	for p.i < len(p.src) && isIdentCont(p.src[p.i]) {
		p.i++
	}
	id := string(p.src[s:p.i])
	p.bump()
	return id
}

func (p *parser) peekKw(k string) bool {
	m := p.save()
	if !p.peekIdent() {
		return false
	}
	id := p.ident()
	p.restore(m)
	return id == k
}

func (p *parser) kw(k string) bool {
	if !p.peekKw(k) {
		return false
	}
	p.ident()
	return true
}

func (p *parser) expectKw(k string) {
	if !p.kw(k) {
		p.errorf("expected %s", k)
	}
}

func (p *parser) parseFile() *ir.Package {
	s := p.start()
	p.expectKw("package")
	path := p.parsePath()
	pkg := &ir.Package{ID: "pkg:" + path, Path: path, Span: p.span(s)}
	p.expectEnd()
	p.pkg = pkg
	for {
		s := p.start()
		if !p.kw("import") {
			break
		}
		ip := p.parsePath()
		pkg.Imports = append(pkg.Imports, ir.Import{ID: "im:" + path + ":" + ip, Path: ip, Span: p.span(s)})
		p.expectEnd()
	}
	for !p.eof() {
		s := p.start()
		switch {
		case p.kw("const"):
			p.parseConst(s)
		case p.kw("type"):
			p.parseTypeDecl(s)
		case p.kw("func"):
			p.parseFunc(s)
		case p.peekKw("import"):
			p.errorf("imports must come before declarations")
		default:
			p.errorf("expected const, type, or func")
		}
	}
	return pkg
}

func (p *parser) parsePath() string {
	seg := p.ident()
	path := seg
	for p.peekByte('/') {
		p.expect('/')
		path += "/" + p.ident()
	}
	return path
}

func (p *parser) expectEnd() {
	if p.peekByte('}') {
		return
	}
	if p.eof() {
		return
	}
	if !p.nl {
		p.errorf("expected newline")
	}
}

func (p *parser) parseConst(s int) {
	name := p.ident()
	typ := p.parseType()
	p.expect('=')
	ex := p.parseExpr()
	v, ok := p.evalConst(ex)
	if !ok {
		p.errorf("const %s is not a constant integer expression", name)
	}
	if typ != "i64" {
		p.errorf("const %s must be i64", name)
	}
	p.pkg.Consts = append(p.pkg.Consts, ir.Const{
		ID: "cn:" + p.pkg.Path + "." + name, Name: name, Type: "i64", Value: v, Span: p.span(s),
	})
	p.expectEnd()
}

func (p *parser) evalConst(n *ir.Node) (int64, bool) {
	if n == nil {
		return 0, false
	}
	switch n.Op {
	case "int":
		return n.Int, true
	case "name":
		if n.Pkg != "" && n.Pkg != p.pkg.Path {
			p.errorf("a const's value cannot name another package's const (%s.%s); write the number", n.Pkg, n.Name)
		}
		for _, c := range p.pkg.Consts {
			if c.Name == n.Name {
				return c.Value, true
			}
		}
		return 0, false
	case "neg":
		v, ok := p.evalConst(n.Arg)
		return -v, ok
	case "bnot":
		v, ok := p.evalConst(n.Arg)
		return ^v, ok
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr":
		l, ok1 := p.evalConst(n.Left)
		r, ok2 := p.evalConst(n.Right)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch n.Op {
		case "add":
			return l + r, true
		case "sub":
			return l - r, true
		case "mul":
			return l * r, true
		case "div":
			if r == 0 {
				return 0, false
			}
			return l / r, true
		case "mod":
			if r == 0 {
				return 0, false
			}
			return l % r, true
		case "and":
			return l & r, true
		case "or":
			return l | r, true
		case "xor":
			return l ^ r, true
		case "shl":
			return l << uint64(r), true
		case "shr":
			return l >> uint64(r), true
		}
	}
	return 0, false
}

func (p *parser) parseTypeDecl(s int) {
	name := p.ident()
	p.expectKw("struct")
	p.expect('{')
	var fields []ir.Field
	for !p.peekByte('}') {
		if p.eof() {
			p.errorf("unclosed struct")
		}
		fs := p.start()
		fn := p.ident()
		ft := p.parseType()
		fields = append(fields, ir.Field{
			ID: "fld:" + p.pkg.Path + "." + name + "." + fn, Name: fn, Type: ft, Span: p.span(fs),
		})
		p.expectEnd()
	}
	p.expect('}')
	p.pkg.Types = append(p.pkg.Types, ir.TypeDecl{
		ID: "ty:" + p.pkg.Path + "." + name, Name: name, Fields: fields, Span: p.span(s),
	})
	p.expectEnd()
}

func (p *parser) parseType() string {
	star := false
	if p.peekByte('*') {
		p.expect('*')
		star = true
	}
	seg := p.ident()
	path := seg
	for p.peekByte('/') {
		p.expect('/')
		path += "/" + p.ident()
	}
	name := path
	pkg := ""
	if p.peekByte('.') {
		p.expect('.')
		name = p.ident()
		pkg = path
	}
	if pkg == "" {
		if name == "i64" || name == "bool" {
			if star {
				p.errorf("cannot use a pointer to %s", name)
			}
			return name
		}
		pkg = p.pkg.Path
	}
	full := pkg + "." + name
	if star {
		return "*" + full
	}
	return full
}

func (p *parser) parseFunc(s int) {
	name := p.ident()
	p.fn = &ir.Func{
		ID: "fn:" + p.pkg.Path + "." + name, Name: name,
	}
	p.ec = 0
	p.sc = 0
	p.expect('(')
	if !p.peekByte(')') {
		for {
			ps := p.start()
			pn := p.ident()
			pt := p.parseType()
			p.fn.Params = append(p.fn.Params, ir.Param{
				ID: "pa:" + p.pkg.Path + "." + name + "." + pn, Name: pn, Type: pt, Span: p.span(ps),
			})
			if p.peekByte(')') {
				break
			}
			p.expect(',')
		}
	}
	p.expect(')')
	p.fn.Result = p.parseType()
	p.fn.Body = p.parseBlock()
	p.fn.Span = p.span(s)
	p.pkg.Funcs = append(p.pkg.Funcs, *p.fn)
	p.fn = nil
	p.expectEnd()
}

func (p *parser) parseBlock() []*ir.Node {
	p.expect('{')
	var ss []*ir.Node
	for !p.peekByte('}') {
		if p.eof() {
			p.errorf("unclosed block")
		}
		ss = append(ss, p.parseStmt())
		p.expectEnd()
	}
	p.expect('}')
	return ss
}

func (p *parser) sid() string {
	p.sc++
	fn := "_"
	if p.fn != nil {
		fn = p.fn.Name
	}
	return fmt.Sprintf("st:%s.%s:%d", p.pkg.Path, fn, p.sc)
}

func (p *parser) eid() string {
	p.ec++
	fn := "_"
	if p.fn != nil {
		fn = p.fn.Name
	}
	return fmt.Sprintf("ex:%s.%s:%d", p.pkg.Path, fn, p.ec)
}

func (p *parser) parseStmt() *ir.Node {
	s := p.start()
	// Statement ids are numbered in source order, outer before inner.
	id := p.sid()
	n := p.stmt(s, id)
	n.ID = id
	n.Span = p.span(s)
	return n
}

func (p *parser) stmt(s int, id string) *ir.Node {
	switch {
	case p.kw("var"):
		return p.parseVar()
	case p.kw("if"):
		return p.parseIf(s, id)
	case p.kw("while"):
		return p.parseWhile()
	case p.kw("return"):
		return p.parseReturn()
	case p.peekKw("store8") || p.peekKw("store64"):
		op := p.ident()
		p.expect('(')
		addr := p.parseExpr()
		p.expect(',')
		val := p.parseExpr()
		p.expect(')')
		return &ir.Node{Op: op, Addr: addr, Val: val}
	default:
		e := p.parseExpr()
		if p.peekAssign() {
			p.expect('=')
			v := p.parseExpr()
			if e.Op == "name" && e.ValK == 0 && e.Left == nil && e.Base == nil {
				return &ir.Node{Op: "assign", Name: e.Name, Val: v}
			}
			if e.Op == "field" {
				return &ir.Node{Op: "setfield", Base: e.Base, Name: e.Name, Val: v}
			}
			p.errorf("cannot assign to this expression")
		}
		return &ir.Node{Op: "expr", Val: e}
	}
}

func (p *parser) peekAssign() bool {
	p.skip()
	if p.i < len(p.src) && p.src[p.i] == '=' {
		if p.i+1 < len(p.src) && p.src[p.i+1] == '=' {
			return false
		}
		return true
	}
	return false
}

func (p *parser) parseVar() *ir.Node {
	name := p.ident()
	typ := p.parseType()
	n := &ir.Node{Op: "var", Name: name, Type: typ}
	if p.peekAssign() {
		p.expect('=')
		n.Val = p.parseExpr()
	}
	return n
}

func (p *parser) parseReturn() *ir.Node {
	n := &ir.Node{Op: "return"}
	p.skip()
	if p.i >= len(p.src) || p.nl || p.src[p.i] == '}' {
		return n
	}
	if p.peekKw("var") || p.peekKw("if") || p.peekKw("while") || p.peekKw("return") || p.peekKw("store8") || p.peekKw("store64") {
		return n
	}
	n.Val = p.parseExpr()
	return n
}

func (p *parser) parseIf(s int, id string) *ir.Node {
	cond := p.parseExpr()
	then := p.parseBlock()
	var elseS []*ir.Node
	if p.peekKw("else") {
		p.ident()
		if p.peekKw("if") {
			es := p.start()
			eid := p.sid()
			p.ident()
			elseS = []*ir.Node{p.parseIf(es, eid)}
		} else {
			elseS = p.parseBlock()
		}
	}
	return &ir.Node{ID: id, Op: "if", Cond: cond, Then: then, Else: elseS, Span: p.span(s)}
}

func (p *parser) parseWhile() *ir.Node {
	cond := p.parseExpr()
	body := p.parseBlock()
	return &ir.Node{Op: "while", Cond: cond, Body: body}
}

func (p *parser) parseExpr() *ir.Node { return p.prec(1) }

func (p *parser) prec(min int) *ir.Node {
	left := p.unary()
	for {
		op, lvl, ok := p.peekBinop()
		if !ok || lvl < min {
			break
		}
		p.consumeBinop()
		right := p.prec(lvl + 1)
		left = &ir.Node{ID: p.eid(), Op: op, Left: left, Right: right, Span: ir.Span{File: p.file, Off: left.Span.Off, End: p.last}}
	}
	return left
}

func (p *parser) peekBinop() (string, int, bool) {
	p.skip()
	if p.i >= len(p.src) {
		return "", 0, false
	}
	two := ""
	if p.i+1 < len(p.src) {
		two = string(p.src[p.i : p.i+2])
	}
	switch two {
	case "&&":
		return "land", 2, true
	case "||":
		return "lor", 1, true
	case "==":
		return "eq", 3, true
	case "!=":
		return "ne", 3, true
	case "<=":
		return "le", 3, true
	case ">=":
		return "ge", 3, true
	case "<<":
		return "shl", 5, true
	case ">>":
		return "shr", 5, true
	}
	switch p.src[p.i] {
	case '<':
		return "lt", 3, true
	case '>':
		return "gt", 3, true
	case '+':
		return "add", 4, true
	case '-':
		return "sub", 4, true
	case '|':
		return "or", 4, true
	case '^':
		return "xor", 4, true
	case '*':
		return "mul", 5, true
	case '/':
		return "div", 5, true
	case '%':
		return "mod", 5, true
	case '&':
		return "and", 5, true
	}
	return "", 0, false
}

func (p *parser) consumeBinop() {
	p.skip()
	if p.i+1 < len(p.src) {
		two := string(p.src[p.i : p.i+2])
		switch two {
		case "&&", "||", "==", "!=", "<=", ">=", "<<", ">>":
			p.i += 2
			p.bump()
			return
		}
	}
	p.i++
	p.bump()
}

func (p *parser) unary() *ir.Node {
	s := p.start()
	n := p.unary0()
	if n.Span.End == 0 {
		n.Span = p.span(s)
	}
	return n
}

func (p *parser) unary0() *ir.Node {
	if p.peekByte('!') {
		p.expect('!')
		return &ir.Node{ID: p.eid(), Op: "not", Arg: p.unary()}
	}
	if p.peekByte('-') {
		// negative number or negation
		p.skip()
		if p.i+1 < len(p.src) && p.src[p.i] == '-' && isDigit(p.src[p.i+1]) {
			return p.number()
		}
		p.expect('-')
		return &ir.Node{ID: p.eid(), Op: "neg", Arg: p.unary()}
	}
	if p.peekByte('^') {
		p.expect('^')
		return &ir.Node{ID: p.eid(), Op: "bnot", Arg: p.unary()}
	}
	if p.peekByte('&') {
		p.errorf("there is no address-of; to let a callee write a value back, pass a cell: var c i64 = ovid/io.Alloc(io, 8), then load64(c) (see ovid help language, Memory)")
	}
	if p.peekByte('*') {
		p.errorf("there is no dereference operator; read through a pointer with p.field, or an address with load64(addr)")
	}
	return p.postfix()
}

func (p *parser) postfix() *ir.Node {
	e := p.primary()
	for {
		if p.peekByte('.') {
			p.expect('.')
			name := p.ident()
			if p.peekByte('(') {
				p.errorf("methods are not in v0")
			}
			e = &ir.Node{ID: p.eid(), Op: "field", Base: e, Name: name, Span: ir.Span{File: p.file, Off: e.Span.Off, End: p.last}}
			continue
		}
		if p.peekKw("as") {
			p.ident()
			t := p.parseType()
			e = &ir.Node{ID: p.eid(), Op: "cast", Type: t, Arg: e, Span: ir.Span{File: p.file, Off: e.Span.Off, End: p.last}}
			continue
		}
		return e
	}
}

func (p *parser) primary() *ir.Node {
	s := p.start()
	n := p.primary0()
	if n.Span.End == 0 {
		n.Span = p.span(s)
	}
	return n
}

func (p *parser) primary0() *ir.Node {
	if p.peekByte('(') {
		p.expect('(')
		e := p.parseExpr()
		p.expect(')')
		return e
	}
	if p.peekByte('"') {
		p.errorf("bare string literal; write strptr(\"...\") for the address and strlen(\"...\") for the length")
	}
	if p.isNum() {
		return p.number()
	}
	switch {
	case p.peekKw("true"):
		p.ident()
		return &ir.Node{ID: p.eid(), Op: "bool", ValK: 2, Bool: true}
	case p.peekKw("false"):
		p.ident()
		return &ir.Node{ID: p.eid(), Op: "bool", ValK: 2, Bool: false}
	case p.peekKw("syscall"):
		p.ident()
		return p.callArgs("syscall", "", "")
	case p.peekKw("load8"), p.peekKw("load32"), p.peekKw("load64"):
		op := p.ident()
		p.expect('(')
		a := p.parseExpr()
		p.expect(')')
		return &ir.Node{ID: p.eid(), Op: op, Arg: a}
	case p.peekKw("strptr"), p.peekKw("strlen"):
		op := p.ident()
		p.expect('(')
		s := p.string()
		p.expect(')')
		return &ir.Node{ID: p.eid(), Op: op, ValK: 3, Str: s}
	}
	if !p.peekIdent() {
		p.errorf("expected an expression")
	}
	m := p.save()
	id := p.ident()
	if p.peekByte('/') {
		if path, name, ok := p.tryPkgRef(id); ok {
			if p.peekByte('(') {
				return p.callArgs("call", path, name)
			}
			if p.imported(path) {
				return &ir.Node{ID: p.eid(), Op: "name", Pkg: path, Name: name}
			}
		}
		p.restore(m)
		id = p.ident()
	}
	if p.peekByte('.') && p.imported(id) {
		m := p.save()
		p.expect('.')
		if p.peekIdent() {
			name := p.ident()
			if p.peekByte('(') {
				return p.callArgs("call", id, name)
			}
			// pkg.Name without a call is an imported const.
			return &ir.Node{ID: p.eid(), Op: "name", Pkg: id, Name: name}
		}
		p.restore(m)
	}
	if p.peekByte('(') {
		return p.callArgs("call", "", id)
	}
	return &ir.Node{ID: p.eid(), Op: "name", Name: id}
}

// imported reports whether path is imported by the file being parsed.
func (p *parser) imported(path string) bool {
	for _, im := range p.pkg.Imports {
		if im.Path == path {
			return true
		}
	}
	return false
}

// tryPkgRef reads the rest of path/to/pkg.Name after its first segment.
func (p *parser) tryPkgRef(first string) (string, string, bool) {
	if !p.peekByte('/') {
		return "", "", false
	}
	path := first
	for p.peekByte('/') {
		p.expect('/')
		if !p.peekIdent() {
			return "", "", false
		}
		path += "/" + p.ident()
	}
	if !p.peekByte('.') {
		return "", "", false
	}
	p.expect('.')
	if !p.peekIdent() {
		return "", "", false
	}
	return path, p.ident(), true
}

func (p *parser) callArgs(op, pkg, name string) *ir.Node {
	p.expect('(')
	var args []*ir.Node
	if !p.peekByte(')') {
		for {
			args = append(args, p.parseExpr())
			if p.peekByte(')') {
				break
			}
			p.expect(',')
		}
	}
	p.expect(')')
	n := &ir.Node{ID: p.eid(), Op: op, Pkg: pkg, Func: name, Args: args}
	if op == "syscall" {
		n.Func = ""
		n.Pkg = ""
	}
	return n
}

func (p *parser) isNum() bool {
	p.skip()
	if p.i >= len(p.src) {
		return false
	}
	c := p.src[p.i]
	if c == '-' && p.i+1 < len(p.src) && isDigit(p.src[p.i+1]) {
		return true
	}
	return isDigit(c)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (p *parser) number() *ir.Node {
	p.skip()
	s := p.i
	if p.src[p.i] == '-' {
		p.i++
	}
	base := 10
	if p.i+1 < len(p.src) && p.src[p.i] == '0' && (p.src[p.i+1] == 'x' || p.src[p.i+1] == 'X') {
		base = 16
		p.i += 2
	}
	for p.i < len(p.src) {
		c := p.src[p.i]
		if isDigit(c) || (base == 16 && ((c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'))) {
			p.i++
			continue
		}
		break
	}
	lit := string(p.src[s:p.i])
	p.bump()
	v, err := strconv.ParseInt(lit, 0, 64)
	if err != nil {
		p.errorf("bad integer %s", lit)
	}
	return &ir.Node{ID: p.eid(), Op: "int", ValK: 1, Int: v}
}

func (p *parser) string() string {
	if !p.peekByte('"') {
		p.errorf("expected string")
	}
	p.i++
	p.bump()
	var b bytes.Buffer
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c == '"' {
			p.i++
			p.bump()
			return b.String()
		}
		if c == '\\' {
			if p.i+1 >= len(p.src) {
				p.errorf("bad escape")
			}
			n := p.src[p.i+1]
			p.i += 2
			switch n {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\', '"':
				b.WriteByte(n)
			case '0':
				b.WriteByte(0)
			case 'x':
				if p.i+1 >= len(p.src) {
					p.errorf("bad hex escape")
				}
				h := string(p.src[p.i : p.i+2])
				v, err := strconv.ParseUint(h, 16, 8)
				if err != nil {
					p.errorf("bad hex escape")
				}
				b.WriteByte(byte(v))
				p.i += 2
			default:
				p.errorf("bad escape")
			}
			continue
		}
		if c == '\n' {
			p.errorf("newline in string")
		}
		r, size := utf8.DecodeRune(p.src[p.i:])
		if r == utf8.RuneError && size == 1 {
			p.errorf("bad utf-8")
		}
		b.WriteRune(r)
		p.i += size
	}
	p.errorf("unclosed string")
	return ""
}
