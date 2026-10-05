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
	// Full is Span widened to take in the doc comment of a func, type, or
	// const (see DocStart): the text that show prints, a hash covers, and
	// replace and delete swap or remove. For every other node it is Span.
	Full ir.Span
	Node any
	decl *Loc // the copy of the enclosing decl this node is in
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
	// index, order, locs, copies, and byNode are built together on first
	// use (Index): check, build, run, and test never need them, and they
	// are a quarter of a loaded module's memory. index maps each id to its
	// node, the first in load order when several share it (check reports
	// the duplicate); order lists each id once; copies lists every node of
	// an id that more than one node has, and locs every node.
	index  map[string]*Loc
	order  []string
	locs   []*Loc
	copies map[string][]*Loc
	byNode map[any]*Loc
	hashes map[*Loc]string // full digests, filled on first Hash
	modSrc []byte          // ovid.mod as read
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

// LoadBuild is Load without the module's _test.ov files: the program that
// build and run compile, so an error in a test cannot stop them.
func LoadBuild(dir string) (*Module, error) { return load(dir, nil, true) }

// LoadOverlay is Load with some files replaced by in-memory contents, keyed
// by absolute path. Overlay files that do not exist on disk are added.
func LoadOverlay(dir string, overlay map[string][]byte) (*Module, error) {
	return load(dir, overlay, false)
}

func load(dir string, overlay map[string][]byte, noTests bool) (*Module, error) {
	root, err := Find(dir)
	if err != nil {
		return nil, err
	}
	m := &Module{Root: root, Std: map[string]bool{}}
	modSrc, err := os.ReadFile(filepath.Join(root, "ovid.mod"))
	if err != nil {
		return nil, err
	}
	m.modSrc = modSrc
	for _, ln := range strings.Split(string(modSrc), "\n") {
		f := strings.Fields(ln)
		if len(f) == 0 || strings.HasPrefix(f[0], "//") {
			continue
		}
		if len(f) != 2 {
			return nil, fmt.Errorf("ovid.mod: bad line %q; want `module <name>` or `entry <pkg>`", ln)
		}
		switch f[0] {
		case "module":
			m.Name = f[1]
		case "entry":
			m.Entry = f[1]
		case "std":
			// A module once could name its own standard library here, and
			// with it take the right to call syscall.
			return nil, fmt.Errorf("ovid.mod: the std line is no longer supported: a module always uses the standard library built into ovid; delete the line")
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
		if !strings.HasSuffix(p, ".ov") || noTests && strings.HasSuffix(p, "_test.ov") {
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
		m.addPackage(pk, byPkg[pk], fromModule)
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
			files := stdFiles(pk)
			if len(files) == 0 {
				continue
			}
			m.Std[pk] = true
			m.addPackage(pk, files, fromToolchain)
		}
	}
	sort.SliceStable(m.Prog.Packages, func(i, j int) bool { return m.Prog.Packages[i].Path < m.Prog.Packages[j].Path })
	for _, f := range m.Files {
		m.Prog.Files = append(m.Prog.Files, f.Path)
	}
	return m, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// stdFiles are the files of a package the toolchain ships, or none.
func stdFiles(pkg string) []*File {
	var out []*File
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

// Where a package's files came from.
const (
	fromModule    = iota // the module's own directories
	fromToolchain        // the standard library built into ovid
)

// shipped reports whether the toolchain ships a package of this path.
func shipped(pkgPath string) bool {
	ents, _ := fs.ReadDir(std.FS, pkgPath)
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ov") {
			return true
		}
	}
	return false
}

func (m *Module) addPackage(pkgPath string, files []*File, from int) {
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
			hint := ""
			if strings.HasPrefix(perr.Msg, "expected package") {
				hint = "every .ov file starts with `package " + pkgPath + "` (its directory path), then its imports"
			}
			m.Errors = append(m.Errors, m.diagAt(idx, perr.Off, perr.Off, "syntax", perr.Msg, hint))
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
			merged.Sys = from == fromToolchain
			if from == fromModule && shipped(pkgPath) {
				// Once for the package, at its first file's package clause.
				m.Errors = append(m.Errors, m.diagAt(idx, pkg.Span.Off, pkg.Span.End, "reserved_path",
					"package "+pkgPath+" is one the toolchain ships; a module may not have its own",
					"move these files to a package path of the module's own, and import "+pkgPath+" for the shipped one"))
			}
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

// PathsEnv selects what the paths in records are relative to: unset or
// "cwd", the working directory; "module", the module root.
const PathsEnv = "OVID_PATHS"

// DisplayPath is the path an agent can open: relative to the cwd when
// possible. With OVID_PATHS=module it is relative to the module root
// instead ("std:<path>" for a shipped package), so that copies of one
// module report the same paths wherever they are and wherever ovid runs.
func (m *Module) DisplayPath(f *File) string {
	if os.Getenv(PathsEnv) == "module" {
		if f.Path != "" {
			return filepath.ToSlash(f.Path)
		}
		if rel, err := filepath.Rel(m.Root, f.Abs); err == nil {
			return filepath.ToSlash(rel)
		}
		return f.Abs
	}
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

// add indexes l, a node of decl (nil for a package), and returns it.
func (m *Module) add(l *Loc, decl *Loc) *Loc {
	if l.ID == "" {
		return l
	}
	l.decl = decl
	if decl == nil {
		l.decl = l
	}
	l.Full = l.Span
	switch l.Kind {
	case "func", "type", "const":
		l.Full.Off = DocStart(m.Files[l.Span.File].Src, l.Span.Off)
	}
	m.locs = append(m.locs, l)
	m.byNode[l.Node] = l
	if first, dup := m.index[l.ID]; dup {
		if m.copies[l.ID] == nil {
			m.copies[l.ID] = []*Loc{first}
		}
		m.copies[l.ID] = append(m.copies[l.ID], l)
		return l
	}
	m.index[l.ID] = l
	m.order = append(m.order, l.ID)
	return l
}

// Index maps every id of the module to its node and place. Where several
// nodes share an id, the first in load order keeps it; Copies lists all.
func (m *Module) Index() map[string]*Loc {
	if m.index == nil {
		m.index = map[string]*Loc{}
		m.copies = map[string][]*Loc{}
		m.byNode = map[any]*Loc{}
		m.buildIndex()
	}
	return m.index
}

// Order lists the ids of Index in program order: packages by path, and
// each decl before the nodes inside it.
func (m *Module) Order() []string {
	m.Index()
	return m.order
}

// Copies lists every node with the given id in load order (by file, then
// position): one for a well-formed module, several where declarations
// share the id (check reports them), none for an unknown id.
func (m *Module) Copies(id string) []*Loc {
	idx := m.Index()
	if c := m.copies[id]; c != nil {
		return c
	}
	if l := idx[id]; l != nil {
		return []*Loc{l}
	}
	return nil
}

// Locs lists every indexed node in load order, each copy of a duplicated
// id included.
func (m *Module) Locs() []*Loc {
	m.Index()
	return m.locs
}

// LocOf is the node indexed for an ir node (a *ir.Func, *ir.Node, ...).
func (m *Module) LocOf(node any) *Loc {
	m.Index()
	return m.byNode[node]
}

// DeclLoc is the copy of the top-level decl that l is in (l itself for a
// decl): the one whose hash a st:/ex: hash is bound to.
func (m *Module) DeclLoc(l *Loc) *Loc { return l.decl }

func (m *Module) buildIndex() {
	for pi := range m.Prog.Packages {
		pkg := &m.Prog.Packages[pi]
		m.add(&Loc{ID: pkg.ID, Kind: "package", Pkg: pkg.Path, Span: pkg.Span, Node: pkg}, nil)
		for i := range pkg.Imports {
			im := &pkg.Imports[i]
			m.add(&Loc{ID: im.ID, Kind: "import", Pkg: pkg.Path, Decl: im.ID, Parent: pkg.ID, Span: im.Span, Node: im}, nil)
		}
		for i := range pkg.Consts {
			c := &pkg.Consts[i]
			m.add(&Loc{ID: c.ID, Kind: "const", Pkg: pkg.Path, Decl: c.ID, Parent: pkg.ID, Span: c.Span, Node: c}, nil)
		}
		for i := range pkg.Types {
			t := &pkg.Types[i]
			tl := m.add(&Loc{ID: t.ID, Kind: "type", Pkg: pkg.Path, Decl: t.ID, Parent: pkg.ID, Span: t.Span, Node: t}, nil)
			for j := range t.Fields {
				f := &t.Fields[j]
				m.add(&Loc{ID: f.ID, Kind: "field", Pkg: pkg.Path, Decl: t.ID, Parent: t.ID, Span: f.Span, Node: f}, tl)
			}
		}
		for i := range pkg.Funcs {
			fn := &pkg.Funcs[i]
			fl := m.add(&Loc{ID: fn.ID, Kind: "func", Pkg: pkg.Path, Decl: fn.ID, Parent: pkg.ID, Span: fn.Span, Node: fn}, nil)
			for j := range fn.Params {
				pa := &fn.Params[j]
				m.add(&Loc{ID: pa.ID, Kind: "param", Pkg: pkg.Path, Decl: fn.ID, Parent: fn.ID, Span: pa.Span, Node: pa}, fl)
			}
			var walk func(n *ir.Node, parent string)
			walk = func(n *ir.Node, parent string) {
				kind := "expr"
				if ir.IsStmt(n.Op) {
					kind = "stmt"
				}
				m.add(&Loc{ID: n.ID, Kind: kind, Pkg: pkg.Path, Decl: fn.ID, Parent: parent, Span: n.Span, Node: n}, fl)
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

// DocStart is where the doc comment of the decl at off begins: the first
// non-blank byte of the unbroken run of // lines directly above the line
// off is on. A blank line ends the run, so a comment separated from the
// decl by one is not its doc comment. With no such lines it is off.
func DocStart(src []byte, off int) int {
	start := lineBegin(src, off)
	if strings.TrimSpace(string(src[start:off])) != "" {
		// Something precedes the decl on its line; it has no doc comment.
		return off
	}
	doc := off
	for start > 0 {
		pl := lineBegin(src, start-1)
		if !strings.HasPrefix(strings.TrimSpace(string(src[pl:start-1])), "//") {
			break
		}
		start = pl
		doc = pl
		for src[doc] == ' ' || src[doc] == '\t' {
			doc++
		}
	}
	return doc
}

func lineBegin(src []byte, off int) int {
	for off > 0 && src[off-1] != '\n' {
		off--
	}
	return off
}

// Text returns the source text of a span.
func (m *Module) Text(s ir.Span) string {
	return string(m.Files[s.File].Src[s.Off:s.End])
}

// Hash is a short content hash of an id's source text. Edits that name it
// are rejected when the text has changed. A declaration's text (Full)
// includes its name and its doc comment, so its hash is the text's alone.
// A statement's or expression's id is a position, so its hash is bound to
// the whole declaration it is in: it covers the decl's id and full text,
// doc comment included, its own kind and text, and which
// of the identical-text nodes there it is. Any change to that decl, even
// one elsewhere in it, changes the hash of every statement in it, so a
// stale st:/ex: id never matches, not even on a text-identical twin that
// inherited its position; a change to another decl changes nothing. It is
// a staleness check, not a cache key. Where several nodes share the id,
// it is the first one's; LocHash gives each its own.
func (m *Module) Hash(id string) string { return m.LocHash(m.Index()[id]) }

// LocHash is Hash of one node, so of one copy of a duplicated id: copies
// with different text have different hashes. A statement's is bound to
// the copy of the decl it is in.
func (m *Module) LocHash(l *Loc) string {
	if h := m.LocDigest(l); h != "" {
		return h[:12]
	}
	return ""
}

// Digest is the full sha256 that Hash prints the first 12 digits of. The
// short form guards an edit; this one is wide enough to key a cache.
func (m *Module) Digest(id string) string { return m.LocDigest(m.Index()[id]) }

// LocDigest is Digest of one node.
func (m *Module) LocDigest(l *Loc) string {
	if l == nil {
		return ""
	}
	if m.hashes == nil {
		m.hashes = map[*Loc]string{}
		type key struct {
			decl *Loc
			k    string
		}
		seen := map[key]int{}
		for _, o := range m.Locs() {
			text := m.Text(o.Full)
			in := text
			if o.Kind == "stmt" || o.Kind == "expr" {
				k := key{o.decl, o.Kind + "\x00" + o.Decl + "\x00" + text}
				// The decl's digest, which covers its text; it precedes its
				// statements, so it is already known.
				in = fmt.Sprintf("%s\x00%d\x00%s", k.k, seen[k], m.hashes[o.decl])
				seen[k]++
			}
			sum := sha256.Sum256([]byte(in))
			m.hashes[o] = hex.EncodeToString(sum[:])
		}
	}
	return m.hashes[l]
}

// Revision is the first 16 digits of RevisionDigest: enough to tell an
// agent that the module moved.
func (m *Module) Revision() string { return m.RevisionDigest()[:16] }

// RevisionDigest is the sha256 of everything a build of the module reads:
// ovid.mod, every module file in load order, then every std file that was
// loaded, sorted by path. Each is hashed as name, NUL, contents, NUL. The
// self-hosted compiler computes the same value (prog/ovid/parse Revision).
func (m *Module) RevisionDigest() string {
	h := sha256.New()
	add := func(name string, src []byte) {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(src)
		h.Write([]byte{0})
	}
	add("ovid.mod", m.modSrc)
	var stds []*File
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "std:") {
			stds = append(stds, f)
			continue
		}
		add(f.Path, f.Src)
	}
	sort.Slice(stds, func(i, j int) bool { return stds[i].Path < stds[j].Path })
	for _, f := range stds {
		add(f.Path, f.Src)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Where resolves a span to a diag-ready location.
func (m *Module) Where(s ir.Span) (file string, a, b Pos, line string) {
	f := m.Files[s.File]
	a, b = f.Pos(s.Off), f.Pos(s.End)
	return m.DisplayPath(f), a, b, f.Line(a.Line)
}

// Locate fills location fields of d from its id.
func (m *Module) Locate(d *Diag) {
	l := m.Index()[d.ID]
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
	if _, ok := m.Index()[q]; ok {
		return m.Copies(q), nil
	}
	var out []*Loc
	for _, id := range m.Order() {
		l := m.Index()[id]
		switch l.Kind {
		case "func", "type", "const":
		default:
			continue
		}
		name := id[strings.LastIndex(id, ".")+1:]
		if q == name || q == l.Pkg+"."+name || pkgSuffix(l.Pkg, q, name) {
			out = append(out, m.Copies(id)...)
		}
	}
	if len(out) == 0 && strings.Contains(q, ".") {
		// Type.field and Func.param, optionally package-qualified.
		for _, id := range m.Order() {
			l := m.Index()[id]
			if l.Kind != "field" && l.Kind != "param" {
				continue
			}
			rest := id[strings.Index(id, ":")+1+len(l.Pkg)+1:]
			if q == rest || q == l.Pkg+"."+rest || pkgSuffix(l.Pkg, q, rest) {
				out = append(out, m.Copies(id)...)
			}
		}
	}
	if len(out) == 0 {
		var cands []string
		for _, id := range m.Order() {
			if k := m.Index()[id].Kind; k == "func" || k == "type" || k == "const" {
				cands = append(cands, id)
			}
		}
		msg := "no node with id or name " + q
		var locals []string
		for _, id := range m.Order() {
			l := m.Index()[id]
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

// Trim is used by callers that print source snippets.
func Trim(s string) string { return string(bytes.TrimSpace([]byte(s))) }
