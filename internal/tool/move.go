package tool

import (
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"ovid/internal/check"
	"ovid/internal/ir"
	"ovid/internal/module"
	"ovid/internal/syntax"
)

// Move moves a func, type, or const to another package (created if needed)
// and rewrites every use: qualifiers are added, changed, or dropped, and
// files gain the imports they now need. Like rename, it refuses a change
// that adds check errors.
func Move(dir, q, to, file string, dryRun bool, w io.Writer) int {
	unlock, code := lockModule(dir, w)
	if unlock == nil {
		return code
	}
	defer unlock()
	return move(dir, q, to, file, dryRun, w)
}

// move is Move with the module lock already held.
func move(dir, q, to, file string, dryRun bool, w io.Writer) int {
	p, code := planMove(dir, q, to, file, nil, w)
	if p == nil {
		return code
	}
	if !dryRun {
		if code := commitFiles(w, p.m, p.loaded, p.overlay); code != ExitOK {
			return code
		}
	}
	return p.emit(w, dryRun)
}

// planMove plans the move of q to package to in memory, over the files in
// base that earlier moves changed, and checks it. On failure it writes why
// and returns a nil plan.
func planMove(dir, q, to, file string, base map[string][]byte, w io.Writer) (*planned, int) {
	if dir == "" {
		dir = "."
	}
	m, err := module.LoadOverlay(dir, base)
	if err != nil {
		return nil, fail(w, "load", err.Error(), "")
	}
	if len(m.Errors) > 0 {
		(&checked{m: m, diags: m.Errors}).writeDiags(w)
		return nil, fail(w, "syntax", "fix syntax errors first; move needs a parsed module", "")
	}
	locs, err := m.Lookup(q)
	if err != nil {
		return nil, fail(w, "not_found", err.Error(), "")
	}
	if sameID(locs) {
		return nil, failAmbiguousID(w, m, locs)
	}
	if len(locs) > 1 {
		var ids []string
		for _, l := range locs {
			ids = append(ids, l.ID)
		}
		return nil, fail(w, "ambiguous", q+" names several declarations", "use a full id: "+strings.Join(ids, ", "))
	}
	t := locs[0]
	if t.Kind != "func" && t.Kind != "type" && t.Kind != "const" {
		return nil, fail(w, "unsupported", "move works on funcs, types, and consts; "+t.ID+" is a "+t.Kind, "")
	}
	to = strings.TrimPrefix(to, "pkg:")
	from := t.Pkg
	name := nameOf(t)
	switch {
	case m.IsStd(t.Span.File) || m.Std[to]:
		return nil, fail(w, "std", "shipped packages cannot be changed", "copy the package into the module first")
	case to == from:
		return nil, fail(w, "bad_move", t.ID+" is already in "+to, "")
	case !isIdent(strings.ReplaceAll(to, "/", "_")):
		return nil, fail(w, "bad_name", fmt.Sprintf("%q is not a package path", to), "")
	case t.Kind == "func" && from == m.Entry && name == "main":
		return nil, fail(w, "bad_move", "main must stay in the entry package", "")
	}
	dest := findPkg(m, to)
	if dest != nil {
		fake := &module.Loc{ID: t.ID, Kind: t.Kind, Pkg: to}
		if c := collision(m, fake, name); c != "" {
			return nil, fail(w, "conflict", to+" already has "+c, "rename one of them first")
		}
	}

	res := check.Run(m.Prog)
	src := m.Files[t.Span.File].Src
	lo := t.Full.Off
	inMoved := func(fi, off int) bool { return fi == t.Span.File && off >= lo && off < t.Span.End }

	// Each use is a qualifier change at a name token: the qualifier before
	// it (if any) is replaced by want ("" drops it).
	type qual struct {
		file, off, end int
		text           string
	}
	var inner, outer []qual
	seen := map[[2]int]bool{}
	need := map[int]map[string]bool{} // file -> imports it must have
	needImport := func(fi int, p string) {
		if need[fi] == nil {
			need[fi] = map[string]bool{}
		}
		need[fi][p] = true
	}
	movedNeedsFrom := false
	requalify := func(r ref, target string) {
		fi, o := r.tok.File, r.tok.Off
		fsrc := m.Files[fi].Src
		k := [2]int{fi, o}
		if seen[k] {
			return
		}
		seen[k] = true
		moved := inMoved(fi, o)
		here := filePkg(m, fi)
		if moved {
			here = to
		}
		want := ""
		if here != target {
			want = target + "."
		}
		qs := o
		if o > 0 && fsrc[o-1] == '.' {
			qs = o - 1
			for qs > 0 && (identByte(fsrc[qs-1]) || fsrc[qs-1] == '/') {
				qs--
			}
		}
		if string(fsrc[qs:o]) == want {
			return
		}
		qq := qual{fi, qs, o, want}
		if moved {
			inner = append(inner, qq)
			if target == from {
				movedNeedsFrom = true
			}
		} else {
			outer = append(outer, qq)
			if want != "" {
				needImport(fi, target)
			}
		}
	}

	// Uses of the moved decl now point at its new package.
	rs, err := findRefs(m, res, t)
	if err != nil {
		return nil, fail(w, "unsupported", err.Error(), "")
	}
	for _, r := range rs {
		if m.IsStd(r.span.File) {
			continue
		}
		requalify(r, to)
	}
	// Inside the moved text, uses of its old and new neighbours change too.
	for _, pk := range []string{from, to} {
		for _, id := range m.Order {
			l := m.Index[id]
			if l.Pkg != pk || l.ID == t.ID || (l.Kind != "func" && l.Kind != "type" && l.Kind != "const") {
				continue
			}
			lrs, err := findRefs(m, res, l)
			if err != nil {
				continue
			}
			for _, r := range lrs {
				if inMoved(r.span.File, r.span.Off) {
					requalify(r, pk)
				}
			}
		}
	}

	// The moved text with its qualifiers rewritten.
	sort.Slice(inner, func(i, j int) bool { return inner[i].off < inner[j].off })
	var b strings.Builder
	prev := lo
	for _, qq := range inner {
		b.Write(src[prev:qq.off])
		b.WriteString(qq.text)
		prev = qq.end
	}
	b.Write(src[prev:t.Span.End])
	moved := b.String()

	// Packages the moved text names, which its new file must import.
	movedImports := map[string]bool{}
	for _, p := range pkgsUsed(t) {
		if p != from && p != to {
			movedImports[p] = true
		}
	}
	if movedNeedsFrom {
		movedImports[from] = true
	}

	var sps []*splice
	// Cut the decl, its doc comment, and one blank line after it.
	end := lineAfter(src, t.Span.End)
	if end < len(src) && src[end] == '\n' {
		end++
	}
	sps = append(sps, &splice{abs: m.Files[t.Span.File].Abs, off: lineBegin(src, lo), end: end})
	for _, qq := range outer {
		sps = append(sps, &splice{abs: m.Files[qq.file].Abs, off: qq.off, end: qq.end, text: qq.text})
	}

	// Where the decl lands: --file, else the package's first file, else a
	// new package.
	destFile := -1
	var destAbs string
	if file != "" {
		if filepath.ToSlash(filepath.Dir(file)) != to {
			return nil, fail(w, "bad_move", file+" is not in package directory "+to, "")
		}
		destAbs = filepath.Join(m.Root, filepath.FromSlash(file))
		for i, f := range m.Files {
			if f.Abs == destAbs {
				destFile = i
			}
		}
	} else if dest != nil {
		destFile = dest.Span.File
		destAbs = m.Files[destFile].Abs
	} else {
		destAbs = filepath.Join(m.Root, filepath.FromSlash(to), path.Base(to)+".ov")
	}
	if destFile >= 0 {
		for p := range movedImports {
			needImport(destFile, p)
		}
		ds := m.Files[destFile].Src
		sps = append(sps, &splice{abs: destAbs, off: len(ds), end: len(ds), text: sepFor(ds) + moved + "\n"})
	} else {
		var hdr strings.Builder
		hdr.WriteString("package " + to + "\n\n")
		var ims []string
		for p := range movedImports {
			ims = append(ims, p)
		}
		sort.Strings(ims)
		for _, p := range ims {
			hdr.WriteString("import " + p + "\n")
		}
		if len(ims) > 0 {
			hdr.WriteString("\n")
		}
		sps = append(sps, &splice{abs: destAbs, isNew: true, text: hdr.String() + moved + "\n"})
	}

	// Add missing imports at the end of each file's import block.
	for fi, ps := range need {
		f := m.Files[fi]
		pf, perr := syntax.ParseFile(fi, f.Src)
		if perr != nil {
			continue
		}
		var add []string
		for p := range ps {
			has := p == pf.Path
			for _, im := range pf.Imports {
				has = has || im.Path == p
			}
			if !has {
				add = append(add, p)
			}
		}
		if len(add) == 0 {
			continue
		}
		sort.Strings(add)
		at := pf.Span.End
		text := "\n"
		if len(pf.Imports) > 0 {
			at = pf.Imports[len(pf.Imports)-1].Span.End
		} else {
			text = "\n\n"
		}
		for i, p := range add {
			if i > 0 {
				text += "\n"
			}
			text += "import " + p
		}
		sps = append(sps, &splice{abs: f.Abs, off: at, end: at, text: text})
	}

	newID := t.ID[:strings.Index(t.ID, ":")+1] + to + "." + name
	return planSplices(w, dir, m, base, sps, nil, guardNoWorse, false,
		map[string]any{"from": t.ID, "to": newID, "file": rel(m, destAbs), "refs": len(rs), "edits": len(sps)})
}

// filePkg is the package a module file belongs to.
func filePkg(m *module.Module, fi int) string {
	for _, l := range m.Locs() {
		if l.Span.File == fi {
			return l.Pkg
		}
	}
	return ""
}

// pkgsUsed lists the packages a decl's types, calls, and consts name.
func pkgsUsed(l *module.Loc) []string {
	set := map[string]bool{}
	typ := func(t string) {
		t = strings.TrimPrefix(t, "*")
		if i := strings.LastIndex(t, "."); i > 0 {
			set[t[:i]] = true
		}
	}
	var walk func(n *ir.Node)
	walk = func(n *ir.Node) {
		if n == nil {
			return
		}
		if n.Op == "call" || n.Op == "name" {
			p := n.Pkg
			if p == "" && n.Op == "call" {
				p = l.Pkg
			}
			if p != "" {
				set[p] = true
			}
		}
		if n.Op == "var" || n.Op == "cast" || n.Op == "sizeof" {
			typ(n.Type)
		}
		for _, c := range n.Children() {
			walk(c)
		}
	}
	switch d := l.Node.(type) {
	case *ir.Func:
		for _, p := range d.Params {
			typ(p.Type)
		}
		typ(d.Result)
		for _, s := range d.Body {
			walk(s)
		}
	case *ir.TypeDecl:
		for _, f := range d.Fields {
			typ(f.Type)
		}
	}
	var out []string
	for p := range set {
		out = append(out, p)
	}
	return out
}

// MoveMany moves several decls to one package in order, all or none.
// Each move is planned in memory over the ones before it, so later moves
// see earlier ones, and checked; only when every one has passed are the
// files they changed written, together, by one module.WriteFiles. A dry
// run writes nothing at all.
func MoveMany(dir string, qs []string, to, file string, dryRun bool, w io.Writer) int {
	unlock, code := lockModule(dir, w)
	if unlock == nil {
		return code
	}
	defer unlock()
	if len(qs) == 1 {
		return move(dir, qs[0], to, file, dryRun, w)
	}
	base := map[string][]byte{}   // each changed file's source after the moves so far
	loaded := map[string][]byte{} // and as it is on disk; a file the moves create has none
	var plans []*planned
	var moved []string
	for _, q := range qs {
		p, code := planMove(dir, q, to, file, base, w)
		if p == nil {
			emit(w, map[string]any{"ok": false, "error": "rolled_back", "message": "move of " + q + " failed; no file was changed",
				"moved_before_failure": moved})
			return code
		}
		for abs, src := range p.overlay {
			if _, again := base[abs]; !again {
				// No earlier move changed it, so p read it from disk.
				if was, ok := p.loaded[abs]; ok {
					loaded[abs] = was
				}
			}
			base[abs] = src
		}
		plans = append(plans, p)
		moved = append(moved, q)
	}
	if !dryRun {
		if code := commitFiles(w, plans[0].m, loaded, base); code != ExitOK {
			return code
		}
	}
	// Each move's receipt, then the whole one's.
	for _, p := range plans {
		p.emit(w, dryRun)
	}
	emit(w, map[string]any{"ok": true, "moved": moved, "to": to, "written": !dryRun})
	return ExitOK
}
