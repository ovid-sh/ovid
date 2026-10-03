// Package ov parses the human projection into the Ovid JSON program.
// It is a bootstrap authoring tool. The ovid binary reads ovid.json only.
package ov

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ovid/internal/ir"
)

type parseErr struct{ msg string }

func (e parseErr) Error() string { return e.msg }

type parser struct {
	path    string
	src     []byte
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
}

func ParseProgram(files map[string]string, entry string) (prog *ir.Program, err error) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseErr); ok {
				prog = nil
				err = pe
				return
			}
			panic(r)
		}
	}()
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	prog = &ir.Program{
		Revision: ir.RevZeros,
		Module:   "ovid",
		Entry:    entry,
		Packages: make([]ir.Package, 0, len(keys)),
	}
	for _, path := range keys {
		p := &parser{path: path, src: []byte(files[path])}
		pkg := p.parseFile()
		if pkg.Path != path {
			p.errorf("package %s does not match path %s", pkg.Path, path)
		}
		prog.Packages = append(prog.Packages, *pkg)
	}
	return prog, nil
}

func (p *parser) errorf(f string, args ...any) {
	line := 1
	for i := 0; i < p.i && i < len(p.src); i++ {
		if p.src[i] == '\n' {
			line++
		}
	}
	panic(parseErr{fmt.Sprintf("%s:%d: "+f, append([]any{p.path, line}, args...)...)})
}

func (p *parser) save() mark { return mark{p.i, p.nl, p.skipped} }

func (p *parser) restore(m mark) {
	p.i = m.i
	p.nl = m.nl
	p.skipped = m.skipped
}

func (p *parser) bump() { p.skipped = false }

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
	p.expectKw("package")
	path := p.parsePath()
	p.expectEnd()
	pkg := &ir.Package{ID: "pkg:" + path, Path: path}
	p.pkg = pkg
	for p.kw("import") {
		ip := p.parsePath()
		pkg.Imports = append(pkg.Imports, ir.Import{ID: "im:" + path + ":" + ip, Path: ip})
		p.expectEnd()
	}
	for !p.eof() {
		switch {
		case p.kw("const"):
			p.parseConst()
		case p.kw("type"):
			p.parseTypeDecl()
		case p.kw("func"):
			p.parseFunc()
		default:
			p.errorf("expected declaration")
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

func (p *parser) parseConst() {
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
		ID: "cn:" + p.pkg.Path + "." + name, Name: name, Type: "i64", Value: v,
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

func (p *parser) parseTypeDecl() {
	name := p.ident()
	p.expectKw("struct")
	p.expect('{')
	var fields []ir.Field
	for !p.peekByte('}') {
		if p.eof() {
			p.errorf("unclosed struct")
		}
		fn := p.ident()
		ft := p.parseType()
		fields = append(fields, ir.Field{
			ID: "fld:" + p.pkg.Path + "." + name + "." + fn, Name: fn, Type: ft,
		})
		p.expectEnd()
	}
	p.expect('}')
	p.pkg.Types = append(p.pkg.Types, ir.TypeDecl{
		ID: "ty:" + p.pkg.Path + "." + name, Name: name, Fields: fields,
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

func (p *parser) parseFunc() {
	name := p.ident()
	p.fn = &ir.Func{
		ID: "fn:" + p.pkg.Path + "." + name, Name: name,
	}
	p.ec = 0
	p.sc = 0
	p.expect('(')
	if !p.peekByte(')') {
		for {
			pn := p.ident()
			pt := p.parseType()
			p.fn.Params = append(p.fn.Params, ir.Param{
				ID: "pa:" + p.pkg.Path + "." + name + "." + pn, Name: pn, Type: pt,
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
	switch {
	case p.kw("var"):
		return p.parseVar()
	case p.kw("if"):
		return p.parseIf()
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
		return &ir.Node{ID: p.sid(), Op: op, Addr: addr, Val: val}
	default:
		e := p.parseExpr()
		if p.peekAssign() {
			p.expect('=')
			v := p.parseExpr()
			if e.Op == "name" && e.ValK == 0 && e.Left == nil && e.Base == nil {
				return &ir.Node{ID: p.sid(), Op: "assign", Name: e.Name, Val: v}
			}
			if e.Op == "field" {
				return &ir.Node{ID: p.sid(), Op: "setfield", Base: e.Base, Name: e.Name, Val: v}
			}
			p.errorf("cannot assign to this expression")
		}
		return &ir.Node{ID: p.sid(), Op: "expr", Val: e}
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
	n := &ir.Node{ID: p.sid(), Op: "var", Name: name, Type: typ}
	if p.peekAssign() {
		p.expect('=')
		n.Val = p.parseExpr()
	}
	return n
}

func (p *parser) parseReturn() *ir.Node {
	n := &ir.Node{ID: p.sid(), Op: "return"}
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

func (p *parser) parseIf() *ir.Node {
	cond := p.parseExpr()
	then := p.parseBlock()
	var elseS []*ir.Node
	if p.peekKw("else") {
		p.ident()
		if p.peekKw("if") {
			p.ident()
			elseS = []*ir.Node{p.parseIf()}
		} else {
			elseS = p.parseBlock()
		}
	}
	return &ir.Node{ID: p.sid(), Op: "if", Cond: cond, Then: then, Else: elseS}
}

func (p *parser) parseWhile() *ir.Node {
	cond := p.parseExpr()
	body := p.parseBlock()
	return &ir.Node{ID: p.sid(), Op: "while", Cond: cond, Body: body}
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
		left = &ir.Node{ID: p.eid(), Op: op, Left: left, Right: right}
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
			e = &ir.Node{ID: p.eid(), Op: "field", Base: e, Name: name}
			continue
		}
		if p.peekKw("as") {
			p.ident()
			t := p.parseType()
			e = &ir.Node{ID: p.eid(), Op: "cast", Type: t, Arg: e}
			continue
		}
		return e
	}
}

func (p *parser) primary() *ir.Node {
	if p.peekByte('(') {
		p.expect('(')
		e := p.parseExpr()
		p.expect(')')
		return e
	}
	if p.peekByte('"') {
		p.errorf("bare string; use strptr or strlen")
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
		p.errorf("expected expression")
	}
	m := p.save()
	id := p.ident()
	if p.peekByte('/') {
		if path, name, ok := p.tryPkgCall(id); ok {
			return p.callArgs("call", path, name)
		}
		p.restore(m)
		id = p.ident()
	}
	if p.peekByte('(') {
		return p.callArgs("call", "", id)
	}
	return &ir.Node{ID: p.eid(), Op: "name", Name: id}
}

func (p *parser) tryPkgCall(first string) (string, string, bool) {
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
	name := p.ident()
	if !p.peekByte('(') {
		return "", "", false
	}
	return path, name, true
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

// Unused import guard so strings stays available for callers/tests.
var _ = strings.TrimSpace
