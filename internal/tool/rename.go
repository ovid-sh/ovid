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
	"as": true, "syscall": true, "load8": true, "load32": true, "load64": true, "store8": true,
	"store64": true, "strptr": true, "strlen": true, "i64": true, "bool": true,
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

// tokens returns offsets of whole-word occurrences of name in src[lo:hi],
// skipping string literals and comments.
func tokens(src []byte, lo, hi int, name string) []int {
	var out []int
	for i := lo; i < hi; i++ {
		c := src[i]
		if c == '"' {
			for i++; i < hi && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
			continue
		}
		if c == '/' && i+1 < hi && src[i+1] == '/' {
			for i < hi && src[i] != '\n' {
				i++
			}
			continue
		}
		if !identByte(c) || (i > 0 && identByte(src[i-1])) {
			continue
		}
		j := i
		for j < len(src) && identByte(src[j]) {
			j++
		}
		if string(src[i:j]) == name && j <= hi {
			out = append(out, i)
		}
		i = j - 1
	}
	return out
}

func prevByte(src []byte, off int) byte {
	for off > 0 {
		off--
		if src[off] != ' ' && src[off] != '\t' {
			return src[off]
		}
	}
	return 0
}

func nextByte(src []byte, off int) byte {
	for ; off < len(src); off++ {
		if src[off] != ' ' && src[off] != '\t' {
			return src[off]
		}
	}
	return 0
}

// Rename renames a declaration and every use of it, token by token. It
// refuses names that collide and changes that add check errors.
func Rename(dir, q, to string, dryRun bool, w io.Writer) int {
	unlock, err := lockModule(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
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
	if t.Kind == "stmt" {
		if n := t.Node.(*ir.Node); n.Op != "var" {
			return fail(w, "unsupported", "rename works on funcs, types, fields, consts, params, and var statements; "+t.ID+" is a "+n.Op+" statement", "")
		}
	}
	old := nameOf(t)
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
	res := check.Run(m.Prog)
	rs, err := findRefs(m, res, t)
	if err != nil {
		return fail(w, "unsupported", err.Error(), "")
	}
	type at struct{ file, off int }
	seen := map[at]bool{}
	var sps []*splice
	add := func(file int, offs []int) {
		for _, off := range offs {
			k := at{file, off}
			if seen[k] {
				continue
			}
			seen[k] = true
			sps = append(sps, &splice{abs: m.Files[file].Abs, off: off, end: off + len(old), text: to})
		}
	}
	// The declaration's own name token.
	add(t.Span.File, declToken(m, t, old))
	for _, r := range rs {
		add(r.span.File, refToken(m, r, old))
	}
	for _, s := range sps {
		if s.abs == "" {
			return fail(w, "std", "a use sits in a shipped package", "")
		}
	}
	return applySplices(w, dir, m, sps, nil, dryRun, guardNoWorse, false,
		map[string]any{"from": t.ID, "to": to, "edits": len(sps), "refs": len(rs), "id": renamedID(t, old, to)})
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
		ty := m.Index[t.Decl].Node.(*ir.TypeDecl)
		for _, f := range ty.Fields {
			if f.Name == to {
				return f.ID
			}
		}
	case "param", "stmt":
		fn := m.Index[t.Decl].Node.(*ir.Func)
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

// declToken finds the name token in a declaration's own source.
func declToken(m *module.Module, t *module.Loc, old string) []int {
	src := m.Files[t.Span.File].Src
	offs := tokens(src, t.Span.Off, t.Span.End, old)
	if len(offs) == 0 {
		return nil
	}
	switch t.Kind {
	case "func", "type", "const", "stmt":
		// The first token after the keyword.
		return offs[:1]
	}
	// Fields and params start with their name.
	if offs[0] == t.Span.Off {
		return offs[:1]
	}
	return nil
}

// refToken finds the token a reference spells the name with.
func refToken(m *module.Module, r ref, old string) []int {
	src := m.Files[r.span.File].Src
	offs := tokens(src, r.span.Off, r.span.End, old)
	if len(offs) == 0 {
		return nil
	}
	n, _ := m.Index[r.id].Node.(*ir.Node)
	switch r.kind {
	case "call":
		for _, o := range offs {
			if nextByte(src, o+len(old)) == '(' {
				return []int{o}
			}
		}
	case "field", "setfield":
		lo := n.Base.Span.End
		for _, o := range offs {
			if o >= lo && prevByte(src, o) == '.' {
				return []int{o}
			}
		}
	case "name", "assign":
		if n.Pkg != "" {
			// path.Name: the name is the last token.
			return offs[len(offs)-1:]
		}
		return offs[:1]
	case "type", "result":
		var out []int
		for _, o := range offs {
			if p := prevByte(src, o); p == '*' || p == '.' {
				out = append(out, o)
				continue
			}
			// An unstarred local type: `x T`. The first token of a param or
			// field is its own name, so skip it.
			if o != r.span.Off && !(n != nil && n.Op == "var" && o == offs[0] && n.Name == old) {
				if nb := nextByte(src, o+len(old)); nb != '.' && nb != '(' {
					out = append(out, o)
				}
			}
		}
		return out
	}
	return nil
}
