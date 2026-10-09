package tool

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"ovid/internal/check"
	"ovid/internal/ir"
	"ovid/internal/module"
)

// Outline lists packages, or with pkg set, that package's declarations.
// As text it prints each file's path once and then one line per decl:
// the line number and the signature (a struct by its field count), with
// the id and hash after --ids
// (ids, since a decl's id is its name, and the hash guards an edit). As
// JSON (asJSON) it prints one record per package or decl with everything.
// It prints the records of page; the last line counts them all and says
// where the next page starts.
func Outline(dir, pkg string, all, uses, ids, asJSON bool, page Page, w io.Writer) int {
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	for _, d := range m.Errors {
		emit(w, d)
	}
	found := pkg == ""
	pg := pager{Page: page}
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
			if !pg.take() {
				continue
			}
			if !asJSON {
				line := fmt.Sprintf("%s  funcs=%d types=%d consts=%d", p.Path, len(p.Funcs), len(p.Types), len(p.Consts))
				if len(ims) > 0 {
					line += "  imports " + strings.Join(ims, " ")
				}
				if m.Std[p.Path] {
					line += "  (std)"
				} else {
					line += "  " + strings.Join(pkgFiles(m, p), " ")
				}
				fmt.Fprintln(w, line)
				continue
			}
			r := map[string]any{"kind": "package", "id": p.ID, "path": p.Path, "funcs": len(p.Funcs),
				"types": len(p.Types), "consts": len(p.Consts), "imports": ims}
			if m.Std[p.Path] {
				r["std"] = true
			} else {
				r["files"] = pkgFiles(m, p)
			}
			emit(w, r)
			continue
		}
		var res *check.Result
		if uses && len(m.Errors) == 0 {
			res = check.Run(m.Prog)
		}
		lastFile := ""
		for _, l := range declLocs(m, p) {
			if !pg.take() {
				continue
			}
			d := declLine(m, l)
			if res != nil && (l.Kind == "func" || l.Kind == "type" || l.Kind == "const") {
				if rs, err := findRefs(m, res, l, ""); err == nil {
					d["used_by"] = usesByPkg(m, rs)
				}
			}
			if asJSON {
				emit(w, d)
				continue
			}
			if file := d["file"].(string); file != lastFile {
				fmt.Fprintln(w, file)
				lastFile = file
			}
			sig := d["sig"].(string)
			if td, ok := l.Node.(*ir.TypeDecl); ok {
				// A struct's fields are show's to print; the outline says how many.
				sig = fmt.Sprintf("type %s struct { %d fields }", td.Name, len(td.Fields))
			}
			line := fmt.Sprintf("%4d  %s", d["line"], sig)
			if n, ok := d["id_copies"]; ok {
				line += fmt.Sprintf("  (one of %d copies)", n)
			}
			if ids {
				line += fmt.Sprintf("  %s hash=%s", d["id"], d["hash"])
			}
			if ub, ok := d["used_by"].(map[string]int); ok {
				line += "  " + usedBy(ub)
			}
			fmt.Fprintln(w, line)
		}
	}
	if !found {
		var paths []string
		for _, p := range m.Prog.Packages {
			paths = append(paths, p.Path)
		}
		return fail(w, "not_found", "no package "+pkg, "packages: "+strings.Join(paths, ", "))
	}
	r := map[string]any{"ok": true, "revision": m.Revision()}
	pg.finish(r)
	if pkg == "" && !all && r["hint"] == nil {
		r["hint"] = "`ovid outline --pkg <path>` lists declarations; `ovid show <id>` prints source"
	}
	emit(w, r)
	return ExitOK
}

// usedBy renders a used_by map as text: "used by ovid/cli 3, ovid/cg 1",
// or "unused".
func usedBy(ub map[string]int) string {
	if len(ub) == 0 {
		return "unused"
	}
	var ps []string
	for p := range ub {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	var parts []string
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%s %d", p, ub[p]))
	}
	return "used by " + strings.Join(parts, ", ")
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
	// Each node's own Loc, so every copy of a duplicated id is listed
	// where it is.
	for i := range p.Consts {
		out = append(out, m.LocOf(&p.Consts[i]))
	}
	for i := range p.Types {
		out = append(out, m.LocOf(&p.Types[i]))
	}
	for i := range p.Funcs {
		out = append(out, m.LocOf(&p.Funcs[i]))
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
	r := map[string]any{"id": l.ID, "kind": l.Kind, "sig": sigOf(l), "file": file, "line": a.Line, "end_line": b.Line, "hash": m.LocHash(l)}
	if n := len(m.Copies(l.ID)); n > 1 {
		// A duplicate, which check reports; an edit picks a copy by its hash.
		r["id_copies"] = n
	}
	if doc := docComment(m.Files[l.Span.File].Src, l.Span.Off); doc != "" {
		r["doc"] = doc
	}
	switch n := l.Node.(type) {
	case *ir.TypeDecl:
		// What to pass ovid/io.Alloc for one of these.
		r["size"] = check.SizeOf(n.Fields)
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
		if n.Table {
			return fmt.Sprintf("const %s %s", n.Name, n.Type)
		}
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

// Show prints the source of each node. As text that is a header line with
// the node's id and hash, then the source; withIDs adds `// @id` to each
// line where a statement starts, and exprs lists the expressions inside a
// decl with their ids (they are always listed for a statement or
// expression). asJSON prints one record per node instead.
func Show(dir string, ids []string, withIDs, asJSON, exprs bool, w io.Writer) int {
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	var types map[string]string
	if len(m.Errors) == 0 {
		types = check.Run(m.Prog).Types
	}
	// typeOf is l's checked type. Types is keyed by id, and of the copies
	// of a duplicated func the checker types only the first, the one the
	// id indexes, so another copy's nodes get none rather than its types.
	typeOf := func(l *module.Loc) string {
		if m.Index()[l.ID] != l {
			return ""
		}
		return types[l.ID]
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
		// A decl's text starts at its doc comment: what replace swaps.
		start := lineStart(f.Src, l.Full.Off)
		text := string(f.Src[start:l.Full.End])
		docAt := f.Pos(l.Full.Off)
		if asJSON {
			r := map[string]any{"id": l.ID, "kind": l.Kind, "file": file, "line": a.Line, "end_line": b.Line,
				"hash": m.LocHash(l), "decl": l.Decl, "parent": l.Parent, "text": m.Text(l.Full)}
			if l.Full.Off < l.Span.Off {
				r["doc_line"] = docAt.Line
				r["doc"] = docComment(f.Src, l.Span.Off)
			}
			if t := typeOf(l); t != "" {
				r["type"] = t
			}
			if s := sigOf(l); s != "" {
				r["sig"] = s
			}
			if exprs || l.Kind == "stmt" || l.Kind == "expr" {
				es := []map[string]any{}
				for _, e := range exprsIn(m, l) {
					_, ea, _, _ := m.Where(e.Span)
					x := map[string]any{"id": e.ID, "line": ea.Line, "col": ea.Col, "text": m.Text(e.Span), "hash": m.LocHash(e)}
					if t := typeOf(e); t != "" {
						x["type"] = t
					}
					es = append(es, x)
				}
				r["exprs"] = es
			}
			emit(w, r)
			continue
		}
		// The line range is the printed text's, doc comment included.
		hdr := fmt.Sprintf("// %s %s %s:%d-%d hash=%s", l.Kind, l.ID, file, docAt.Line, b.Line, m.LocHash(l))
		if l.Decl != "" && l.Decl != l.ID {
			hdr += " in=" + l.Decl
		}
		if t := typeOf(l); t != "" {
			hdr += " type=" + t
		}
		fmt.Fprintln(w, hdr)
		if withIDs {
			text = annotate(m, l, start, text)
		}
		fmt.Fprintln(w, text)
		if exprs || l.Kind == "stmt" || l.Kind == "expr" {
			for _, e := range exprsIn(m, l) {
				_, ea, _, _ := m.Where(e.Span)
				line := fmt.Sprintf("//   %s %d:%d %s  hash=%s", e.ID, ea.Line, ea.Col, oneLine(m.Text(e.Span), 72), m.LocHash(e))
				if t := typeOf(e); t != "" {
					line += " type=" + t
				}
				fmt.Fprintln(w, line)
			}
		}
	}
	if asJSON {
		emit(w, map[string]any{"ok": true, "count": len(locs)})
	}
	return ExitOK
}

// exprsIn lists the expressions inside l in source order, outer before
// inner, so each can be replaced by id.
func exprsIn(m *module.Module, l *module.Loc) []*module.Loc {
	var out []*module.Loc
	visit := func(c *ir.Node) {
		if e := m.LocOf(c); e != nil && e.Kind == "expr" {
			out = append(out, e)
		}
	}
	switch n := l.Node.(type) {
	case *ir.Func:
		for _, st := range n.Body {
			st.Walk(visit)
		}
	case *ir.Node:
		n.Walk(visit)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Span.Off != out[j].Span.Off {
			return out[i].Span.Off < out[j].Span.Off
		}
		return out[i].Span.End > out[j].Span.End
	})
	return out
}

// oneLine is s on one line, cut to about n bytes at a character boundary.
// Only line breaks and the indentation around them become one space; a
// string literal cannot span lines, so its spacing is kept.
func oneLine(s string, n int) string {
	ls := strings.Split(s, "\n")
	for i, l := range ls {
		ls[i] = strings.Trim(l, " \t\r")
	}
	s = strings.Join(ls, " ")
	if len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "…"
	}
	return s
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

// Ref is one use of a declaration. span is the node that holds it, which
// refs reports; tok is the name token itself, which rename rewrites.
type ref struct {
	id   string
	kind string // call, field, setfield, name, assign, type, result
	span ir.Span
	tok  ir.Span
	decl string
}

// Refs prints every use of the named declaration: as text, grouped under
// the decl each use is in, one line per use with its line number and
// source; as JSON (asJSON), one record per use.
func Refs(dir, q, name string, asJSON bool, page Page, w io.Writer) int {
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
	if sameID(locs) {
		return failAmbiguousID(w, m, locs)
	}
	if len(locs) > 1 {
		var ids []string
		for _, l := range locs {
			ids = append(ids, l.ID)
		}
		return fail(w, "ambiguous", q+" names several declarations", "use a full id: "+strings.Join(ids, ", "))
	}
	target := locs[0]
	name, err = pickName(target, name)
	if err != nil {
		return fail(w, "not_found", err.Error(), "")
	}
	res := check.Run(m.Prog)
	rs, err := findRefs(m, res, target, name)
	if err != nil {
		return fail(w, "unsupported", err.Error(), "")
	}
	pg := pager{Page: page}
	var grp groups
	for _, r := range rs {
		if !pg.take() {
			continue
		}
		file, a, _, line := m.Where(r.span)
		if !asJSON {
			grp.line(w, file, r.decl, a.Line, strings.TrimSpace(line))
			continue
		}
		emit(w, map[string]any{"id": r.id, "kind": r.kind, "in": r.decl, "file": file, "line": a.Line, "col": a.Col, "source": strings.TrimSpace(line)})
	}
	byPkg := usesByPkg(m, rs)
	files := map[int]bool{}
	external := 0
	for _, r := range rs {
		files[r.span.File] = true
	}
	for p, n := range byPkg {
		if p != target.Pkg {
			external += n
		}
	}
	// files, by_pkg, and external describe every use, on the page or not.
	last := map[string]any{"ok": true, "target": target.ID, "files": len(files),
		"by_pkg": byPkg, "external": external, "revision": m.Revision()}
	pg.finish(last)
	emit(w, last)
	return ExitOK
}

// groups prints lines of source grouped under the file and decl they are
// in: a heading "file  decl" when either changes, then "line: source".
type groups struct{ file, decl string }

func (g *groups) line(w io.Writer, file, decl string, line int, source string) {
	if file != g.file || decl != g.decl {
		g.file, g.decl = file, decl
		h := file
		if decl != "" {
			h += "  " + decl
		}
		fmt.Fprintln(w, h)
	}
	fmt.Fprintf(w, "%4d: %s\n", line, source)
}

// usesByPkg counts refs by the package they appear in.
func usesByPkg(m *module.Module, rs []ref) map[string]int {
	out := map[string]int{}
	for _, r := range rs {
		out[filePkg(m, r.span.File)]++
	}
	return out
}

// findRefs lists the uses of target that the checker resolved to it, in
// source order. A name the checker resolved elsewhere (a field spelled like
// a type, a local shadowing a const) is not a use, whatever its spelling.
func findRefs(m *module.Module, res *check.Result, target *module.Loc, name string) ([]ref, error) {
	two := false // a var2 statement: name picks which of its two locals
	switch target.Kind {
	case "func", "type", "field", "const", "param":
	case "stmt":
		if n := target.Node.(*ir.Node); n.Op == "var2" {
			two = true
			if ls := var2Locals(n); name == "" && len(ls) == 1 {
				name = ls[0]
			} else if name == "" {
				return nil, fmt.Errorf("%s declares two locals, %s and %s; say which with --name %s or --name %s", target.ID, n.Name, n.Two.Name, n.Name, n.Two.Name)
			}
		} else if n.Op != "var" {
			return nil, fmt.Errorf("refs works on funcs, types, fields, consts, params, and var statements; %s is a %s statement", target.ID, n.Op)
		}
	default:
		return nil, fmt.Errorf("refs works on funcs, types, fields, consts, params, and var statements, not %s", target.Kind)
	}
	var out []ref
	for _, u := range res.Uses {
		if u.Target != target.ID {
			continue
		}
		// The two locals of a var2 share its id: the token says which.
		if two && string(m.Files[u.Span.File].Src[u.Span.Off:u.Span.End]) != name {
			continue
		}
		r := ref{id: u.ID, kind: u.Kind, span: u.Span, tok: u.Span, decl: u.In}
		if u.Kind == "result" {
			if l := m.Index()[u.ID]; l != nil {
				r.span = headerSpan(m, l.Node.(*ir.Func))
			}
		} else if l := m.Index()[u.ID]; l != nil {
			r.span = l.Span
			if _, ok := l.Node.(*ir.Const); ok {
				// A const named in a const's value has no node of its own,
				// and a table's elements may run over lines: the name's.
				r.span = u.Span
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].tok, out[j].tok
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Off < b.Off
	})
	return out, nil
}

// pickName is the name refs or rename works on in t: name, which must be
// one t declares, or t's own when name is empty. A var2 statement
// declares its locals under one id, and name says which; with two and no
// name it is "" (findRefs refuses that). _ is a discard, not a local.
func pickName(t *module.Loc, name string) (string, error) {
	if n, ok := t.Node.(*ir.Node); ok && n.Op == "var2" {
		ls := var2Locals(n)
		if name == "" && len(ls) == 1 {
			return ls[0], nil
		}
		for _, l := range ls {
			if name == "" || name == l {
				return name, nil
			}
		}
		if len(ls) == 0 {
			return "", fmt.Errorf("%s declares no local, only discards", t.ID)
		}
		return "", fmt.Errorf("%s declares %s, not %s", t.ID, strings.Join(ls, " and "), name)
	}
	own := nameOf(t)
	if name == "" || own == "" || name == own {
		return own, nil
	}
	return "", fmt.Errorf("%s declares %s, not %s", t.ID, own, name)
}

// var2Locals are the locals a var2 statement declares: its two names but
// _, which discards a result.
func var2Locals(n *ir.Node) []string {
	var ls []string
	for _, nm := range []string{n.Name, n.Two.Name} {
		if nm != "_" {
			ls = append(ls, nm)
		}
	}
	return ls
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
			if (n.Op == "var" || n.Op == "var2") && n.Name == name {
				found = true
			}
			if n.Op == "var2" && n.Two.Name == name {
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
