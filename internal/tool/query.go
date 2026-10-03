package tool

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"ovid/internal/check"
	"ovid/internal/ir"
	"ovid/internal/module"
)

// Outline lists packages, or with pkg set, that package's declarations as
// one line each with signature, location, and hash.
func Outline(dir, pkg string, all bool, w io.Writer) int {
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	for _, d := range m.Errors {
		emit(w, d)
	}
	found := pkg == ""
	count := 0
	for pi := range m.Prog.Packages {
		p := &m.Prog.Packages[pi]
		if pkg != "" && p.Path != pkg {
			continue
		}
		found = true
		if pkg == "" && !all {
			var ims []string
			for _, im := range p.Imports {
				ims = append(ims, im.Path)
			}
			r := map[string]any{"kind": "package", "id": p.ID, "path": p.Path, "funcs": len(p.Funcs),
				"types": len(p.Types), "consts": len(p.Consts), "imports": ims}
			if m.Std[p.Path] {
				r["std"] = true
			} else {
				r["files"] = pkgFiles(m, p)
			}
			emit(w, r)
			count++
			continue
		}
		for _, l := range declLocs(m, p) {
			emit(w, declLine(m, l))
			count++
		}
	}
	if !found {
		var paths []string
		for _, p := range m.Prog.Packages {
			paths = append(paths, p.Path)
		}
		return fail(w, "not_found", "no package "+pkg, "packages: "+strings.Join(paths, ", "))
	}
	r := map[string]any{"ok": true, "count": count, "revision": m.Revision()}
	if pkg == "" && !all {
		r["hint"] = "`ovid outline --pkg <path>` lists declarations; `ovid show <id>` prints source"
	}
	emit(w, r)
	return ExitOK
}

func pkgFiles(m *module.Module, p *ir.Package) []string {
	seen := map[int]bool{}
	var out []string
	add := func(s ir.Span) {
		if !seen[s.File] {
			seen[s.File] = true
			out = append(out, m.DisplayPath(m.Files[s.File]))
		}
	}
	add(p.Span)
	for _, f := range p.Funcs {
		add(f.Span)
	}
	for _, t := range p.Types {
		add(t.Span)
	}
	return out
}

func declLocs(m *module.Module, p *ir.Package) []*module.Loc {
	var out []*module.Loc
	for _, c := range p.Consts {
		out = append(out, m.Index[c.ID])
	}
	for _, t := range p.Types {
		out = append(out, m.Index[t.ID])
	}
	for _, f := range p.Funcs {
		out = append(out, m.Index[f.ID])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Span, out[j].Span
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Off < b.Off
	})
	return out
}

func declLine(m *module.Module, l *module.Loc) map[string]any {
	file, a, b, _ := m.Where(l.Span)
	r := map[string]any{"id": l.ID, "kind": l.Kind, "sig": sigOf(l), "file": file, "line": a.Line, "end_line": b.Line, "hash": m.Hash(l.ID)}
	if doc := docComment(m.Files[l.Span.File].Src, l.Span.Off); doc != "" {
		r["doc"] = doc
	}
	switch n := l.Node.(type) {
	case *ir.TypeDecl:
		// What to pass ovid/io.Alloc for one of these.
		r["size"] = 8 * len(n.Fields)
	case *ir.Func:
		if isTest(n) {
			r["test"] = true
		}
	}
	return r
}

func sigOf(l *module.Loc) string {
	switch n := l.Node.(type) {
	case *ir.Func:
		return check.Signature(l.Pkg, n)
	case *ir.TypeDecl:
		var fs []string
		for _, f := range n.Fields {
			fs = append(fs, f.Name+" "+check.ShowType(l.Pkg, f.Type))
		}
		return "type " + n.Name + " struct { " + strings.Join(fs, "; ") + " }"
	case *ir.Const:
		return fmt.Sprintf("const %s i64 = %d", n.Name, n.Value)
	case *ir.Field:
		return n.Name + " " + check.ShowType(l.Pkg, n.Type)
	case *ir.Param:
		return n.Name + " " + check.ShowType(l.Pkg, n.Type)
	case *ir.Import:
		return "import " + n.Path
	case *ir.Package:
		return "package " + n.Path
	}
	return ""
}

// Show prints the source of each id. Text mode is source with a header line;
// ids adds a trailing `// @id` on lines where a statement starts.
func Show(dir string, ids []string, withIDs, asJSON bool, w io.Writer) int {
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	var types map[string]string
	if len(m.Errors) == 0 {
		types = check.Run(m.Prog).Types
	}
	var locs []*module.Loc
	for _, q := range ids {
		ls, err := m.Lookup(q)
		if err != nil {
			return fail(w, "not_found", err.Error(), "ids come from `ovid outline`, `ovid check`, or `ovid show --ids`")
		}
		locs = append(locs, ls...)
	}
	for _, l := range locs {
		file, a, b, _ := m.Where(l.Span)
		f := m.Files[l.Span.File]
		start := lineStart(f.Src, l.Span.Off)
		text := string(f.Src[start:l.Span.End])
		if asJSON {
			r := map[string]any{"id": l.ID, "kind": l.Kind, "file": file, "line": a.Line, "end_line": b.Line,
				"hash": m.Hash(l.ID), "decl": l.Decl, "parent": l.Parent, "text": m.Text(l.Span)}
			if t := types[l.ID]; t != "" {
				r["type"] = t
			}
			if s := sigOf(l); s != "" {
				r["sig"] = s
			}
			emit(w, r)
			continue
		}
		hdr := fmt.Sprintf("// %s %s %s:%d-%d hash=%s", l.Kind, l.ID, file, a.Line, b.Line, m.Hash(l.ID))
		if l.Decl != "" && l.Decl != l.ID {
			hdr += " in=" + l.Decl
		}
		if t := types[l.ID]; t != "" {
			hdr += " type=" + t
		}
		fmt.Fprintln(w, hdr)
		if withIDs {
			text = annotate(m, l, start, text)
		}
		fmt.Fprintln(w, text)
	}
	if asJSON {
		emit(w, map[string]any{"ok": true, "count": len(locs)})
	}
	return ExitOK
}

func lineStart(src []byte, off int) int {
	for off > 0 && src[off-1] != '\n' {
		if src[off-1] != ' ' && src[off-1] != '\t' {
			// Something else precedes the node on its line; start at the node.
			for off < len(src) && (src[off] == ' ' || src[off] == '\t') {
				off++
			}
			return off
		}
		off--
	}
	return off
}

// annotate tags each line where a statement (or the decl) starts.
func annotate(m *module.Module, root *module.Loc, base int, text string) string {
	starts := map[int][]string{}
	f := m.Files[root.Span.File]
	add := func(id string, off int) {
		ln := f.Pos(off).Line
		starts[ln] = append(starts[ln], id)
	}
	switch n := root.Node.(type) {
	case *ir.Func:
		add(n.ID, n.Span.Off)
		for _, st := range n.Body {
			st.Walk(func(c *ir.Node) {
				if ir.IsStmt(c.Op) {
					add(c.ID, c.Span.Off)
				}
			})
		}
	case *ir.Node:
		n.Walk(func(c *ir.Node) {
			if ir.IsStmt(c.Op) || c == n {
				add(c.ID, c.Span.Off)
			}
		})
	default:
		return text
	}
	first := f.Pos(base).Line
	lines := strings.Split(text, "\n")
	for i := range lines {
		if ids := starts[first+i]; len(ids) > 0 {
			lines[i] += "  // @" + strings.Join(ids, " @")
		}
	}
	return strings.Join(lines, "\n")
}

// Ref is one use of a declaration.
type ref struct {
	id   string
	kind string // call, field, setfield, name, assign, type, decl
	span ir.Span
	decl string
}

// Refs prints every use of the named declaration.
func Refs(dir, q string, w io.Writer) int {
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	if len(m.Errors) > 0 {
		c := &checked{m: m, diags: m.Errors}
		c.writeDiags(w)
		return fail(w, "syntax", "fix syntax errors first; refs needs a parsed module", "")
	}
	locs, err := m.Lookup(q)
	if err != nil {
		return fail(w, "not_found", err.Error(), "")
	}
	if len(locs) > 1 {
		var ids []string
		for _, l := range locs {
			ids = append(ids, l.ID)
		}
		return fail(w, "ambiguous", q+" names several declarations", "use a full id: "+strings.Join(ids, ", "))
	}
	target := locs[0]
	res := check.Run(m.Prog)
	rs, err := findRefs(m, res, target)
	if err != nil {
		return fail(w, "unsupported", err.Error(), "")
	}
	for _, r := range rs {
		file, a, _, line := m.Where(r.span)
		emit(w, map[string]any{"id": r.id, "kind": r.kind, "in": r.decl, "file": file, "line": a.Line, "col": a.Col, "source": strings.TrimSpace(line)})
	}
	emit(w, map[string]any{"ok": true, "target": target.ID, "count": len(rs)})
	return ExitOK
}

func findRefs(m *module.Module, res *check.Result, target *module.Loc) ([]ref, error) {
	var out []ref
	full := func(t string, pkg string) string {
		// Types in the tree are already resolved to pkg.T / *pkg.T.
		return strings.TrimPrefix(t, "*")
	}
	switch target.Kind {
	case "func":
		name := target.ID[strings.LastIndex(target.ID, ".")+1:]
		forEachNode(m, func(pkg *ir.Package, fn *ir.Func, n *ir.Node) {
			if n.Op != "call" || n.Func != name {
				return
			}
			p := n.Pkg
			if p == "" {
				p = pkg.Path
			}
			if p == target.Pkg {
				out = append(out, ref{id: n.ID, kind: "call", span: n.Span, decl: fn.ID})
			}
		})
	case "type":
		tname := target.Pkg + "." + target.ID[strings.LastIndex(target.ID, ".")+1:]
		for pi := range m.Prog.Packages {
			pkg := &m.Prog.Packages[pi]
			for _, t := range pkg.Types {
				for _, f := range t.Fields {
					if full(f.Type, pkg.Path) == tname {
						out = append(out, ref{id: f.ID, kind: "type", span: f.Span, decl: t.ID})
					}
				}
			}
			for fi := range pkg.Funcs {
				fn := &pkg.Funcs[fi]
				for _, pa := range fn.Params {
					if full(pa.Type, pkg.Path) == tname {
						out = append(out, ref{id: pa.ID, kind: "type", span: pa.Span, decl: fn.ID})
					}
				}
				if full(fn.Result, pkg.Path) == tname {
					out = append(out, ref{id: fn.ID, kind: "result", span: headerSpan(m, fn), decl: fn.ID})
				}
			}
		}
		forEachNode(m, func(pkg *ir.Package, fn *ir.Func, n *ir.Node) {
			if (n.Op == "var" || n.Op == "cast") && full(n.Type, pkg.Path) == tname {
				out = append(out, ref{id: n.ID, kind: "type", span: n.Span, decl: fn.ID})
			}
		})
	case "field":
		fl := target.Node.(*ir.Field)
		owner := "*" + strings.TrimPrefix(target.Decl, "ty:")
		forEachNode(m, func(pkg *ir.Package, fn *ir.Func, n *ir.Node) {
			if (n.Op == "field" || n.Op == "setfield") && n.Name == fl.Name && res.Types[n.Base.ID] == owner {
				out = append(out, ref{id: n.ID, kind: n.Op, span: n.Span, decl: fn.ID})
			}
		})
	case "const":
		cn := target.Node.(*ir.Const)
		forEachNode(m, func(pkg *ir.Package, fn *ir.Func, n *ir.Node) {
			if n.Op != "name" || n.Name != cn.Name {
				return
			}
			if (n.Pkg == "" && pkg.Path == target.Pkg && !localNamed(fn, cn.Name)) || n.Pkg == target.Pkg {
				out = append(out, ref{id: n.ID, kind: "name", span: n.Span, decl: fn.ID})
			}
		})
	case "param":
		pa := target.Node.(*ir.Param)
		fn := m.Index[target.Decl].Node.(*ir.Func)
		for _, st := range fn.Body {
			st.Walk(func(n *ir.Node) {
				if (n.Op == "name" || n.Op == "assign") && n.Name == pa.Name {
					out = append(out, ref{id: n.ID, kind: n.Op, span: n.Span, decl: fn.ID})
				}
			})
		}
	case "stmt":
		n := target.Node.(*ir.Node)
		if n.Op != "var" {
			return nil, fmt.Errorf("refs works on funcs, types, fields, consts, params, and var statements; %s is a %s statement", target.ID, n.Op)
		}
		fn := m.Index[target.Decl].Node.(*ir.Func)
		after := false
		for _, st := range fn.Body {
			st.Walk(func(c *ir.Node) {
				if c == n {
					after = true
					return
				}
				if after && (c.Op == "name" || c.Op == "assign") && c.Name == n.Name {
					out = append(out, ref{id: c.ID, kind: c.Op, span: c.Span, decl: fn.ID})
				}
			})
		}
	default:
		return nil, fmt.Errorf("refs works on funcs, types, fields, consts, params, and var statements, not %s", target.Kind)
	}
	return out, nil
}

func localNamed(fn *ir.Func, name string) bool {
	for _, pa := range fn.Params {
		if pa.Name == name {
			return true
		}
	}
	found := false
	for _, st := range fn.Body {
		st.Walk(func(n *ir.Node) {
			if n.Op == "var" && n.Name == name {
				found = true
			}
		})
	}
	return found
}

// headerSpan is the span of a func's signature line.
func headerSpan(m *module.Module, fn *ir.Func) ir.Span {
	src := m.Files[fn.Span.File].Src
	end := fn.Span.Off
	for end < fn.Span.End && src[end] != '{' {
		end++
	}
	return ir.Span{File: fn.Span.File, Off: fn.Span.Off, End: end}
}

func forEachNode(m *module.Module, f func(pkg *ir.Package, fn *ir.Func, n *ir.Node)) {
	for pi := range m.Prog.Packages {
		pkg := &m.Prog.Packages[pi]
		for fi := range pkg.Funcs {
			fn := &pkg.Funcs[fi]
			for _, st := range fn.Body {
				st.Walk(func(n *ir.Node) { f(pkg, fn, n) })
			}
		}
	}
}
