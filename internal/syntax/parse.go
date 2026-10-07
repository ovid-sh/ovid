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
	Hint string // what the language wants instead, when the slip is a known one
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
	p.fail("", f, args...)
}

// fail is errorf with a hint.
func (p *parser) fail(hint string, f string, args ...any) {
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
	panic(&Error{File: p.file, Off: off, Msg: msg, Hint: hint})
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

// tok is the span of the identifier name that was just consumed.
func (p *parser) tok(name string) ir.Span {
	return ir.Span{File: p.file, Off: p.last - len(name), End: p.last}
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

// peekLen reports whether the next tokens are len(, with at most spaces
// and tabs between: len is a keyword only there, so a variable may still
// be named len.
func (p *parser) peekLen() bool {
	if !p.peekKw("len") {
		return false
	}
	m := p.save()
	p.ident()
	i := p.i
	for i < len(p.src) && (p.src[i] == ' ' || p.src[i] == '\t') {
		i++
	}
	open := i < len(p.src) && p.src[i] == '('
	p.restore(m)
	return open
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
		if p.peekByte(';') {
			p.fail("statements are one per line; there is no ;", "expected newline")
		}
		p.errorf("expected newline")
	}
}

func (p *parser) parseConst(s int) {
	name := p.ident()
	ns := p.tok(name)
	if p.peekByte('[') {
		p.parseTable(s, name, ns)
		return
	}
	typ, _ := p.parseType()
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
		ID: "cn:" + p.pkg.Path + "." + name, Name: name, Type: "i64", Value: v, Span: p.span(s), NameSpan: ns,
	})
	p.expectEnd()
}

// parseTable reads the rest of const Name [N]i64 = {e, ...}: N elements,
// each a constant expression, which may run over several lines.
func (p *parser) parseTable(s int, name string, ns ir.Span) {
	p.expect('[')
	if !p.isNum() {
		p.errorf("expected the table's length")
	}
	n := p.number().Int
	p.expect(']')
	p.expectKw("i64")
	p.expect('=')
	p.expect('{')
	var vals []int64
	for !p.peekByte('}') {
		if p.eof() {
			p.errorf("unclosed table")
		}
		ex := p.parseExpr()
		v, ok := p.evalConst(ex)
		if !ok {
			p.errorf("element %d of table %s is not a constant integer expression", len(vals), name)
		}
		vals = append(vals, v)
		if !p.peekByte('}') {
			p.expect(',')
		}
	}
	p.expect('}')
	if int64(len(vals)) != n {
		p.errorf("table %s is declared [%d]i64 but has %d elements", name, n, len(vals))
	}
	p.pkg.Consts = append(p.pkg.Consts, ir.Const{
		ID: "cn:" + p.pkg.Path + "." + name, Name: name, Type: fmt.Sprintf("[%d]i64", n), Table: true, Values: vals,
		Span: p.span(s), NameSpan: ns,
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
				return c.Value, !c.Table
			}
		}
		return 0, false
	case "len":
		// len(Table) of a table in this package, declared above.
		if n.Pkg != "" && n.Pkg != p.pkg.Path {
			p.errorf("a const's value cannot name another package's table (%s.%s); write the number", n.Pkg, n.Name)
		}
		for _, c := range p.pkg.Consts {
			if c.Name == n.Name && c.Table {
				return int64(len(c.Values)), true
			}
		}
		return 0, false
	case "sizeof":
		p.errorf("a const's value cannot use sizeof; write sizeof(T) where the size is used")
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
	ns := p.tok(name)
	p.expectKw("struct")
	p.expect('{')
	var fields []ir.Field
	for !p.peekByte('}') {
		if p.eof() {
			p.errorf("unclosed struct")
		}
		fs := p.start()
		fn := p.ident()
		fns := p.tok(fn)
		ft, fts := p.parseType()
		fields = append(fields, ir.Field{
			ID: "fld:" + p.pkg.Path + "." + name + "." + fn, Name: fn, Type: ft, Span: p.span(fs),
			NameSpan: fns, TypeSpan: fts,
		})
		p.expectEnd()
	}
	p.expect('}')
	p.pkg.Types = append(p.pkg.Types, ir.TypeDecl{
		ID: "ty:" + p.pkg.Path + "." + name, Name: name, Fields: fields, Span: p.span(s), NameSpan: ns,
	})
	p.expectEnd()
}

// parseType reads a type and returns its full form and the span of its
// name, the last identifier.
func (p *parser) parseType() (string, ir.Span) {
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
	ns := p.tok(name)
	if pkg == "" {
		if name == "i64" || name == "bool" || name == "bytes" {
			if star {
				p.errorf("cannot use a pointer to %s", name)
			}
			return name, ns
		}
		pkg = p.pkg.Path
	}
	full := pkg + "." + name
	if star {
		return "*" + full, ns
	}
	return full, ns
}

func (p *parser) parseFunc(s int) {
	name := p.ident()
	p.fn = &ir.Func{
		ID: "fn:" + p.pkg.Path + "." + name, Name: name, NameSpan: p.tok(name),
	}
	p.ec = 0
	p.sc = 0
	p.expect('(')
	if !p.peekByte(')') {
		for {
			ps := p.start()
			pn := p.ident()
			pns := p.tok(pn)
			pt, pts := p.parseType()
			p.fn.Params = append(p.fn.Params, ir.Param{
				ID: "pa:" + p.pkg.Path + "." + name + "." + pn, Name: pn, Type: pt, Span: p.span(ps),
				NameSpan: pns, TypeSpan: pts,
			})
			if p.peekByte(')') {
				break
			}
			p.expect(',')
		}
	}
	p.expect(')')
	if p.peekByte('(') {
		// Two results: (T, i64), the second an error code.
		p.expect('(')
		p.fn.Result, p.fn.ResultSpan = p.parseType()
		p.expect(',')
		p.fn.Result2, _ = p.parseType()
		if p.fn.Result2 != "i64" {
			p.errorf("the second result is an error code; write (%s, i64)", p.fn.Result)
		}
		p.expect(')')
	} else {
		p.fn.Result, p.fn.ResultSpan = p.parseType()
	}
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
	case p.peekKw("store8") || p.peekKw("store16") || p.peekKw("store32") || p.peekKw("store64"):
		op := p.ident()
		args, ok := p.builtinArgs(2)
		if !ok {
			return &ir.Node{Op: op, Args: args}
		}
		return &ir.Node{Op: op, Addr: args[0], Val: args[1]}
	default:
		e := p.parseExpr()
		if p.peekByte(',') {
			// a, b = call: the call's two results into existing locals.
			if e.Op != "name" || e.Pkg != "" {
				p.errorf("cannot assign to this expression")
			}
			p.expect(',')
			name2 := p.ident()
			ns2 := p.tok(name2)
			if e.Name == "_" && name2 == "_" {
				p.errorf("_, _ discards both results; receive the error")
			}
			p.expect('=')
			v := p.parseExpr()
			return &ir.Node{Op: "assign2", Name: e.Name, Two: &ir.Second{Name: name2, NameSpan: ns2}, Val: v, NameSpan: e.NameSpan}
		}
		if p.peekAssign() {
			p.expect('=')
			v := p.parseExpr()
			if e.Op == "name" && e.ValK == 0 && e.Left == nil && e.Base == nil {
				return &ir.Node{Op: "assign", Name: e.Name, Val: v, NameSpan: e.NameSpan}
			}
			if e.Op == "field" {
				return &ir.Node{Op: "setfield", Base: e.Base, Name: e.Name, Val: v, NameSpan: e.NameSpan}
			}
			if e.Op == "index" || e.Op == "byte" {
				// b[i] = v stores the low byte of v.
				return &ir.Node{Op: "setbyte", Base: e, Val: v}
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
	name, ns, typ, ts := p.varName()
	n := &ir.Node{Op: "var", Name: name, Type: typ, NameSpan: ns, TypeSpan: ts}
	if p.peekByte(',') {
		// var a T, b T = call: the call's two results. _ discards one.
		p.expect(',')
		n.Op = "var2"
		n.Two = &ir.Second{}
		n.Two.Name, n.Two.NameSpan, n.Two.Type, n.Two.TypeSpan = p.varName()
		p.expect('=')
		n.Val = p.parseExpr()
		if n.Name == "_" && n.Two.Name == "_" {
			p.errorf("var _, _ discards both results; receive the error")
		}
		return n
	}
	if name == "_" {
		p.errorf("_ only discards one of a call's two results: var x T, _ = f(...)")
	}
	if p.peekAssign() {
		p.expect('=')
		n.Val = p.parseExpr()
	}
	return n
}

// varName reads a var's name and type; _ has no type.
func (p *parser) varName() (string, ir.Span, string, ir.Span) {
	name := p.ident()
	ns := p.tok(name)
	if name == "_" {
		return name, ns, "", ir.Span{}
	}
	if p.peekByte('=') {
		p.fail("every local is declared with its type: var "+name+" i64 = ...", "expected a type after "+name)
	}
	typ, ts := p.parseType()
	return name, ns, typ, ts
}

func (p *parser) parseReturn() *ir.Node {
	n := &ir.Node{Op: "return"}
	p.skip()
	if p.i >= len(p.src) || p.nl || p.src[p.i] == '}' {
		return n
	}
	if p.peekKw("var") || p.peekKw("if") || p.peekKw("while") || p.peekKw("return") || p.peekKw("store8") || p.peekKw("store16") || p.peekKw("store32") || p.peekKw("store64") {
		return n
	}
	n.Val = p.parseExpr()
	if p.peekByte(',') {
		// return v, e: a func with two results.
		p.expect(',')
		n.Val2 = p.parseExpr()
	}
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
			e = &ir.Node{ID: p.eid(), Op: "field", Base: e, Name: name, Span: ir.Span{File: p.file, Off: e.Span.Off, End: p.last}, NameSpan: p.tok(name)}
			continue
		}
		if p.peekKw("as") {
			p.ident()
			t, ts := p.parseType()
			e = &ir.Node{ID: p.eid(), Op: "cast", Type: t, Arg: e, Span: ir.Span{File: p.file, Off: e.Span.Off, End: p.last}, TypeSpan: ts}
			continue
		}
		if p.peekByte('[') {
			// Name[i] reads a table or a bytes, which the checker tells
			// apart (a local shadows a table); e[i] on any other base and
			// e[i:j] are bytes operations.
			p.expect('[')
			i := p.parseExpr()
			if p.peekByte(':') {
				p.expect(':')
				j := p.parseExpr()
				p.expect(']')
				e = &ir.Node{ID: p.eid(), Op: "slice", Base: e, Left: i, Right: j, Span: ir.Span{File: p.file, Off: e.Span.Off, End: p.last}}
				continue
			}
			p.expect(']')
			if e.Op != "name" {
				e = &ir.Node{ID: p.eid(), Op: "byte", Base: e, Arg: i, Span: ir.Span{File: p.file, Off: e.Span.Off, End: p.last}}
				continue
			}
			// The name node stays as the base, for a bytes.
			e = &ir.Node{ID: p.eid(), Op: "index", Name: e.Name, Pkg: e.Pkg, Base: e, Arg: i, Span: ir.Span{File: p.file, Off: e.Span.Off, End: p.last}, NameSpan: e.NameSpan}
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
		return p.callArgs("syscall", "", "", ir.Span{})
	case p.peekLen():
		// len(Table), a compile-time constant, or len(b) of a bytes; a
		// name is either, which the checker tells apart.
		p.ident()
		p.expect('(')
		t := p.postfix()
		p.expect(')')
		if t.Op != "name" {
			return &ir.Node{ID: p.eid(), Op: "blen", Base: t}
		}
		return &ir.Node{ID: p.eid(), Op: "len", Name: t.Name, Pkg: t.Pkg, Base: t, NameSpan: t.NameSpan}
	case p.peekKw("ushr"), p.peekKw("umulhi"), p.peekKw("ult"), p.peekKw("udiv"), p.peekKw("urem"):
		// The unsigned operations are binary operators spelled as calls.
		op := p.ident()
		args, ok := p.builtinArgs(2)
		if !ok {
			return &ir.Node{ID: p.eid(), Op: op, Args: args}
		}
		return &ir.Node{ID: p.eid(), Op: op, Left: args[0], Right: args[1]}
	case p.peekKw("load8"), p.peekKw("load16"), p.peekKw("load32"), p.peekKw("load64"), p.peekKw("bswap16"), p.peekKw("bswap32"), p.peekKw("bswap64"):
		op := p.ident()
		args, ok := p.builtinArgs(1)
		if !ok {
			return &ir.Node{ID: p.eid(), Op: op, Args: args}
		}
		return &ir.Node{ID: p.eid(), Op: op, Arg: args[0]}
	case p.peekKw("sizeof"):
		p.ident()
		p.expect('(')
		if p.peekByte('*') {
			p.errorf("sizeof takes a struct type, not a pointer: sizeof(T) is the size of a T")
		}
		t, ts := p.parseType()
		p.expect(')')
		return &ir.Node{ID: p.eid(), Op: "sizeof", Type: t, TypeSpan: ts}
	case p.peekKw("strptr"), p.peekKw("strlen"):
		op := p.ident()
		p.expect('(')
		s := p.string()
		p.expect(')')
		return &ir.Node{ID: p.eid(), Op: op, ValK: 3, Str: s}
	case p.peekKw("bytes"):
		// bytes(p, n): a bytes value from an address and a length.
		p.ident()
		args, ok := p.builtinArgs(2)
		if !ok {
			return &ir.Node{ID: p.eid(), Op: "bytes", Args: args}
		}
		return &ir.Node{ID: p.eid(), Op: "bytes", Left: args[0], Right: args[1]}
	}
	if p.peekByte('"') {
		// A string literal is a bytes value in rodata.
		s := p.string()
		return &ir.Node{ID: p.eid(), Op: "str", ValK: 3, Str: s}
	}
	if !p.peekIdent() {
		p.errorf("expected an expression")
	}
	m := p.save()
	id := p.ident()
	if p.peekByte('/') {
		if path, name, ok := p.tryPkgRef(id); ok {
			ns := p.tok(name)
			if p.peekByte('(') {
				return p.callArgs("call", path, name, ns)
			}
			if p.imported(path) {
				return &ir.Node{ID: p.eid(), Op: "name", Pkg: path, Name: name, NameSpan: ns}
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
			ns := p.tok(name)
			if p.peekByte('(') {
				return p.callArgs("call", id, name, ns)
			}
			// pkg.Name without a call is an imported const.
			return &ir.Node{ID: p.eid(), Op: "name", Pkg: id, Name: name, NameSpan: ns}
		}
		p.restore(m)
	}
	ns := p.tok(id)
	if p.peekByte('(') {
		return p.callArgs("call", "", id, ns)
	}
	return &ir.Node{ID: p.eid(), Op: "name", Name: id, NameSpan: ns}
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

// callArgs reads a call's arguments; ns is the span of the func name.
func (p *parser) callArgs(op, pkg, name string, ns ir.Span) *ir.Node {
	args := p.argList()
	n := &ir.Node{ID: p.eid(), Op: op, Pkg: pkg, Func: name, Args: args, NameSpan: ns}
	if op == "syscall" {
		n.Func = ""
		n.Pkg = ""
	}
	return n
}

// argList reads a parenthesised, comma-separated list of expressions.
func (p *parser) argList() []*ir.Node {
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
	return args
}

// builtinArgs reads a builtin's arguments and reports whether there are
// want of them. Any other number is left to the checker, which reports
// an arity error naming the builtin and its form, as the parser cannot.
func (p *parser) builtinArgs(want int) ([]*ir.Node, bool) {
	args := p.argList()
	return args, len(args) == want
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
