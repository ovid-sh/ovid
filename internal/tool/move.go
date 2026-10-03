package tool

import (
	"bytes"
	"fmt"
	"io"
	"os"
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
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	if len(m.Errors) > 0 {
		(&checked{m: m, diags: m.Errors}).writeDiags(w)
		return fail(w, "syntax", "fix syntax errors first; move needs a parsed module", "")
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
	if t.Kind != "func" && t.Kind != "type" && t.Kind != "const" {
		return fail(w, "unsupported", "move works on funcs, types, and consts; "+t.ID+" is a "+t.Kind, "")
	}
	to = strings.TrimPrefix(to, "pkg:")
	from := t.Pkg
	name := nameOf(t)
	switch {
	case m.IsStd(t.Span.File) || m.Std[to]:
		return fail(w, "std", "shipped packages cannot be changed", "copy the package into the module first")
	case to == from:
		return fail(w, "bad_move", t.ID+" is already in "+to, "")
	case !isIdent(strings.ReplaceAll(to, "/", "_")):
		return fail(w, "bad_name", fmt.Sprintf("%q is not a package path", to), "")
	case t.Kind == "func" && from == m.Entry && name == "main":
		return fail(w, "bad_move", "main must stay in the entry package", "")
	}
	dest := findPkg(m, to)
	if dest != nil {
		fake := &module.Loc{ID: t.ID, Kind: t.Kind, Pkg: to}
		if c := collision(m, fake, name); c != "" {
			return fail(w, "conflict", to+" already has "+c, "rename one of them first")
		}
	}

	res := check.Run(m.Prog)
	src := m.Files[t.Span.File].Src
	lo := docStart(src, t.Span.Off)
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
	requalify := func(r ref, old, target string) {
		fsrc := m.Files[r.span.File].Src
		for _, o := range refToken(m, r, old) {
			k := [2]int{r.span.File, o}
			if seen[k] {
				continue
			}
			seen[k] = true
			moved := inMoved(r.span.File, o)
			here := filePkg(m, r.span.File)
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
				continue
			}
			qq := qual{r.span.File, qs, o, want}
			if moved {
				inner = append(inner, qq)
				if target == from {
					movedNeedsFrom = true
				}
			} else {
				outer = append(outer, qq)
				if want != "" {
					needImport(r.span.File, target)
				}
			}
		}
	}

	// Uses of the moved decl now point at its new package.
	rs, err := findRefs(m, res, t)
	if err != nil {
		return fail(w, "unsupported", err.Error(), "")
	}
	for _, r := range rs {
		if m.IsStd(r.span.File) {
			continue
		}
		requalify(r, name, to)
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
					requalify(r, nameOf(l), pk)
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
			return fail(w, "bad_move", file+" is not in package directory "+to, "")
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
	return applySplices(w, dir, m, sps, nil, dryRun, guardNoWorse, false,
		map[string]any{"from": t.ID, "to": newID, "file": rel(m, destAbs), "refs": len(rs), "edits": len(sps)})
}

// filePkg is the package a module file belongs to.
func filePkg(m *module.Module, fi int) string {
	for _, id := range m.Order {
		if l := m.Index[id]; l.Span.File == fi {
			return l.Pkg
		}
	}
	return ""
}

// docStart is where the // comment block directly above off begins.
func docStart(src []byte, off int) int {
	start := lineBegin(src, off)
	for start > 0 {
		pl := lineBegin(src, start-1)
		if !strings.HasPrefix(strings.TrimSpace(string(src[pl:start-1])), "//") {
			break
		}
		start = pl
	}
	if start < off && strings.TrimSpace(string(src[start:off])) == "" {
		return off
	}
	return start
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

// MoveMany moves several decls to one package in order. Either all of them
// move or the module's .ov files are put back as they were; dry-run moves
// for real and then restores, so later moves see earlier ones.
func MoveMany(dir string, qs []string, to, file string, dryRun bool, w io.Writer) int {
	if len(qs) == 1 {
		return Move(dir, qs[0], to, file, dryRun, w)
	}
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	snap, err := snapshotOv(m.Root)
	if err != nil {
		return fail(w, "read", err.Error(), "")
	}
	var moved []string
	for _, q := range qs {
		if code := Move(dir, q, to, file, false, w); code != ExitOK {
			if rerr := restoreOv(m.Root, snap); rerr != nil {
				return fail(w, "restore", rerr.Error(), "the module may be half-moved; check git status")
			}
			emit(w, map[string]any{"ok": false, "error": "rolled_back", "message": "move of " + q + " failed; no file was changed",
				"moved_before_failure": moved})
			return code
		}
		moved = append(moved, q)
	}
	if dryRun {
		if err := restoreOv(m.Root, snap); err != nil {
			return fail(w, "restore", err.Error(), "the module may be half-moved; check git status")
		}
	}
	emit(w, map[string]any{"ok": true, "moved": moved, "to": to, "written": !dryRun})
	return ExitOK
}

// snapshotOv reads every .ov file under root.
func snapshotOv(root string) (map[string][]byte, error) {
	snap := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".ov") {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			snap[p] = b
		}
		return nil
	})
	return snap, err
}

// restoreOv puts root's .ov files back to snap, removing files (and the
// directories they emptied) that were created since.
func restoreOv(root string, snap map[string][]byte) error {
	now, err := snapshotOv(root)
	if err != nil {
		return err
	}
	for p := range now {
		if _, ok := snap[p]; !ok {
			if err := os.Remove(p); err != nil {
				return err
			}
			for d := filepath.Dir(p); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
				if os.Remove(d) != nil {
					break
				}
			}
		}
	}
	for p, b := range snap {
		if !bytes.Equal(now[p], b) {
			if err := os.WriteFile(p, b, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
