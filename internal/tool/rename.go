package tool

import (
	"fmt"
	"io"
	"strings"

	"ovid/internal/check"
	"ovid/internal/ir"
	"ovid/internal/module"
)

var keywords = map[string]bool{
	"package": true, "import": true, "const": true, "type": true, "struct": true, "func": true,
	"var": true, "if": true, "else": true, "while": true, "return": true, "true": true, "false": true,
	"as": true, "syscall": true, "load8": true, "load16": true, "load32": true, "load64": true, "store8": true, "store16": true, "store32": true,
	"store64": true, "bswap16": true, "bswap32": true, "bswap64": true, "ushr": true, "umulhi": true, "ult": true, "udiv": true, "urem": true, "strptr": true, "strlen": true, "i64": true, "bool": true,
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

func identByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// Rename renames a declaration and every use the checker resolved to it,
// rewriting those name tokens and, when the declaration's doc comment opens
// with its name, that word too: a field, local, or other declaration
// spelled the same, or any other mention in a comment or string, is left
// alone. It refuses names that collide and changes that add check errors.
func Rename(dir, q, to, name string, dryRun bool, w io.Writer) int {
	unlock, code := lockModule(dir, w)
	if unlock == nil {
		return code
	}
	defer unlock()
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	if len(m.Errors) > 0 {
		(&checked{m: m, diags: m.Errors}).writeDiags(w)
		return fail(w, "syntax", "fix syntax errors first; rename needs a parsed module", "")
	}
	if !isIdent(to) || keywords[to] {
		return fail(w, "bad_name", fmt.Sprintf("%q is not a usable identifier", to), "")
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
	t := locs[0]
	if m.IsStd(t.Span.File) {
		return fail(w, "std", t.ID+" is in a shipped package", "")
	}
	var two *ir.Node // a var2 statement: name picks which of its locals
	if t.Kind == "stmt" {
		if n := t.Node.(*ir.Node); n.Op == "var2" {
			two = n
		} else if n.Op != "var" {
			return fail(w, "unsupported", "rename works on funcs, types, fields, consts, params, and var statements; "+t.ID+" is a "+n.Op+" statement", "")
		}
	}
	old, err := pickName(t, name)
	if err != nil {
		return fail(w, "not_found", err.Error(), "")
	}
	if two != nil && old == "" {
		return fail(w, "unsupported", t.ID+" declares two locals, "+two.Name+" and "+two.Two.Name, "say which with --name "+two.Name+" or --name "+two.Two.Name)
	}
	if old == "" {
		return fail(w, "unsupported", "rename works on funcs, types, fields, consts, params, and var statements, not "+t.Kind, "")
	}
	if old == to {
		return fail(w, "bad_name", t.ID+" is already named "+to, "")
	}
	if c := collision(m, t, to); c != "" {
		return fail(w, "conflict", "renaming to "+to+" collides with "+c, "pick another name")
	}
	if t.Kind == "func" && t.Pkg == m.Entry && old == "main" {
		return fail(w, "bad_name", "main is the entry point and cannot be renamed", "")
	}
	if servedHandle(m, t) {
		return fail(w, "bad_name", "handle is the entry point (the package has no main) and cannot be renamed", "")
	}
	res := check.Run(m.Prog)
	rs, err := findRefs(m, res, t, name)
	if err != nil {
		return fail(w, "unsupported", err.Error(), "")
	}
	type at struct{ file, off int }
	seen := map[at]bool{}
	var sps []*splice
	add := func(sp ir.Span) {
		k := at{sp.File, sp.Off}
		if seen[k] || sp.End > len(m.Files[sp.File].Src) || string(m.Files[sp.File].Src[sp.Off:sp.End]) != old {
			return
		}
		seen[k] = true
		sps = append(sps, &splice{abs: m.Files[sp.File].Abs, off: sp.Off, end: sp.End, text: to})
	}
	// The declaration's own name token (of a var2, the one name picks).
	if two != nil && old == two.Two.Name {
		add(two.Two.NameSpan)
	} else {
		add(declSpan(t))
	}
	for _, r := range rs {
		add(r.tok)
	}
	// The first word of the decl's doc comment, when that is the name:
	// "// Name does ..." is the convention, and a renamed decl whose
	// comment still opens with the old name reads as a mistake.
	doc := false
	if sp, ok := docName(m, t, old); ok {
		add(sp)
		doc = true
	}
	for _, s := range sps {
		if s.abs == "" {
			return fail(w, "std", "a use sits in a shipped package", "")
		}
	}
	return applySplices(w, dir, m, sps, nil, dryRun, guardNoWorse, false,
		map[string]any{"from": t.ID, "to": to, "edits": len(sps), "refs": len(rs), "id": renamedID(t, old, to), "doc": doc})
}

// docName is the span of the first word of l's doc comment, if l has one
// and that word is name: the "// Name ..." convention.
func docName(m *module.Module, l *module.Loc, name string) (ir.Span, bool) {
	if l.Full.Off >= l.Span.Off {
		return ir.Span{}, false
	}
	src := m.Files[l.Span.File].Src
	i := l.Full.Off
	if i+2 > len(src) || src[i] != '/' || src[i+1] != '/' {
		return ir.Span{}, false
	}
	i += 2
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	end := i + len(name)
	if end > len(src) || string(src[i:end]) != name || (end < len(src) && identByte(src[end])) {
		return ir.Span{}, false
	}
	return ir.Span{File: l.Span.File, Off: i, End: end}, true
}

func nameOf(l *module.Loc) string {
	switch n := l.Node.(type) {
	case *ir.Func:
		return n.Name
	case *ir.TypeDecl:
		return n.Name
	case *ir.Const:
		return n.Name
	case *ir.Field:
		return n.Name
	case *ir.Param:
		return n.Name
	case *ir.Node:
		if n.Op == "var" {
			return n.Name
		}
	}
	return ""
}

// declSpan is the name token of a declaration.
func declSpan(l *module.Loc) ir.Span {
	switch n := l.Node.(type) {
	case *ir.Func:
		return n.NameSpan
	case *ir.TypeDecl:
		return n.NameSpan
	case *ir.Const:
		return n.NameSpan
	case *ir.Field:
		return n.NameSpan
	case *ir.Param:
		return n.NameSpan
	case *ir.Node:
		return n.NameSpan
	}
	return ir.Span{}
}

func renamedID(l *module.Loc, old, to string) string {
	switch l.Kind {
	case "func", "type", "const", "field", "param":
		if strings.HasSuffix(l.ID, "."+old) {
			return strings.TrimSuffix(l.ID, old) + to
		}
	}
	return l.ID
}

// collision names what a new name would clash with, or "".
func collision(m *module.Module, t *module.Loc, to string) string {
	pkg := findPkg(m, t.Pkg)
	switch t.Kind {
	case "func", "type", "const":
		for _, f := range pkg.Funcs {
			if f.Name == to && t.Kind != "type" {
				return f.ID
			}
		}
		for _, c := range pkg.Consts {
			if c.Name == to && t.Kind != "type" {
				return c.ID
			}
		}
		for _, ty := range pkg.Types {
			if ty.Name == to && t.Kind == "type" {
				return ty.ID
			}
		}
		if t.Kind == "const" {
			// A local of the same name would shadow the const at its uses.
			for fi := range pkg.Funcs {
				if localNamed(&pkg.Funcs[fi], to) {
					return "a local in " + pkg.Funcs[fi].ID
				}
			}
		}
	case "field":
		ty := m.Index()[t.Decl].Node.(*ir.TypeDecl)
		for _, f := range ty.Fields {
			if f.Name == to {
				return f.ID
			}
		}
	case "param", "stmt":
		fn := m.Index()[t.Decl].Node.(*ir.Func)
		if localNamed(fn, to) {
			return "a param or local in " + fn.ID
		}
		for _, c := range pkg.Consts {
			if c.Name == to {
				return c.ID + " (the local would shadow it)"
			}
		}
	}
	return ""
}
