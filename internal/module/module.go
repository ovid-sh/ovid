// Package module loads an Ovid module from disk: ovid.mod, one directory per
// package, any number of .ov files per package. It indexes every node by id
// and maps ids to file:line:col.
package module

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"ovid/internal/ir"
	"ovid/internal/syntax"
	"ovid/std"
)

// File is one source file. Path is relative to the module root, or
// "std:<path>" for a package the toolchain ships.
type File struct {
	Path  string
	Abs   string
	Src   []byte
	lines []int
}

// Pos is a 1-based line and column (columns count bytes).
type Pos struct{ Line, Col int }

func (f *File) Pos(off int) Pos {
	if f.lines == nil {
		f.lines = []int{0}
		for i, c := range f.Src {
			if c == '\n' {
				f.lines = append(f.lines, i+1)
			}
		}
	}
	i := sort.Search(len(f.lines), func(i int) bool { return f.lines[i] > off }) - 1
	return Pos{Line: i + 1, Col: off - f.lines[i] + 1}
}

// Line returns the text of 1-based line n.
func (f *File) Line(n int) string {
	f.Pos(0)
	if n < 1 || n > len(f.lines) {
		return ""
	}
	s := f.lines[n-1]
	e := len(f.Src)
	if n < len(f.lines) {
		e = f.lines[n] - 1
	}
	return string(f.Src[s:e])
}

// Loc is where an id lives.
type Loc struct {
	ID     string
	Kind   string // package import const type field func param stmt expr
	Pkg    string
	Decl   string // id of the enclosing top-level decl
	Parent string // id of the parent node
	Span   ir.Span
	Node   any
}

type Module struct {
	Root  string
	Name  string
	Entry string
	Files []*File
	Prog  *ir.Program
	// Std is true for packages loaded from the toolchain rather than the module.
	Std map[string]bool
	// Errors are syntax and layout problems found while loading.
	Errors []Diag
	Index  map[string]*Loc
	Order  []string
}

// Diag is one error with its location resolved.
type Diag struct {
	Fact     string `json:"fact"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	ID       string `json:"id,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	EndLine  int    `json:"end_line,omitempty"`
	EndCol   int    `json:"end_col,omitempty"`
	Expected string `json:"expected,omitempty"`
	Got      string `json:"got,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Source   string `json:"source,omitempty"`
}

// Find walks up from dir to the directory holding ovid.mod.
func Find(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, "ovid.mod")); err == nil && !st.IsDir() {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no ovid.mod in %s or any parent; create one with `ovid init`", abs)
		}
	}
}

// Load reads the module rooted at (or above) dir.
func Load(dir string) (*Module, error) { return LoadOverlay(dir, nil) }

// LoadOverlay is Load with some files replaced by in-memory contents, keyed
// by absolute path. Overlay files that do not exist on disk are added.
func LoadOverlay(dir string, overlay map[string][]byte) (*Module, error) {
	root, err := Find(dir)
	if err != nil {
		return nil, err
	}
	m := &Module{Root: root, Std: map[string]bool{}, Index: map[string]*Loc{}}
	modSrc, err := os.ReadFile(filepath.Join(root, "ovid.mod"))
	if err != nil {
		return nil, err
	}
	var stdDir string
	for _, ln := range strings.Split(string(modSrc), "\n") {
		f := strings.Fields(ln)
		if len(f) == 0 || strings.HasPrefix(f[0], "//") {
			continue
		}
		if len(f) != 2 {
			return nil, fmt.Errorf("ovid.mod: bad line %q; want `module <name>`, `entry <pkg>`, or `std <dir>`", ln)
		}
		switch f[0] {
		case "module":
			m.Name = f[1]
		case "entry":
			m.Entry = f[1]
		case "std":
			stdDir = f[1]
			if !filepath.IsAbs(stdDir) {
				stdDir = filepath.Join(root, stdDir)
			}
		default:
			return nil, fmt.Errorf("ovid.mod: unknown directive %q", f[0])
		}
	}
	m.Prog = &ir.Program{Module: m.Name, Entry: m.Entry}

	byPkg := map[string][]*File{}
	seenAbs := map[string]bool{}
	addFile := func(p string, src []byte) {
		seenAbs[p] = true
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		pkg := path.Dir(rel)
		if pkg == "." {
			pkg = ""
		}
		byPkg[pkg] = append(byPkg[pkg], &File{Path: rel, Abs: p, Src: src})
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || exists(filepath.Join(p, "ovid.mod"))) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".ov") {
			return nil
		}
		src, ok := overlay[p]
		if !ok {
			if src, err = os.ReadFile(p); err != nil {
				return err
			}
		}
		addFile(p, src)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for p, src := range overlay {
		if !seenAbs[p] && strings.HasSuffix(p, ".ov") && strings.HasPrefix(p, root+string(filepath.Separator)) {
			addFile(p, src)
		}
	}
	paths := make([]string, 0, len(byPkg))
	for k := range byPkg {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	have := map[string]bool{}
	for _, pk := range paths {
		m.addPackage(pk, byPkg[pk])
		have[pk] = true
	}

	// Pull in shipped packages that are imported but not in the module.
	for {
		var need []string
		for _, pkg := range m.Prog.Packages {
			for _, im := range pkg.Imports {
				if !have[im.Path] {
					need = append(need, im.Path)
					have[im.Path] = true
				}
			}
		}
		if len(need) == 0 {
			break
		}
		sort.Strings(need)
		for _, pk := range need {
			files := stdFiles(stdDir, pk)
			if len(files) == 0 {
				continue
			}
			m.Std[pk] = true
			m.addPackage(pk, files)
		}
	}
	sort.SliceStable(m.Prog.Packages, func(i, j int) bool { return m.Prog.Packages[i].Path < m.Prog.Packages[j].Path })
	for _, f := range m.Files {
		m.Prog.Files = append(m.Prog.Files, f.Path)
	}
	m.index()
	return m, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func stdFiles(dir, pkg string) []*File {
	var out []*File
	if dir != "" {
		ents, _ := os.ReadDir(filepath.Join(dir, filepath.FromSlash(pkg)))
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".ov") {
				p := filepath.Join(dir, filepath.FromSlash(pkg), e.Name())
				src, err := os.ReadFile(p)
				if err == nil {
					out = append(out, &File{Path: "std:" + pkg + "/" + e.Name(), Abs: p, Src: src})
				}
			}
		}
		return out
	}
	ents, _ := fs.ReadDir(std.FS, pkg)
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ov") {
			src, err := fs.ReadFile(std.FS, pkg+"/"+e.Name())
			if err == nil {
				out = append(out, &File{Path: "std:" + pkg + "/" + e.Name(), Src: src})
			}
		}
	}
	return out
}

func (m *Module) addPackage(pkgPath string, files []*File) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var merged *ir.Package
	for _, f := range files {
		idx := len(m.Files)
		m.Files = append(m.Files, f)
		if pkgPath == "" {
			m.Errors = append(m.Errors, m.diagAt(idx, 0, 0, "layout",
				"source file is in the module root; put it in a package directory such as "+strings.TrimSuffix(path.Base(f.Path), ".ov")+"/", ""))
			continue
		}
		pkg, perr := syntax.ParseFile(idx, f.Src)
		if perr != nil {
			m.Errors = append(m.Errors, m.diagAt(idx, perr.Off, perr.Off, "syntax", perr.Msg, ""))
			continue
		}
		if pkg.Path != pkgPath {
			m.Errors = append(m.Errors, m.diagAt(idx, pkg.Span.Off, pkg.Span.End, "layout",
				fmt.Sprintf("file says `package %s` but sits in directory %s", pkg.Path, pkgPath),
				"the package path is the directory path; write `package "+pkgPath+"`"))
			continue
		}
		if merged == nil {
			merged = pkg
			continue
		}
		seen := map[string]bool{}
		for _, im := range merged.Imports {
			seen[im.Path] = true
		}
		for _, im := range pkg.Imports {
			if !seen[im.Path] {
				merged.Imports = append(merged.Imports, im)
			}
		}
		merged.Consts = append(merged.Consts, pkg.Consts...)
		merged.Types = append(merged.Types, pkg.Types...)
		merged.Funcs = append(merged.Funcs, pkg.Funcs...)
	}
	if merged != nil {
		m.Prog.Packages = append(m.Prog.Packages, *merged)
	}
}

func (m *Module) diagAt(file, off, end int, code, msg, hint string) Diag {
	f := m.Files[file]
	a, b := f.Pos(off), f.Pos(end)
	return Diag{Fact: "error", Code: code, Message: msg, Hint: hint, File: m.DisplayPath(f),
		Line: a.Line, Col: a.Col, EndLine: b.Line, EndCol: b.Col, Source: f.Line(a.Line)}
}

// DisplayPath is the path an agent can open: relative to the cwd when possible.
func (m *Module) DisplayPath(f *File) string {
	if f.Abs == "" {
		return f.Path
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, f.Abs); err == nil && !strings.HasPrefix(rel, "../../") {
			return rel
		}
	}
	return f.Abs
}

func (m *Module) add(l *Loc) {
	if l.ID == "" {
		return
	}
	if _, dup := m.Index[l.ID]; dup {
		return
	}
	m.Index[l.ID] = l
	m.Order = append(m.Order, l.ID)
}

func (m *Module) index() {
	for pi := range m.Prog.Packages {
		pkg := &m.Prog.Packages[pi]
		m.add(&Loc{ID: pkg.ID, Kind: "package", Pkg: pkg.Path, Span: pkg.Span, Node: pkg})
		for i := range pkg.Imports {
			im := &pkg.Imports[i]
			m.add(&Loc{ID: im.ID, Kind: "import", Pkg: pkg.Path, Decl: im.ID, Parent: pkg.ID, Span: im.Span, Node: im})
		}
		for i := range pkg.Consts {
			c := &pkg.Consts[i]
			m.add(&Loc{ID: c.ID, Kind: "const", Pkg: pkg.Path, Decl: c.ID, Parent: pkg.ID, Span: c.Span, Node: c})
		}
		for i := range pkg.Types {
			t := &pkg.Types[i]
			m.add(&Loc{ID: t.ID, Kind: "type", Pkg: pkg.Path, Decl: t.ID, Parent: pkg.ID, Span: t.Span, Node: t})
			for j := range t.Fields {
				f := &t.Fields[j]
				m.add(&Loc{ID: f.ID, Kind: "field", Pkg: pkg.Path, Decl: t.ID, Parent: t.ID, Span: f.Span, Node: f})
			}
		}
		for i := range pkg.Funcs {
			fn := &pkg.Funcs[i]
			m.add(&Loc{ID: fn.ID, Kind: "func", Pkg: pkg.Path, Decl: fn.ID, Parent: pkg.ID, Span: fn.Span, Node: fn})
			for j := range fn.Params {
				pa := &fn.Params[j]
				m.add(&Loc{ID: pa.ID, Kind: "param", Pkg: pkg.Path, Decl: fn.ID, Parent: fn.ID, Span: pa.Span, Node: pa})
			}
			var walk func(n *ir.Node, parent string)
			walk = func(n *ir.Node, parent string) {
				kind := "expr"
				if ir.IsStmt(n.Op) {
					kind = "stmt"
				}
				m.add(&Loc{ID: n.ID, Kind: kind, Pkg: pkg.Path, Decl: fn.ID, Parent: parent, Span: n.Span, Node: n})
				for _, c := range n.Children() {
					walk(c, n.ID)
				}
			}
			for _, st := range fn.Body {
				walk(st, fn.ID)
			}
		}
	}
}

// Text returns the source text of a span.
func (m *Module) Text(s ir.Span) string {
	return string(m.Files[s.File].Src[s.Off:s.End])
}

// Hash is a short content hash of an id's source text. Edits that name it
// are rejected when the text has changed.
func (m *Module) Hash(id string) string {
	l := m.Index[id]
	if l == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(m.Text(l.Span)))
	return hex.EncodeToString(sum[:6])
}

// Revision hashes every module file (not std): path and contents.
func (m *Module) Revision() string {
	h := sha256.New()
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "std:") {
			continue
		}
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write(f.Src)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Where resolves a span to a diag-ready location.
func (m *Module) Where(s ir.Span) (file string, a, b Pos, line string) {
	f := m.Files[s.File]
	a, b = f.Pos(s.Off), f.Pos(s.End)
	return m.DisplayPath(f), a, b, f.Line(a.Line)
}

// Locate fills location fields of d from its id.
func (m *Module) Locate(d *Diag) {
	l := m.Index[d.ID]
	if l == nil {
		return
	}
	file, a, b, line := m.Where(l.Span)
	d.File, d.Line, d.Col, d.EndLine, d.EndCol, d.Source = file, a.Line, a.Col, b.Line, b.Col, line
}

// IsStd reports whether file index fi belongs to a shipped package.
func (m *Module) IsStd(fi int) bool { return strings.HasPrefix(m.Files[fi].Path, "std:") }

// Lookup finds an id, also accepting a bare name or pkg.Name for decls.
func (m *Module) Lookup(q string) ([]*Loc, error) {
	if l, ok := m.Index[q]; ok {
		return []*Loc{l}, nil
	}
	var out []*Loc
	for _, id := range m.Order {
		l := m.Index[id]
		switch l.Kind {
		case "func", "type", "const":
		default:
			continue
		}
		name := id[strings.LastIndex(id, ".")+1:]
		if q == name || q == l.Pkg+"."+name || pkgSuffix(l.Pkg, q, name) {
			out = append(out, l)
		}
	}
	if len(out) == 0 && strings.Contains(q, ".") {
		// Type.field and Func.param, optionally package-qualified.
		for _, id := range m.Order {
			l := m.Index[id]
			if l.Kind != "field" && l.Kind != "param" {
				continue
			}
			rest := id[strings.Index(id, ":")+1+len(l.Pkg)+1:]
			if q == rest || q == l.Pkg+"."+rest || pkgSuffix(l.Pkg, q, rest) {
				out = append(out, l)
			}
		}
	}
	if len(out) == 0 {
		var cands []string
		for _, id := range m.Order {
			if k := m.Index[id].Kind; k == "func" || k == "type" || k == "const" {
				cands = append(cands, id)
			}
		}
		msg := "no node with id or name " + q
		var locals []string
		for _, id := range m.Order {
			l := m.Index[id]
			if (l.Kind == "param" && strings.HasSuffix(id, "."+q)) || (l.Kind == "stmt" && l.Node.(*ir.Node).Op == "var" && l.Node.(*ir.Node).Name == q) {
				locals = append(locals, id)
			}
		}
		if len(locals) > 0 {
			msg += "; names alone only find funcs, types, and consts; params and locals named " + q + ": " + strings.Join(locals, ", ")
		} else if s := suggestID(q, cands); s != "" {
			msg += "; did you mean " + s + "?"
		} else {
			msg += "; give a name (Sum, util.Sum, app/util.Sum, Pair.next, Sum.n) or an id (fn:app/util.Sum, st:app/util.Sum:3); see ovid help ids"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return out, nil
}

// pkgSuffix reports whether q is name qualified by a trailing part of the
// package path: mem.Eq for ovid/mem.Eq.
func pkgSuffix(pkg, q, name string) bool {
	qp, ok := strings.CutSuffix(q, "."+name)
	return ok && qp != "" && strings.HasSuffix(pkg, "/"+qp)
}

func suggestID(q string, ids []string) string {
	for _, id := range ids {
		if strings.EqualFold(id, q) || strings.HasSuffix(strings.ToLower(id), "."+strings.ToLower(q)) {
			return id
		}
	}
	return ""
}

// WriteFile writes a module file atomically.
func WriteFile(abs string, src []byte) error {
	tmp := abs + ".ovid-tmp"
	if err := os.WriteFile(tmp, src, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, abs)
}

// Trim is used by callers that print source snippets.
func Trim(s string) string { return string(bytes.TrimSpace([]byte(s))) }
