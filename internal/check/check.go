// Package check typechecks an Ovid program. It reports issues addressed by
// node id and records the type of every expression it visits.
package check

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"

	"ovid/internal/ir"
	"ovid/std"
)

// Issue is one error. ID names the node; the caller maps it to a location,
// unless At gives one: a repeated decl shares its id with the first, so the
// id would point there.
type Issue struct {
	Code     string   `json:"code"`
	ID       string   `json:"id,omitempty"`
	At       *ir.Span `json:"-"`
	Message  string   `json:"message"`
	Expected string   `json:"expected,omitempty"`
	Got      string   `json:"got,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

// Fact is a declaration-level fact (types, layouts, signatures).
type Fact map[string]any

type Result struct {
	Issues []Issue
	Facts  []Fact
	// Types maps expression ids to their checked type.
	Types map[string]string
	Funcs int
}

type sig struct {
	params []string
	names  []string
	result string
	id     string
}

type checker struct {
	r        *Result
	pkgs     map[string]*ir.Package
	sigs     map[string]sig
	pkg      *ir.Package
	fn       *ir.Func
	imported map[string]bool
	res      string
	dup      map[*ir.Func]bool // funcs whose name was already declared
	dupName  map[string]bool   // those funcs, as pkg.Name
}

func (c *checker) issue(is Issue) { c.r.Issues = append(c.r.Issues, is) }

func (c *checker) err(id, code, msg string) {
	c.issue(Issue{Code: code, ID: id, Message: msg})
}

func (c *checker) mismatch(id, what, got, want string) {
	c.issue(Issue{Code: "type_mismatch", ID: id, Message: fmt.Sprintf("%s: got %s, want %s", what, got, want), Expected: want, Got: got})
}

// Run checks p.
func Run(p *ir.Program) *Result {
	r := &Result{Types: map[string]string{}}
	c := &checker{r: r, pkgs: map[string]*ir.Package{}, sigs: map[string]sig{}, dup: map[*ir.Func]bool{}, dupName: map[string]bool{}}
	if strings.TrimSpace(p.Module) == "" {
		c.err("", "bad_module", "ovid.mod has no module line")
	}
	ids := map[string]bool{}
	claim := func(id string) {
		if id == "" {
			return
		}
		if ids[id] {
			c.err(id, "duplicate_id", "two nodes share id "+id)
		}
		ids[id] = true
	}
	for i := range p.Packages {
		pkg := &p.Packages[i]
		claim(pkg.ID)
		if _, ok := c.pkgs[pkg.Path]; ok {
			c.err(pkg.ID, "duplicate_package", "package "+pkg.Path+" is declared twice")
		}
		c.pkgs[pkg.Path] = pkg
	}

	for i := range p.Packages {
		pkg := &p.Packages[i]
		c.pkg = pkg
		// first is the decl of each name that comes first in the source,
		// whatever its kind: consts, types, and funcs share the names.
		type firstDecl struct {
			id string
			at ir.Span
		}
		first := map[string]firstDecl{}
		note := func(id, name string, at ir.Span) {
			f, ok := first[name]
			if !ok || at.File < f.at.File || (at.File == f.at.File && at.Off < f.at.Off) {
				first[name] = firstDecl{id, at}
			}
		}
		for _, cn := range pkg.Consts {
			note(cn.ID, cn.Name, cn.Span)
		}
		for _, t := range pkg.Types {
			note(t.ID, t.Name, t.Span)
		}
		for fi := range pkg.Funcs {
			note(pkg.Funcs[fi].ID, pkg.Funcs[fi].Name, pkg.Funcs[fi].Span)
		}
		// decl reports whether this is the first decl of name in the
		// package. A later one gets one error, at that decl, and nothing
		// else about it is checked: its id may be the first decl's, so its
		// fields, params, and statements could be reported at the wrong
		// place.
		decl := func(id, name string, at ir.Span) bool {
			if f := first[name]; f.at != at {
				c.issue(Issue{Code: "duplicate_name", ID: id, At: &at,
					Message: fmt.Sprintf("%s is already declared in package %s (%s)", name, pkg.Path, f.id)})
				return false
			}
			claim(id)
			return true
		}
		iseen := map[string]bool{}
		for _, im := range pkg.Imports {
			if iseen[im.Path] {
				c.issue(Issue{Code: "duplicate_name", ID: im.ID, At: &im.Span, Message: im.Path + " is imported twice"})
				continue
			}
			iseen[im.Path] = true
			claim(im.ID)
			if im.Path == pkg.Path {
				c.err(im.ID, "import_self", "package "+pkg.Path+" imports itself")
			} else if _, ok := c.pkgs[im.Path]; !ok {
				c.issue(Issue{Code: "unknown_package", ID: im.ID, Message: "no package " + im.Path,
					Hint: "packages are directories under the module root; std packages are " + strings.Join(StdHint, ", ")})
			}
		}
		for _, cn := range pkg.Consts {
			if !decl(cn.ID, cn.Name, cn.Span) {
				continue
			}
			c.r.Facts = append(c.r.Facts, Fact{"fact": "const", "id": cn.ID, "value": cn.Value})
			if cn.Type != "i64" {
				c.err(cn.ID, "bad_type", "const must be i64")
			}
		}
		for _, t := range pkg.Types {
			if !decl(t.ID, t.Name, t.Span) {
				continue
			}
			c.r.Facts = append(c.r.Facts, Fact{"fact": "type", "id": t.ID, "fields": len(t.Fields), "size": len(t.Fields) * 8})
			fseen := map[string]bool{}
			for i, f := range t.Fields {
				if fseen[f.Name] {
					c.issue(Issue{Code: "duplicate_name", ID: f.ID, At: &f.Span, Message: "field " + f.Name + " is declared twice"})
					continue
				}
				claim(f.ID)
				fseen[f.Name] = true
				ft, err := c.resolve(f.Type)
				c.r.Facts = append(c.r.Facts, Fact{"fact": "field", "id": f.ID, "type": ft, "offset": i * 8})
				if err != nil {
					c.err(f.ID, "bad_type", err.Error())
				} else if !scalar(ft) {
					c.err(f.ID, "struct_value", "field must be i64, bool, or a pointer; write *"+ft)
				}
			}
		}
		for fi := range pkg.Funcs {
			fn := &pkg.Funcs[fi]
			if !decl(fn.ID, fn.Name, fn.Span) {
				c.dup[fn] = true
				c.dupName[pkg.Path+"."+fn.Name] = true
				continue
			}
			c.r.Funcs++
			if len(fn.Params) > 6 {
				c.err(fn.ID, "arity", fmt.Sprintf("%s has %d parameters; at most 6", fn.Name, len(fn.Params)))
			}
			var ps, names []string
			pseen := map[string]bool{}
			for _, pa := range fn.Params {
				// A repeated param is reported by checkBody.
				if !pseen[pa.Name] {
					claim(pa.ID)
				}
				pseen[pa.Name] = true
				pt, err := c.resolve(pa.Type)
				if err != nil {
					c.err(pa.ID, "bad_type", err.Error())
					pt = "invalid"
				} else if !scalar(pt) {
					c.err(pa.ID, "struct_value", "parameter must be i64, bool, or a pointer; write *"+pt)
				}
				ps = append(ps, pt)
				names = append(names, pa.Name)
			}
			rt, err := c.resolve(fn.Result)
			if err != nil {
				c.err(fn.ID, "bad_type", err.Error())
				rt = "invalid"
			} else if !scalar(rt) {
				c.err(fn.ID, "struct_value", "result must be i64, bool, or a pointer; write *"+rt)
			}
			c.sigs[pkg.Path+"."+fn.Name] = sig{params: ps, names: names, result: rt, id: fn.ID}
			c.r.Facts = append(c.r.Facts, Fact{"fact": "func", "id": fn.ID, "sig": Signature(pkg.Path, fn)})
			for _, st := range fn.Body {
				st.Walk(func(n *ir.Node) { claim(n.ID) })
			}
		}
	}

	c.checkEntry(p)

	for i := range p.Packages {
		pkg := &p.Packages[i]
		c.pkg = pkg
		c.imported = map[string]bool{}
		for _, im := range pkg.Imports {
			c.imported[im.Path] = true
		}
		for fi := range pkg.Funcs {
			if !c.dup[&pkg.Funcs[fi]] {
				c.checkBody(&pkg.Funcs[fi])
			}
		}
	}
	return r
}

// StdHint lists the packages the toolchain ships: every directory of the
// embedded std that holds a .ov file.
var StdHint = stdPackages()

func stdPackages() []string {
	var pkgs []string
	fs.WalkDir(std.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ov") {
			if dir := path.Dir(p); !slices.Contains(pkgs, dir) {
				pkgs = append(pkgs, dir)
			}
		}
		return nil
	})
	sort.Strings(pkgs)
	return pkgs
}

func (c *checker) checkEntry(p *ir.Program) {
	ep, ok := c.pkgs[p.Entry]
	if !ok {
		c.issue(Issue{Code: "no_entry", Message: "entry package " + p.Entry + " does not exist", Hint: "set `entry <pkg>` in ovid.mod"})
		return
	}
	var main *ir.Func
	for i := range ep.Funcs {
		// A repeated main is already reported and not checked further.
		if ep.Funcs[i].Name == "main" && !c.dup[&ep.Funcs[i]] {
			main = &ep.Funcs[i]
		}
	}
	want := "func main(io *ovid/io.Cap) i64"
	if main == nil {
		c.issue(Issue{Code: "bad_main", ID: ep.ID, Message: "entry package has no main", Expected: want})
		return
	}
	c.pkg = ep
	pt := ""
	if len(main.Params) == 1 {
		pt, _ = c.resolve(main.Params[0].Type)
	}
	res, _ := c.resolve(main.Result)
	if len(main.Params) != 1 || pt != "*ovid/io.Cap" || res != "i64" {
		c.issue(Issue{Code: "bad_main", ID: main.ID, Message: "main has the wrong signature", Expected: want, Got: Signature(ep.Path, main)})
	}
	if io, ok := c.pkgs["ovid/io"]; ok {
		var capTy *ir.TypeDecl
		for i := range io.Types {
			if io.Types[i].Name == "Cap" {
				capTy = &io.Types[i]
			}
		}
		if capTy == nil {
			c.err(io.ID, "bad_abi", "ovid/io has no type Cap")
			return
		}
		need := []string{"argc", "argv", "heap", "used", "size"}
		for i, n := range need {
			if i >= len(capTy.Fields) || capTy.Fields[i].Name != n || capTy.Fields[i].Type != "i64" {
				c.issue(Issue{Code: "bad_abi", ID: capTy.ID, Message: "the runtime fills Cap's first five fields", Expected: "argc, argv, heap, used, size (all i64, in order)"})
				return
			}
		}
	} else {
		c.issue(Issue{Code: "bad_abi", Message: "missing package ovid/io", Hint: "import ovid/io; the toolchain ships it"})
	}
}

// Signature renders fn the way it is written in source.
func Signature(pkg string, fn *ir.Func) string {
	var b strings.Builder
	b.WriteString("func ")
	b.WriteString(fn.Name)
	b.WriteByte('(')
	for i, pa := range fn.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pa.Name)
		b.WriteByte(' ')
		b.WriteString(ShowType(pkg, pa.Type))
	}
	b.WriteString(") ")
	b.WriteString(ShowType(pkg, fn.Result))
	return b.String()
}

// ShowType strips pkg's own prefix from a type name.
func ShowType(pkg, t string) string {
	prefix := pkg + "."
	if strings.HasPrefix(t, "*"+prefix) {
		return "*" + strings.TrimPrefix(t, "*"+prefix)
	}
	return strings.TrimPrefix(t, prefix)
}

func scalar(t string) bool {
	return t == "i64" || t == "bool" || strings.HasPrefix(t, "*") || t == "invalid"
}

// resolve turns a source type into its full form (i64, bool, pkg.T, *pkg.T).
func (c *checker) resolve(t string) (string, error) {
	return Resolve(c.pkg, t, c.pkgs)
}

func Resolve(pkg *ir.Package, t string, pkgs map[string]*ir.Package) (string, error) {
	if t == "i64" || t == "bool" {
		return t, nil
	}
	star := strings.HasPrefix(t, "*")
	t = strings.TrimPrefix(t, "*")
	if t == "i64" || t == "bool" {
		return "", fmt.Errorf("cannot point at %s; use i64 for a raw address", t)
	}
	tpkg, name := pkg.Path, t
	if i := strings.LastIndex(t, "."); i >= 0 {
		tpkg, name = t[:i], t[i+1:]
	}
	target, ok := pkgs[tpkg]
	if !ok {
		return "", fmt.Errorf("unknown package %s in type %s", tpkg, t)
	}
	found := false
	var names []string
	for _, td := range target.Types {
		names = append(names, td.Name)
		if td.Name == name {
			found = true
		}
	}
	if !found {
		msg := "unknown type " + t
		if s := Suggest(name, names); s != "" {
			msg += "; did you mean " + s + "?"
		}
		return "", fmt.Errorf("%s", msg)
	}
	if star {
		return "*" + tpkg + "." + name, nil
	}
	return tpkg + "." + name, nil
}

type env struct {
	vars map[string]string
	up   *env
}

func (e *env) get(name string) (string, bool) {
	for ; e != nil; e = e.up {
		if t, ok := e.vars[name]; ok {
			return t, true
		}
	}
	return "", false
}

func (e *env) names() []string {
	var out []string
	for ; e != nil; e = e.up {
		for k := range e.vars {
			out = append(out, k)
		}
	}
	return out
}

func (c *checker) checkBody(fn *ir.Func) {
	c.fn = fn
	e := &env{vars: map[string]string{}}
	for _, pa := range fn.Params {
		if _, ok := e.vars[pa.Name]; ok {
			c.issue(Issue{Code: "duplicate_name", ID: pa.ID, At: &pa.Span, Message: "parameter " + pa.Name + " is declared twice"})
		}
		t, err := c.resolve(pa.Type)
		if err != nil {
			t = "invalid"
		}
		e.vars[pa.Name] = t
	}
	c.res, _ = c.resolve(fn.Result)
	if c.res == "" {
		c.res = "invalid"
	}
	c.stmts(e, fn.Body)
	if !pathsReturn(fn.Body) {
		c.issue(Issue{Code: "missing_return", ID: fn.ID, Message: fn.Name + ": not every path ends in return",
			Hint: "end the body with `return`; an if only counts when both branches return"})
	}
}

func pathsReturn(stmts []*ir.Node) bool {
	if len(stmts) == 0 {
		return false
	}
	last := stmts[len(stmts)-1]
	switch last.Op {
	case "return":
		return true
	case "if":
		return len(last.Else) > 0 && pathsReturn(last.Then) && pathsReturn(last.Else)
	}
	return false
}

func (c *checker) stmts(e *env, stmts []*ir.Node) {
	for _, s := range stmts {
		if s != nil {
			c.stmt(e, s)
		}
	}
}

func (c *checker) stmt(e *env, s *ir.Node) {
	switch s.Op {
	case "var":
		t, err := c.resolve(s.Type)
		if err != nil {
			c.err(s.ID, "bad_type", err.Error())
			t = "invalid"
		} else if !scalar(t) {
			c.err(s.ID, "struct_value", "local must be i64, bool, or a pointer; write *"+t)
		}
		if _, ok := e.get(s.Name); ok {
			c.err(s.ID, "duplicate_name", s.Name+" is already declared in this function; assign with `"+s.Name+" = ...` instead")
		}
		if s.Val != nil {
			vt := c.expr(e, s.Val)
			if vt != t && vt != "invalid" && t != "invalid" {
				c.mismatch(s.Val.ID, "var "+s.Name, vt, t)
			}
		}
		e.vars[s.Name] = t
	case "assign":
		t, ok := e.get(s.Name)
		if !ok {
			c.unknownName(s.ID, s.Name, e)
			t = "invalid"
		}
		vt := c.expr(e, s.Val)
		if vt != t && vt != "invalid" && t != "invalid" {
			c.mismatch(s.Val.ID, "assign to "+s.Name, vt, t)
		}
	case "setfield":
		bt := c.expr(e, s.Base)
		ft := c.field(s.ID, bt, s.Name)
		vt := c.expr(e, s.Val)
		if vt != ft && vt != "invalid" && ft != "invalid" {
			c.mismatch(s.Val.ID, "field "+s.Name, vt, ft)
		}
	case "store8", "store64":
		at := c.expr(e, s.Addr)
		vt := c.expr(e, s.Val)
		if at != "i64" && at != "invalid" {
			c.mismatch(s.Addr.ID, s.Op+" address", at, "i64")
		}
		if vt != "i64" && vt != "invalid" {
			c.mismatch(s.Val.ID, s.Op+" value", vt, "i64")
		}
	case "expr":
		c.expr(e, s.Val)
	case "return":
		if s.Val == nil {
			c.mismatch(s.ID, "return", "nothing", c.res)
			return
		}
		vt := c.expr(e, s.Val)
		if vt != c.res && vt != "invalid" && c.res != "invalid" {
			c.mismatch(s.Val.ID, "return value of "+c.fn.Name, vt, c.res)
		}
	case "if":
		if ct := c.expr(e, s.Cond); ct != "bool" && ct != "invalid" {
			c.mismatch(s.Cond.ID, "if condition", ct, "bool")
		}
		c.stmts(&env{vars: map[string]string{}, up: e}, s.Then)
		c.stmts(&env{vars: map[string]string{}, up: e}, s.Else)
	case "while":
		if ct := c.expr(e, s.Cond); ct != "bool" && ct != "invalid" {
			c.mismatch(s.Cond.ID, "while condition", ct, "bool")
		}
		c.stmts(&env{vars: map[string]string{}, up: e}, s.Body)
	default:
		c.err(s.ID, "bad_op", "unknown statement op "+s.Op)
	}
}

func (c *checker) unknownName(id, name string, e *env) {
	cands := e.names()
	for _, cn := range c.pkg.Consts {
		cands = append(cands, cn.Name)
	}
	is := Issue{Code: "unknown_name", ID: id, Message: "undefined: " + name}
	switch name {
	case "break", "continue":
		is.Message = "there is no " + name
		is.Hint = "loop on a flag instead: var more bool = true; while more { ... more = false }"
	case "nil", "null":
		is.Hint = "a null pointer is 0 as *T: var z i64 = 0, then z as *T"
	default:
		if s := Suggest(name, cands); s != "" {
			is.Hint = "did you mean " + s + "?"
		}
	}
	c.issue(is)
}

func (c *checker) field(id, bt, name string) string {
	if bt == "invalid" {
		return "invalid"
	}
	ft, fields, err := FieldType(bt, name, c.pkgs)
	if err != nil {
		is := Issue{Code: "unknown_field", ID: id, Message: err.Error()}
		if s := Suggest(name, fields); s != "" {
			is.Hint = "did you mean " + s + "?"
		} else if len(fields) > 0 {
			is.Hint = "fields: " + strings.Join(fields, ", ")
		}
		c.issue(is)
		return "invalid"
	}
	return ft
}

func (c *checker) expr(e *env, n *ir.Node) string {
	if n == nil {
		c.err(c.fn.ID, "missing_expr", "empty expression")
		return "invalid"
	}
	t := c.expr0(e, n)
	c.r.Types[n.ID] = t
	return t
}

func (c *checker) want(e *env, n *ir.Node, want, what string) {
	if t := c.expr(e, n); t != want && t != "invalid" {
		c.mismatch(n.ID, what, t, want)
	}
}

var opText = map[string]string{
	"add": "+", "sub": "-", "mul": "*", "div": "/", "mod": "%", "and": "&", "or": "|", "xor": "^",
	"shl": "<<", "shr": ">>", "eq": "==", "ne": "!=", "lt": "<", "le": "<=", "gt": ">", "ge": ">=",
	"land": "&&", "lor": "||", "not": "!", "neg": "-", "bnot": "^",
}

func (c *checker) expr0(e *env, n *ir.Node) string {
	switch n.Op {
	case "int":
		return "i64"
	case "bool":
		return "bool"
	case "strptr", "strlen":
		return "i64"
	case "name":
		if n.Pkg != "" && n.Pkg != c.pkg.Path {
			return c.pkgConst(n)
		}
		if t, ok := e.get(n.Name); ok {
			return t
		}
		for _, cn := range c.pkg.Consts {
			if cn.Name == n.Name {
				return "i64"
			}
		}
		c.unknownName(n.ID, n.Name, e)
		return "invalid"
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr":
		c.want(e, n.Left, "i64", "left of "+opText[n.Op])
		c.want(e, n.Right, "i64", "right of "+opText[n.Op])
		return "i64"
	case "lt", "le", "gt", "ge":
		c.want(e, n.Left, "i64", "left of "+opText[n.Op])
		c.want(e, n.Right, "i64", "right of "+opText[n.Op])
		return "bool"
	case "eq", "ne":
		lt := c.expr(e, n.Left)
		rt := c.expr(e, n.Right)
		if lt != rt && lt != "invalid" && rt != "invalid" {
			c.mismatch(n.Right.ID, "right of "+opText[n.Op], rt, lt)
		}
		return "bool"
	case "land", "lor":
		c.want(e, n.Left, "bool", "left of "+opText[n.Op])
		c.want(e, n.Right, "bool", "right of "+opText[n.Op])
		return "bool"
	case "not":
		c.want(e, n.Arg, "bool", "operand of !")
		return "bool"
	case "neg", "bnot":
		c.want(e, n.Arg, "i64", "operand of "+opText[n.Op])
		return "i64"
	case "cast":
		t, err := c.resolve(n.Type)
		if err != nil {
			c.err(n.ID, "bad_type", err.Error())
			return "invalid"
		}
		src := c.expr(e, n.Arg)
		if src != "invalid" && !scalar(t) {
			c.mismatch(n.ID, "cast", src, "i64, bool, or a pointer type")
		}
		return t
	case "sizeof":
		if n.Type == "i64" || n.Type == "bool" {
			c.issue(Issue{Code: "bad_type", ID: n.ID, Message: "sizeof(" + n.Type + ") is always 8", Hint: "write 8; sizeof takes a struct type"})
			return "i64"
		}
		if _, err := c.resolve(n.Type); err != nil {
			c.err(n.ID, "bad_type", err.Error())
		}
		return "i64"
	case "field":
		return c.field(n.ID, c.expr(e, n.Base), n.Name)
	case "call":
		return c.call(e, n)
	case "syscall":
		// The path alone is not enough: the package must be the one the
		// toolchain ships, which a module cannot supply.
		if c.pkg.Path != "ovid/io" || !c.pkg.Sys {
			c.issue(Issue{Code: "syscall_forbidden", ID: n.ID, Message: "syscall is only valid in package ovid/io", Hint: "call an ovid/io function instead"})
		}
		if len(n.Args) != 7 {
			c.issue(Issue{Code: "arity", ID: n.ID, Message: "syscall takes 7 arguments", Expected: "7", Got: fmt.Sprint(len(n.Args))})
		}
		for i, a := range n.Args {
			c.want(e, a, "i64", fmt.Sprintf("syscall argument %d", i+1))
		}
		return "i64"
	case "load8", "load32", "load64":
		c.want(e, n.Arg, "i64", n.Op+" address")
		return "i64"
	}
	c.err(n.ID, "bad_op", "unknown expression op "+n.Op)
	return "invalid"
}

// pkgConst checks a reference to another package's const, path.Name.
func (c *checker) pkgConst(n *ir.Node) string {
	pk := c.pkgs[n.Pkg]
	var cands []string
	if pk != nil {
		for _, cn := range pk.Consts {
			if cn.Name == n.Name {
				return "i64"
			}
			cands = append(cands, cn.Name)
		}
	}
	is := Issue{Code: "unknown_name", ID: n.ID, Message: "undefined const " + n.Pkg + "." + n.Name}
	if s := Suggest(n.Name, cands); s != "" {
		is.Hint = "did you mean " + n.Pkg + "." + s + "?"
	} else if pk == nil {
		is.Hint = "there is no package " + n.Pkg
	} else if _, ok := c.sigs[n.Pkg+"."+n.Name]; ok {
		is.Hint = n.Name + " is a func; call it: " + n.Pkg + "." + n.Name + "(...)"
	}
	c.issue(is)
	return "invalid"
}

func (c *checker) call(e *env, n *ir.Node) string {
	path := n.Pkg
	missing := false
	if path == "" {
		path = c.pkg.Path
	} else if path != c.pkg.Path && !c.imported[path] {
		missing = true
		c.issue(Issue{Code: "missing_import", ID: n.ID, Message: "call to " + path + "." + n.Func + " but " + path + " is not imported",
			Hint: "add `import " + path + "` after the package line"})
	}
	sg, ok := c.sigs[path+"."+n.Func]
	if !ok && c.dupName[path+"."+n.Func] {
		// The only func of this name repeats a const or type. That is
		// reported there, and a call to it is not checked.
		for _, a := range n.Args {
			c.expr(e, a)
		}
		return "invalid"
	}
	if !ok && missing && c.pkgs[path] == nil {
		// A package nothing imports is not loaded, so whether it has the
		// func is unknown; the missing import is the whole report.
		for _, a := range n.Args {
			c.expr(e, a)
		}
		return "invalid"
	}
	if !ok {
		var cands []string
		if pk := c.pkgs[path]; pk != nil {
			for _, f := range pk.Funcs {
				cands = append(cands, f.Name)
			}
		}
		is := Issue{Code: "unknown_name", ID: n.ID, Message: "undefined function " + path + "." + n.Func}
		if s := Suggest(n.Func, cands); s != "" {
			is.Hint = "did you mean " + s + "?"
		} else if c.pkgs[path] == nil {
			is.Hint = "there is no package " + path
		}
		c.issue(is)
		for _, a := range n.Args {
			c.expr(e, a)
		}
		return "invalid"
	}
	if len(n.Args) != len(sg.params) {
		c.issue(Issue{Code: "arity", ID: n.ID, Message: fmt.Sprintf("%s takes %d arguments, got %d", n.Func, len(sg.params), len(n.Args)),
			Expected: fmt.Sprint(len(sg.params)), Got: fmt.Sprint(len(n.Args)), Hint: c.sigText(path, n.Func)})
	}
	for i, a := range n.Args {
		at := c.expr(e, a)
		if i < len(sg.params) && at != sg.params[i] && at != "invalid" && sg.params[i] != "invalid" {
			c.issue(Issue{Code: "type_mismatch", ID: a.ID,
				Message:  fmt.Sprintf("argument %d (%s) of %s: got %s, want %s", i+1, sg.names[i], n.Func, at, sg.params[i]),
				Expected: sg.params[i], Got: at, Hint: c.sigText(path, n.Func)})
		}
	}
	return sg.result
}

func (c *checker) sigText(path, name string) string {
	pk := c.pkgs[path]
	for i := range pk.Funcs {
		if pk.Funcs[i].Name == name {
			return path + ": " + Signature(path, &pk.Funcs[i])
		}
	}
	return ""
}

// FieldType returns the type of field on pointer type baseType, and the
// field names of the struct for hints.
func FieldType(baseType, field string, pkgs map[string]*ir.Package) (string, []string, error) {
	if !strings.HasPrefix(baseType, "*") {
		return "", nil, fmt.Errorf("field %s on %s, which is not a struct pointer", field, baseType)
	}
	full := strings.TrimPrefix(baseType, "*")
	i := strings.LastIndex(full, ".")
	pkg := pkgs[full[:max(i, 0)]]
	if i < 0 || pkg == nil {
		return "", nil, fmt.Errorf("bad pointer type %s", baseType)
	}
	name := full[i+1:]
	for _, t := range pkg.Types {
		if t.Name != name {
			continue
		}
		var names []string
		for _, f := range t.Fields {
			names = append(names, f.Name)
			if f.Name == field {
				ft, err := Resolve(pkg, f.Type, pkgs)
				return ft, nil, err
			}
		}
		return "", names, fmt.Errorf("%s has no field %s", full, field)
	}
	return "", nil, fmt.Errorf("unknown type %s", full)
}

// Suggest returns the candidate closest to name, if any is close enough.
func Suggest(name string, cands []string) string {
	sort.Strings(cands)
	best, bestD := "", 3
	if len(name) <= 3 {
		bestD = 2
	}
	for _, c := range cands {
		if c == name {
			continue
		}
		d := lev(strings.ToLower(name), strings.ToLower(c))
		if d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func lev(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
