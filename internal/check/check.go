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
	// Uses lists every name the checker resolved to a declaration, in the
	// order it checked them.
	Uses  []Use
	Funcs int
}

// Use is one name in source that resolves to a declaration. Span is the name
// token alone (T in *path.T, F in path.F(), x in p.x), so a rename of Target
// rewrites exactly Span.
type Use struct {
	Target string  // the declaration: fn:, ty:, fld:, cn:, pa:, or a var's st: id
	ID     string  // the node that spells it: an expression, statement, field, param, or func
	Kind   string  // call, field, setfield, name, assign, type, result
	In     string  // the type or func the use sits in
	Span   ir.Span // the name token
}

type sig struct {
	params []string
	names  []string
	result string
	two    bool // the func also returns an error code (i64)
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
	two      bool              // the func being checked returns two results
	recv     bool              // the call being checked is received by a var2 or assign2
	dup      map[*ir.Func]bool // funcs whose name was already declared
	dupName  map[string]bool   // those funcs, as pkg.Name
	lean     bool              // record no Types and no Uses
}

func (c *checker) issue(is Issue) { c.r.Issues = append(c.r.Issues, is) }

func (c *checker) use(target, id, kind, in string, sp ir.Span) {
	if c.lean {
		return
	}
	c.r.Uses = append(c.r.Uses, Use{Target: target, ID: id, Kind: kind, In: in, Span: sp})
}

// useType records a use of the struct type t resolves to, if it is one.
func (c *checker) useType(t, id, kind, in string, sp ir.Span) {
	t = strings.TrimPrefix(t, "*")
	if t == "" || t == "i64" || t == "bool" || t == "invalid" {
		return
	}
	c.use("ty:"+t, id, kind, in, sp)
}

func (c *checker) err(id, code, msg string) {
	c.issue(Issue{Code: code, ID: id, Message: msg})
}

func (c *checker) mismatch(id, what, got, want string) {
	c.issue(Issue{Code: "type_mismatch", ID: id, Message: fmt.Sprintf("%s: got %s, want %s", what, got, want), Expected: want, Got: got})
}

// Run checks p and records, besides the issues, the type of every
// expression and every use the checker resolved.
func Run(p *ir.Program) *Result { return run(p, false) }

// Errors checks p for its issues only: Types and Uses stay empty. That is
// all check, build, run, test, and an edit's before and after need, and the
// two tables are a seventh of a checked module's memory.
func Errors(p *ir.Program) *Result { return run(p, true) }

func run(p *ir.Program, lean bool) *Result {
	r := &Result{Types: map[string]string{}}
	c := &checker{r: r, pkgs: map[string]*ir.Package{}, sigs: map[string]sig{}, dup: map[*ir.Func]bool{}, dupName: map[string]bool{}, lean: lean}
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
			if cn.Table {
				c.r.Facts = append(c.r.Facts, Fact{"fact": "table", "id": cn.ID, "len": len(cn.Values)})
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
				c.useType(ft, f.ID, "type", t.ID, f.TypeSpan)
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
				c.useType(pt, pa.ID, "type", fn.ID, pa.TypeSpan)
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
			c.useType(rt, fn.ID, "result", fn.ID, fn.ResultSpan)
			c.sigs[pkg.Path+"."+fn.Name] = sig{params: ps, names: names, result: rt, two: fn.Result2 != "", id: fn.ID}
			c.r.Facts = append(c.r.Facts, Fact{"fact": "func", "id": fn.ID, "sig": Signature(pkg.Path, fn)})
			for _, st := range fn.Body {
				st.Walk(func(n *ir.Node) { claim(n.ID) })
			}
		}
	}

	c.checkCycles(p)
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

// checkCycles reports import_cycle once for each cycle whose lexically
// least package is root: at the first import of root, in source order,
// that leads back to root through packages whose paths sort after it.
// Packages must form a DAG, so that each can be checked and compiled once
// its imports are.
//
// The root walk below costs up to the whole graph per root, so it runs
// only where it can report: a cycle lies entirely within one strongly
// connected component of the import graph, so a root alone in its
// component (a self-import is import_self's, not a cycle) reports
// nothing, and the walk from a root never needs a package outside the
// root's component. A package the walk could enter from the root that
// is outside the component cannot reach the root, nor, since everything
// that reaches the root's component reaches the root, any package in
// it: walking into it only marks packages the walk never needs, so
// skipping it changes neither whether the walk returns true nor the path
// it finds. The components are found once, in time linear in the
// packages and imports, so a valid module costs that and nothing more.
// It returns the steps taken (component edges plus walk imports), which
// a test bounds.
func (c *checker) checkCycles(p *ir.Program) int {
	// The graph's nodes are the packages c.pkgs resolves paths to; a
	// repeated package's other copies are roots only, walked unpruned.
	idx := map[string]int{}
	var nodes []*ir.Package
	for i := range p.Packages {
		if pkg := &p.Packages[i]; c.pkgs[pkg.Path] == pkg {
			idx[pkg.Path] = len(nodes)
			nodes = append(nodes, pkg)
		}
	}
	comp, size, steps := sccs(nodes, idx)
	for i := range p.Packages {
		root := &p.Packages[i]
		in := -1
		if c.pkgs[root.Path] == root {
			in = comp[idx[root.Path]]
			if size[in] == 1 {
				continue
			}
		}
		steps += c.checkCycle(root, func(path string) bool { return in < 0 || comp[idx[path]] == in })
	}
	return steps
}

// sccs numbers the strongly connected components of the import graph on
// nodes (imports of unknown packages dropped) with Tarjan's algorithm,
// iteratively so a long import chain cannot exhaust the stack. It returns
// each node's component, each component's size, and the edges followed.
func sccs(nodes []*ir.Package, idx map[string]int) (comp, size []int, steps int) {
	n := len(nodes)
	adj := make([][]int, n)
	for v, pkg := range nodes {
		for _, im := range pkg.Imports {
			if w, ok := idx[im.Path]; ok {
				adj[v] = append(adj[v], w)
			}
		}
	}
	order := make([]int, n) // 1 + the visit number; 0 is unvisited
	low := make([]int, n)
	on := make([]bool, n)
	comp = make([]int, n)
	var stack []int
	type frame struct{ v, e int }
	var call []frame
	visited := 0
	visit := func(v int) {
		visited++
		order[v], low[v] = visited, visited
		stack = append(stack, v)
		on[v] = true
		call = append(call, frame{v, 0})
	}
	for s := range nodes {
		if order[s] != 0 {
			continue
		}
		visit(s)
		for len(call) > 0 {
			f := &call[len(call)-1]
			v := f.v
			if f.e < len(adj[v]) {
				w := adj[v][f.e]
				f.e++
				steps++
				if order[w] == 0 {
					visit(w)
				} else if on[w] && order[w] < low[v] {
					low[v] = order[w]
				}
				continue
			}
			call = call[:len(call)-1]
			if len(call) > 0 {
				if u := call[len(call)-1].v; low[v] < low[u] {
					low[u] = low[v]
				}
			}
			if low[v] == order[v] {
				k, cnt := len(size), 0
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					on[w] = false
					comp[w] = k
					cnt++
					if w == v {
						break
					}
				}
				size = append(size, cnt)
			}
		}
	}
	return comp, size, steps
}

// checkCycle reports root's cycle, if it has one, walking imports in
// source order, so it finds the same cycle path whatever order the
// packages were loaded in; keep says which packages the walk may enter.
// It returns the imports it looked at.
func (c *checker) checkCycle(root *ir.Package, keep func(path string) bool) int {
	seen := map[string]bool{}
	steps := 0
	var path []string
	var walk func(pkg *ir.Package) bool
	walk = func(pkg *ir.Package) bool {
		if seen[pkg.Path] {
			return false
		}
		seen[pkg.Path] = true
		path = append(path, pkg.Path)
		for _, im := range pkg.Imports {
			steps++
			if im.Path == root.Path {
				path = append(path, root.Path)
				return true
			}
			if next, ok := c.pkgs[im.Path]; ok && im.Path > root.Path && keep(im.Path) && walk(next) {
				return true
			}
		}
		path = path[:len(path)-1]
		return false
	}
	for _, im := range root.Imports {
		steps++
		next, ok := c.pkgs[im.Path]
		if !ok || im.Path <= root.Path || !keep(im.Path) {
			continue
		}
		path = []string{root.Path}
		if walk(next) {
			c.issue(Issue{Code: "import_cycle", ID: im.ID, Message: "import cycle: " + strings.Join(path, " -> "),
				Hint: "packages may not import each other, directly or through others; move what they share into a package both import"})
			return steps
		}
	}
	return steps
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
		// With no main, a handle is the entry: build writes the stdio host
		// around it (tool.serveProgram).
		var handle *ir.Func
		for i := range ep.Funcs {
			if ep.Funcs[i].Name == "handle" && !c.dup[&ep.Funcs[i]] {
				handle = &ep.Funcs[i]
			}
		}
		if handle == nil {
			c.issue(Issue{Code: "bad_main", ID: ep.ID, Message: "entry package has no main", Expected: want})
			return
		}
		c.pkg = ep
		hw := "func handle(io *ovid/io.Cap, req *ovid/http.Request, res *ovid/http.Response) i64"
		ok := len(handle.Params) == 3
		for i, t := range []string{"*ovid/io.Cap", "*ovid/http.Request", "*ovid/http.Response"} {
			if ok {
				pt, _ := c.resolve(handle.Params[i].Type)
				ok = pt == t
			}
		}
		if res, _ := c.resolve(handle.Result); !ok || res != "i64" || handle.Result2 != "" {
			c.issue(Issue{Code: "bad_handler", ID: handle.ID, Message: "handle has the wrong signature", Expected: hw, Got: Signature(ep.Path, handle)})
		}
	} else {
		c.pkg = ep
		pt := ""
		if len(main.Params) == 1 {
			pt, _ = c.resolve(main.Params[0].Type)
		}
		res, _ := c.resolve(main.Result)
		if len(main.Params) != 1 || pt != "*ovid/io.Cap" || res != "i64" || main.Result2 != "" {
			c.issue(Issue{Code: "bad_main", ID: main.ID, Message: "main has the wrong signature", Expected: want, Got: Signature(ep.Path, main)})
		}
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
		need := []string{"argc", "argv", "heap", "used", "size", "maps"}
		for i, n := range need {
			if i >= len(capTy.Fields) || capTy.Fields[i].Name != n || capTy.Fields[i].Type != "i64" {
				c.issue(Issue{Code: "bad_abi", ID: capTy.ID, Message: "the runtime fills Cap's first six fields", Expected: "argc, argv, heap, used, size, maps (all i64, in order)"})
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
	if fn.Result2 != "" {
		b.WriteString("(" + ShowType(pkg, fn.Result) + ", " + fn.Result2 + ")")
		return b.String()
	}
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
	ids  map[string]string // name -> the pa: or st: id that declared it
	up   *env
}

func newEnv(up *env) *env {
	return &env{vars: map[string]string{}, ids: map[string]string{}, up: up}
}

func (e *env) get(name string) (string, bool) {
	t, _, ok := e.lookup(name)
	return t, ok
}

// lookup returns a local's type and the id of its declaration.
func (e *env) lookup(name string) (string, string, bool) {
	for ; e != nil; e = e.up {
		if t, ok := e.vars[name]; ok {
			return t, e.ids[name], true
		}
	}
	return "", "", false
}

func (e *env) bind(name, t, id string) {
	e.vars[name] = t
	e.ids[name] = id
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
	e := newEnv(nil)
	for _, pa := range fn.Params {
		if _, ok := e.vars[pa.Name]; ok {
			c.issue(Issue{Code: "duplicate_name", ID: pa.ID, At: &pa.Span, Message: "parameter " + pa.Name + " is declared twice"})
		}
		t, err := c.resolve(pa.Type)
		if err != nil {
			t = "invalid"
		}
		e.bind(pa.Name, t, pa.ID)
	}
	c.res, _ = c.resolve(fn.Result)
	if c.res == "" {
		c.res = "invalid"
	}
	c.two = fn.Result2 != ""
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
		c.useType(t, s.ID, "type", c.fn.ID, s.TypeSpan)
		if _, ok := e.get(s.Name); ok {
			c.err(s.ID, "duplicate_name", s.Name+" is already declared in this function; assign with `"+s.Name+" = ...` instead")
		}
		if s.Val != nil {
			vt := c.expr(e, s.Val)
			if vt != t && vt != "invalid" && t != "invalid" {
				c.mismatch(s.Val.ID, "var "+s.Name, vt, t)
			}
		}
		e.bind(s.Name, t, s.ID)
	case "assign":
		t, id, ok := e.lookup(s.Name)
		if !ok {
			c.unknownName(s.ID, s.Name, e)
			t = "invalid"
		} else {
			c.use(id, s.ID, "assign", c.fn.ID, s.NameSpan)
		}
		vt := c.expr(e, s.Val)
		if vt != t && vt != "invalid" && t != "invalid" {
			c.mismatch(s.Val.ID, "assign to "+s.Name, vt, t)
		}
	case "var2", "assign2":
		// Each name's type: declared (var2) or the local's (assign2); "_"
		// takes anything. The call's results must match them in order.
		ts := [2]string{}
		for i, name := range []string{s.Name, s.Two.Name} {
			if name == "_" {
				ts[i] = "_"
				continue
			}
			if i == 1 && name == s.Name {
				c.err(s.ID, "duplicate_name", name+" receives both results; give the error code its own name, or _")
			}
			if s.Op == "var2" {
				typ, sp := s.Type, s.TypeSpan
				if i == 1 {
					typ, sp = s.Two.Type, s.Two.TypeSpan
				}
				t, err := c.resolve(typ)
				if err != nil {
					c.err(s.ID, "bad_type", err.Error())
					t = "invalid"
				} else if !scalar(t) {
					c.err(s.ID, "struct_value", "local must be i64, bool, or a pointer; write *"+t)
				}
				c.useType(t, s.ID, "type", c.fn.ID, sp)
				if _, ok := e.get(name); ok {
					c.err(s.ID, "duplicate_name", name+" is already declared in this function; assign with `"+name+" = ...` instead")
				}
				ts[i] = t
			} else {
				sp := s.NameSpan
				if i == 1 {
					sp = s.Two.NameSpan
				}
				t, id, ok := e.lookup(name)
				if !ok {
					c.unknownName(s.ID, name, e)
					t = "invalid"
				} else {
					c.use(id, s.ID, "assign", c.fn.ID, sp)
				}
				ts[i] = t
			}
		}
		// Only a call that is the whole value is received; a two-result
		// call nested in an expression is used as one value.
		sg, direct := c.callSig(s.Val)
		c.recv = direct && sg.two
		vt := c.expr(e, s.Val)
		c.recv = false
		if !direct || !sg.two {
			if vt != "invalid" {
				c.issue(Issue{Code: "arity", ID: s.Val.ID, Message: "two names receive one value",
					Expected: "a call to a func with two results", Got: "one value",
					Hint: "only a func declared (T, i64) returns two results"})
			}
		} else {
			for i, want := range []string{sg.result, "i64"} {
				if ts[i] != "_" && ts[i] != "invalid" && want != "invalid" && ts[i] != want {
					c.mismatch(s.Val.ID, []string{"first", "second"}[i]+" result into "+[]string{s.Name, s.Two.Name}[i], want, ts[i])
				}
			}
		}
		if s.Op == "var2" {
			for i, name := range []string{s.Name, s.Two.Name} {
				if name != "_" {
					e.bind(name, ts[i], s.ID)
				}
			}
		}
	case "setfield":
		bt := c.expr(e, s.Base)
		ft := c.field(s, bt)
		vt := c.expr(e, s.Val)
		if vt != ft && vt != "invalid" && ft != "invalid" {
			c.mismatch(s.Val.ID, "field "+s.Name, vt, ft)
		}
	case "store8", "store16", "store32", "store64":
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
		// return f(...) forwards both results of a two-result call.
		forward := false
		if sg, ok := c.callSig(s.Val); ok && sg.two && c.two && s.Val2 == nil {
			c.recv, forward = true, true
		}
		vt := c.expr(e, s.Val)
		if vt != c.res && vt != "invalid" && c.res != "invalid" {
			c.mismatch(s.Val.ID, "return value of "+c.fn.Name, vt, c.res)
		}
		if forward {
			return
		}
		if s.Val2 != nil && !c.two {
			c.issue(Issue{Code: "arity", ID: s.ID, Message: c.fn.Name + " returns one value", Expected: "1", Got: "2",
				Hint: "declare the func (" + c.fn.Result + ", i64) to return an error code too"})
		} else if s.Val2 == nil && c.two {
			c.issue(Issue{Code: "arity", ID: s.ID, Message: c.fn.Name + " returns a value and an error code", Expected: "2", Got: "1",
				Hint: "write return v, 0 on success and return 0, code on failure"})
		}
		if s.Val2 != nil {
			if et := c.expr(e, s.Val2); et != "i64" && et != "invalid" {
				c.mismatch(s.Val2.ID, "error code returned by "+c.fn.Name, et, "i64")
			}
		}
	case "if":
		if ct := c.expr(e, s.Cond); ct != "bool" && ct != "invalid" {
			c.mismatch(s.Cond.ID, "if condition", ct, "bool")
		}
		c.stmts(newEnv(e), s.Then)
		c.stmts(newEnv(e), s.Else)
	case "while":
		if ct := c.expr(e, s.Cond); ct != "bool" && ct != "invalid" {
			c.mismatch(s.Cond.ID, "while condition", ct, "bool")
		}
		c.stmts(newEnv(e), s.Body)
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

// field checks n.Name on a value of type bt, for a field or setfield node.
// opaque reports whether t is a pointer to one of ovid/io's types seen from
// outside ovid/io. Those types are handles on what the program may do
// (allocate, read arguments, and whatever the library adds), and a handle is
// only worth something if it cannot be forged or taken apart: so outside
// ovid/io one cannot be made by a cast or turned into an address by one,
// and its fields cannot be read or written.
func (c *checker) opaque(t string) bool {
	return strings.HasPrefix(t, "*ovid/io.") && !(c.pkg.Path == "ovid/io" && c.pkg.Sys)
}

func (c *checker) field(n *ir.Node, bt string) string {
	if bt == "invalid" {
		return "invalid"
	}
	if c.opaque(bt) {
		c.issue(Issue{Code: "opaque_type", ID: n.ID, Message: "the fields of " + bt + " belong to ovid/io", Hint: "call an ovid/io func instead"})
		return "invalid"
	}
	ft, fields, err := FieldType(bt, n.Name, c.pkgs)
	if err != nil {
		is := Issue{Code: "unknown_field", ID: n.ID, Message: err.Error()}
		if s := Suggest(n.Name, fields); s != "" {
			is.Hint = "did you mean " + s + "?"
		} else if len(fields) > 0 {
			is.Hint = "fields: " + strings.Join(fields, ", ")
		}
		c.issue(is)
		return "invalid"
	}
	c.use("fld:"+strings.TrimPrefix(bt, "*")+"."+n.Name, n.ID, n.Op, c.fn.ID, n.NameSpan)
	return ft
}

func (c *checker) expr(e *env, n *ir.Node) string {
	if n == nil {
		c.err(c.fn.ID, "missing_expr", "empty expression")
		return "invalid"
	}
	t := c.expr0(e, n)
	if !c.lean {
		c.r.Types[n.ID] = t
	}
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
		if t, id, ok := e.lookup(n.Name); ok {
			c.use(id, n.ID, "name", c.fn.ID, n.NameSpan)
			return t
		}
		for i := range c.pkg.Consts {
			if cn := &c.pkg.Consts[i]; cn.Name == n.Name {
				c.use(cn.ID, n.ID, "name", c.fn.ID, n.NameSpan)
				if cn.Table {
					c.tableAsValue(n, cn)
					return "invalid"
				}
				return "i64"
			}
		}
		c.unknownName(n.ID, n.Name, e)
		return "invalid"
	case "index", "len":
		// A param or local of the name shadows a table, as it does a const.
		var cn *ir.Const
		if _, id, ok := e.lookup(n.Name); ok && n.Pkg == "" {
			c.use(id, n.ID, "name", c.fn.ID, n.NameSpan)
			c.issue(Issue{Code: "bad_type", ID: n.ID, Message: n.Name + " is a variable, not a table",
				Hint: "only a const declared [N]i64 can be indexed or measured"})
		} else {
			cn = c.table(n)
		}
		if n.Op == "index" {
			c.want(e, n.Arg, "i64", "index of "+n.Name)
		}
		if cn == nil {
			return "invalid"
		}
		return "i64"
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr":
		c.want(e, n.Left, "i64", "left of "+opText[n.Op])
		c.want(e, n.Right, "i64", "right of "+opText[n.Op])
		return "i64"
	case "ushr", "umulhi", "udiv", "urem", "ult":
		c.want(e, n.Left, "i64", "argument 1 of "+n.Op)
		c.want(e, n.Right, "i64", "argument 2 of "+n.Op)
		if n.Op == "ult" {
			return "bool"
		}
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
		c.useType(t, n.ID, "type", c.fn.ID, n.TypeSpan)
		if c.opaque(t) {
			c.issue(Issue{Code: "opaque_type", ID: n.ID, Message: "a " + t + " cannot be made by a cast", Hint: "ovid/io's types are handles: get one from main or from an ovid/io func"})
		}
		src := c.expr(e, n.Arg)
		if c.opaque(src) {
			c.issue(Issue{Code: "opaque_type", ID: n.ID, Message: "a " + src + " cannot be cast: it is not an address to compute with", Hint: "ovid/io's types are handles: ask an ovid/io func about one"})
		}
		if src != "invalid" && !scalar(t) {
			c.mismatch(n.ID, "cast", src, "i64, bool, or a pointer type")
		}
		return t
	case "sizeof":
		if n.Type == "i64" || n.Type == "bool" {
			c.issue(Issue{Code: "bad_type", ID: n.ID, Message: "sizeof(" + n.Type + ") is always 8", Hint: "write 8; sizeof takes a struct type"})
			return "i64"
		}
		if t, err := c.resolve(n.Type); err != nil {
			c.err(n.ID, "bad_type", err.Error())
		} else {
			c.useType(t, n.ID, "type", c.fn.ID, n.TypeSpan)
		}
		return "i64"
	case "field":
		return c.field(n, c.expr(e, n.Base))
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
	case "load8", "load16", "load32", "load64":
		c.want(e, n.Arg, "i64", n.Op+" address")
		return "i64"
	case "bswap16", "bswap32", "bswap64":
		c.want(e, n.Arg, "i64", "operand of "+n.Op)
		return "i64"
	}
	c.err(n.ID, "bad_op", "unknown expression op "+n.Op)
	return "invalid"
}

// pkgConst checks a reference to another package's const, path.Name.
// tableAsValue reports a table used where a value is wanted.
func (c *checker) tableAsValue(n *ir.Node, cn *ir.Const) {
	c.issue(Issue{Code: "bad_type", ID: n.ID, Message: cn.Name + " is a table, not a value",
		Hint: "read an element with " + cn.Name + "[i], or its length with len(" + cn.Name + ")"})
}

// table resolves the table an index or len node names, in this package or
// the one it spells, reporting an unknown name or a const that is no table.
func (c *checker) table(n *ir.Node) *ir.Const {
	path := n.Pkg
	if path == "" {
		path = c.pkg.Path
	}
	pk := c.pkgs[path]
	if pk != nil {
		for i := range pk.Consts {
			if cn := &pk.Consts[i]; cn.Name == n.Name {
				c.use(cn.ID, n.ID, "name", c.fn.ID, n.NameSpan)
				if !cn.Table {
					c.issue(Issue{Code: "bad_type", ID: n.ID, Message: cn.Name + " is a const, not a table",
						Hint: "only a const declared [N]i64 can be indexed or measured"})
					return nil
				}
				return cn
			}
		}
	}
	is := Issue{Code: "unknown_name", ID: n.ID, Message: "undefined table " + path + "." + n.Name}
	if pk == nil {
		is.Hint = "there is no package " + path
	}
	c.issue(is)
	return nil
}

func (c *checker) pkgConst(n *ir.Node) string {
	pk := c.pkgs[n.Pkg]
	var cands []string
	if pk != nil {
		for i := range pk.Consts {
			if cn := &pk.Consts[i]; cn.Name == n.Name {
				c.use(cn.ID, n.ID, "name", c.fn.ID, n.NameSpan)
				if cn.Table {
					c.tableAsValue(n, cn)
					return "invalid"
				}
				return "i64"
			}
			cands = append(cands, pk.Consts[i].Name)
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
	c.use(sg.id, n.ID, "call", c.fn.ID, n.NameSpan)
	if sg.two && !c.recv {
		c.issue(Issue{Code: "unused_result", ID: n.ID, Message: n.Func + " returns a value and an error code; only a var or an assignment of two names can receive them",
			Hint: "var v " + sg.result + ", e i64 = " + n.Func + "(...), or _ for the one not needed"})
	}
	c.recv = false
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

// callSig is the signature of the func a call node names, if it is a call
// to a known func.
func (c *checker) callSig(n *ir.Node) (sig, bool) {
	if n == nil || n.Op != "call" {
		return sig{}, false
	}
	path := n.Pkg
	if path == "" {
		path = c.pkg.Path
	}
	sg, ok := c.sigs[path+"."+n.Func]
	return sg, ok
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
