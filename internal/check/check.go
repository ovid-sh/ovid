// Package check typechecks an Ovid program and prints one JSON fact per line.
package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"ovid/internal/ir"
)

type Fact struct {
	raw []byte
}

func (f Fact) Bytes() []byte { return f.raw }

type result struct {
	facts  [][]byte
	errors int
}

func (r *result) add(v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	r.facts = append(r.facts, bytes.TrimRight(buf.Bytes(), "\n"))
}

func (r *result) err(id, pkg, fn, code, detail string) {
	r.errors++
	r.add(struct {
		Fact   string `json:"fact"`
		ID     string `json:"id"`
		Pkg    string `json:"pkg,omitempty"`
		Func   string `json:"func,omitempty"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}{Fact: "error", ID: id, Pkg: pkg, Func: fn, Code: code, Detail: detail})
}

func (r *result) mismatch(n *ir.Node, pkg, fn, expected, actual string) {
	id := ""
	if n != nil {
		id = n.ID
	}
	r.errors++
	r.add(struct {
		Fact     string `json:"fact"`
		ID       string `json:"id"`
		Pkg      string `json:"pkg"`
		Func     string `json:"func"`
		Code     string `json:"code"`
		Detail   string `json:"detail"`
		Expected string `json:"expected"`
		Actual   string `json:"actual"`
	}{"error", id, pkg, fn, "type_mismatch", "expected " + expected + ", got " + actual, expected, actual})
}

// Run checks p. The JSON document contains all local packages; presentation
// files and package directory markers are not compilation inputs.
// file is the raw ovid.json used for the revision fact. facts are one JSON
// object per line, without trailing newlines.
func Run(p *ir.Program, root string, file []byte) (lines [][]byte, errors int) {
	var r result
	if p == nil {
		r.err("", "", "", "no_program", "missing program")
		return r.facts, r.errors
	}
	stored, computed, herr := "", "", error(nil)
	if file != nil {
		stored, computed, herr = ir.Hash(file)
		if herr != nil {
			r.err("", "", "", "revision", herr.Error())
		} else if stored != computed {
			r.err("", p.Module, "", "revision_mismatch", "stored revision does not match file bytes")
		}
	}
	r.add(struct {
		Fact     string `json:"fact"`
		Revision string `json:"revision"`
		Module   string `json:"module"`
		Entry    string `json:"entry"`
		Packages int    `json:"packages"`
	}{Fact: "module", Revision: computed, Module: p.Module, Entry: p.Entry, Packages: len(p.Packages)})

	if strings.TrimSpace(p.Module) == "" {
		r.err("", "", "", "bad_module", "module name is empty")
	}
	pkgs := map[string]*ir.Package{}
	ids := map[string]string{}
	claim := func(id, what string) {
		if id == "" {
			r.err("", "", "", "missing_id", what)
			return
		}
		if prev, ok := ids[id]; ok {
			r.err(id, "", "", "duplicate_id", prev)
			return
		}
		ids[id] = what
	}

	for i := range p.Packages {
		pkg := &p.Packages[i]
		claim(pkg.ID, "package")
		if prev, ok := pkgs[pkg.Path]; ok {
			r.err(pkg.ID, pkg.Path, "", "duplicate_package", prev.ID)
		}
		pkgs[pkg.Path] = pkg
		r.add(struct {
			Fact string `json:"fact"`
			ID   string `json:"id"`
			Path string `json:"path"`
		}{Fact: "package", ID: pkg.ID, Path: pkg.Path})
		for _, im := range pkg.Imports {
			claim(im.ID, "import")
		}
		for _, c := range pkg.Consts {
			claim(c.ID, "const")
		}
		for _, t := range pkg.Types {
			claim(t.ID, "type")
			for _, f := range t.Fields {
				claim(f.ID, "field")
			}
		}
		for _, fn := range pkg.Funcs {
			claim(fn.ID, "func")
			for _, pa := range fn.Params {
				claim(pa.ID, "param")
			}
			for _, st := range fn.Body {
				if st == nil {
					continue
				}
				st.Walk(func(c *ir.Node) { claim(c.ID, "node") })
			}
		}
	}

	for i := range p.Packages {
		pkg := &p.Packages[i]
		for _, im := range pkg.Imports {
			target, ok := pkgs[im.Path]
			resolved := ok && target.Path == im.Path
			r.add(struct {
				Fact     string `json:"fact"`
				ID       string `json:"id"`
				Pkg      string `json:"pkg"`
				Path     string `json:"path"`
				Resolved bool   `json:"resolved"`
			}{Fact: "import", ID: im.ID, Pkg: pkg.Path, Path: im.Path, Resolved: resolved})
			if im.Path == pkg.Path {
				r.err(im.ID, pkg.Path, "", "import_self", im.Path)
			}
			if !ok {
				r.err(im.ID, pkg.Path, "", "unknown_package", im.Path)
			}
		}
		seenTypes := map[string]bool{}
		for _, t := range pkg.Types {
			if seenTypes[t.Name] {
				r.err(t.ID, pkg.Path, "", "duplicate_type", t.Name)
			}
			seenTypes[t.Name] = true
			r.add(struct {
				Fact   string `json:"fact"`
				ID     string `json:"id"`
				Pkg    string `json:"pkg"`
				Name   string `json:"name"`
				Fields int    `json:"fields"`
				Size   int    `json:"size"`
			}{Fact: "type", ID: t.ID, Pkg: pkg.Path, Name: t.Name, Fields: len(t.Fields), Size: len(t.Fields) * 8})
			seenFields := map[string]bool{}
			for i, f := range t.Fields {
				if seenFields[f.Name] {
					r.err(f.ID, pkg.Path, "", "duplicate_field", f.Name)
				}
				seenFields[f.Name] = true
				ft, _, ferr := resolveType(pkg, f.Type, pkgs)
				r.add(struct {
					Fact   string `json:"fact"`
					ID     string `json:"id"`
					Pkg    string `json:"pkg"`
					Type   string `json:"type"`
					Name   string `json:"name"`
					Of     string `json:"of"`
					Offset int    `json:"offset"`
				}{Fact: "field", ID: f.ID, Pkg: pkg.Path, Type: t.Name, Name: f.Name, Of: ft, Offset: i * 8})
				if ferr != nil {
					r.err(f.ID, pkg.Path, "", "bad_type", ferr.Error())
				}
				if ft != "i64" && ft != "bool" && !strings.HasPrefix(ft, "*") {
					r.err(f.ID, pkg.Path, "", "bad_type", "field must be i64, bool, or a pointer")
				}
			}
		}
		seenConsts := map[string]bool{}
		for _, c := range pkg.Consts {
			if seenConsts[c.Name] {
				r.err(c.ID, pkg.Path, "", "duplicate_const", c.Name)
			}
			seenConsts[c.Name] = true
			r.add(struct {
				Fact  string `json:"fact"`
				ID    string `json:"id"`
				Pkg   string `json:"pkg"`
				Name  string `json:"name"`
				Type  string `json:"type"`
				Value int64  `json:"value"`
			}{Fact: "const", ID: c.ID, Pkg: pkg.Path, Name: c.Name, Type: c.Type, Value: c.Value})
			if c.Type != "i64" {
				r.err(c.ID, pkg.Path, "", "bad_type", "const must be i64")
			}
		}
		for _, fn := range pkg.Funcs {
			r.add(struct {
				Fact   string `json:"fact"`
				ID     string `json:"id"`
				Pkg    string `json:"pkg"`
				Name   string `json:"name"`
				Params int    `json:"params"`
				Result string `json:"result"`
			}{Fact: "func", ID: fn.ID, Pkg: pkg.Path, Name: fn.Name, Params: len(fn.Params), Result: fn.Result})
			if len(fn.Params) > 6 {
				r.err(fn.ID, pkg.Path, fn.Name, "arity", "at most 6 parameters")
			}
			for i, pa := range fn.Params {
				pt, _, perr := resolveType(pkg, pa.Type, pkgs)
				r.add(struct {
					Fact  string `json:"fact"`
					ID    string `json:"id"`
					Pkg   string `json:"pkg"`
					Func  string `json:"func"`
					Name  string `json:"name"`
					Type  string `json:"type"`
					Index int    `json:"index"`
				}{Fact: "param", ID: pa.ID, Pkg: pkg.Path, Func: fn.Name, Name: pa.Name, Type: pt, Index: i})
				if perr != nil {
					r.err(pa.ID, pkg.Path, fn.Name, "bad_type", perr.Error())
				}
				if strings.HasPrefix(pt, "*") || pt == "i64" || pt == "bool" {
					continue
				}
				r.err(pa.ID, pkg.Path, fn.Name, "struct_value", "parameters must be i64, bool, or a pointer")
			}
			rt, _, rerr := resolveType(pkg, fn.Result, pkgs)
			if rerr != nil {
				r.err(fn.ID, pkg.Path, fn.Name, "bad_type", rerr.Error())
			} else if rt != "i64" && rt != "bool" && !strings.HasPrefix(rt, "*") {
				r.err(fn.ID, pkg.Path, fn.Name, "struct_value", "result must be i64, bool, or a pointer")
			}
		}
	}

	sigs := map[string]sig{}
	for i := range p.Packages {
		pkg := &p.Packages[i]
		seen := map[string]bool{}
		for _, fn := range pkg.Funcs {
			if seen[fn.Name] {
				r.err(fn.ID, pkg.Path, fn.Name, "duplicate_func", fn.Name)
			}
			seen[fn.Name] = true
			var ps []string
			for _, pa := range fn.Params {
				t, _, _ := resolveType(pkg, pa.Type, pkgs)
				ps = append(ps, t)
			}
			res, _, _ := resolveType(pkg, fn.Result, pkgs)
			sigs[pkg.Path+"."+fn.Name] = sig{params: ps, result: res, id: fn.ID}
		}
	}

	var entryFn *ir.Func
	var entryPkg *ir.Package
	if ep, ok := pkgs[p.Entry]; !ok {
		r.err("", "", "", "no_entry", p.Entry)
	} else {
		entryPkg = ep
		for i := range ep.Funcs {
			if ep.Funcs[i].Name == "main" {
				entryFn = &ep.Funcs[i]
			}
		}
		if entryFn == nil {
			r.err(ep.ID, ep.Path, "", "bad_main", "entry package has no main")
		} else {
			pt := ""
			if len(entryFn.Params) == 1 {
				pt, _, _ = resolveType(ep, entryFn.Params[0].Type, pkgs)
			}
			res, _, _ := resolveType(ep, entryFn.Result, pkgs)
			if len(entryFn.Params) != 1 || pt != "*ovid/io.Cap" || res != "i64" {
				r.err(entryFn.ID, ep.Path, "main", "bad_main", "main must be (io *ovid/io.Cap) i64")
			}
		}
	}

	if ioPkg, ok := pkgs["ovid/io"]; ok {
		var capTy *ir.TypeDecl
		for i := range ioPkg.Types {
			if ioPkg.Types[i].Name == "Cap" {
				capTy = &ioPkg.Types[i]
			}
		}
		need := []string{"argc", "argv", "heap", "used", "size"}
		if capTy == nil {
			r.err(ioPkg.ID, "ovid/io", "", "bad_abi", "missing Cap")
		} else {
			valid := len(capTy.Fields) == len(need)
			if valid {
				for i, n := range need {
					if capTy.Fields[i].Name != n || capTy.Fields[i].Type != "i64" {
						valid = false
					}
				}
			}
			if !valid {
				r.err(capTy.ID, "ovid/io", "", "bad_abi", "Cap must have argc, argv, heap, used, size as i64 in that order")
			}

		}
	} else {
		r.err("", "", "", "bad_abi", "missing package ovid/io")
	}

	for i := range p.Packages {
		pkg := &p.Packages[i]
		imported := map[string]bool{}
		for _, im := range pkg.Imports {
			imported[im.Path] = true
		}
		for _, fn := range pkg.Funcs {
			ok := checkBody(&r, pkg, &fn, pkgs, sigs, imported)
			r.add(struct {
				Fact string `json:"fact"`
				ID   string `json:"id"`
				Pkg  string `json:"pkg"`
				Name string `json:"name"`
				Ok   bool   `json:"ok"`
			}{Fact: "checked", ID: fn.ID, Pkg: pkg.Path, Name: fn.Name, Ok: ok})
		}
	}
	_ = entryPkg

	r.add(struct {
		Fact     string `json:"fact"`
		Ok       bool   `json:"ok"`
		Errors   int    `json:"errors"`
		Packages int    `json:"packages"`
		Funcs    int    `json:"funcs"`
		Revision string `json:"revision"`
	}{Fact: "summary", Ok: r.errors == 0, Errors: r.errors, Packages: len(p.Packages), Funcs: countFuncs(p), Revision: computed})
	return r.facts, r.errors
}

func countFuncs(p *ir.Program) int {
	n := 0
	for _, pkg := range p.Packages {
		n += len(pkg.Funcs)
	}
	return n
}

type sig struct {
	params []string
	result string
	id     string
}

func resolveType(pkg *ir.Package, t string, pkgs map[string]*ir.Package) (string, string, error) {
	if t == "i64" || t == "bool" {
		return t, t, nil
	}
	star := false
	if strings.HasPrefix(t, "*") {
		star = true
		t = t[1:]
	}
	if t == "i64" || t == "bool" {
		return "", "", fmt.Errorf("cannot point at %s", t)
	}
	tpkg := pkg.Path
	name := t
	if i := strings.LastIndex(t, "."); i >= 0 {
		tpkg = t[:i]
		name = t[i+1:]
	}
	target, ok := pkgs[tpkg]
	if !ok {
		return "", "", fmt.Errorf("unknown package in type %s", t)
	}
	found := false
	for _, td := range target.Types {
		if td.Name == name {
			found = true
		}
	}
	if !found {
		return "", "", fmt.Errorf("unknown type %s", t)
	}
	full := "*" + tpkg + "." + name
	if !star {
		return tpkg + "." + name, "struct", nil
	}
	return full, "ptr", nil
}

type env struct {
	vars map[string]string
}

func checkBody(r *result, pkg *ir.Package, fn *ir.Func, pkgs map[string]*ir.Package, sigs map[string]sig, imported map[string]bool) bool {
	before := r.errors
	e := &env{vars: map[string]string{}}
	for _, pa := range fn.Params {
		if _, ok := e.vars[pa.Name]; ok {
			r.err(pa.ID, pkg.Path, fn.Name, "duplicate_name", pa.Name)
		}
		t, _, err := resolveType(pkg, pa.Type, pkgs)
		if err != nil {
			t = "invalid"
		}
		e.vars[pa.Name] = t
	}
	res, _, _ := resolveType(pkg, fn.Result, pkgs)
	checkStmts(r, pkg, fn, e, pkgs, sigs, imported, fn.Body, res)
	if !pathsReturn(fn.Body) {
		r.err(fn.ID, pkg.Path, fn.Name, "missing_return", "not every path returns")
	}
	return r.errors == before
}

func pathsReturn(stmts []*ir.Node) bool {
	if len(stmts) == 0 {
		return false
	}
	last := stmts[len(stmts)-1]
	if last == nil {
		return false
	}
	switch last.Op {
	case "return":
		return true
	case "if":
		if len(last.Else) == 0 {
			return false
		}
		return pathsReturn(last.Then) && pathsReturn(last.Else)
	default:
		return false
	}
}

func checkStmts(r *result, pkg *ir.Package, fn *ir.Func, e *env, pkgs map[string]*ir.Package, sigs map[string]sig, imported map[string]bool, stmts []*ir.Node, res string) {
	for _, s := range stmts {
		if s == nil {
			continue
		}
		switch s.Op {
		case "var":
			t, _, err := resolveType(pkg, s.Type, pkgs)
			if err != nil {
				r.err(s.ID, pkg.Path, fn.Name, "bad_type", err.Error())
				t = "invalid"
			}
			if t != "i64" && t != "bool" && !strings.HasPrefix(t, "*") {
				r.err(s.ID, pkg.Path, fn.Name, "struct_value", "local must be i64, bool, or a pointer")
			}
			if _, ok := e.vars[s.Name]; ok {
				r.err(s.ID, pkg.Path, fn.Name, "duplicate_name", s.Name)
			}
			e.vars[s.Name] = t
			if s.Val != nil {
				vt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Val)
				if vt != t && vt != "invalid" && t != "invalid" {
					r.mismatch(s.Val, pkg.Path, fn.Name, t, vt)
				}
			}
		case "assign":
			t, ok := e.vars[s.Name]
			if !ok {
				r.err(s.ID, pkg.Path, fn.Name, "unknown_name", s.Name)
				t = "invalid"
			}
			vt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Val)
			if vt != t && vt != "invalid" && t != "invalid" {
				r.mismatch(s.Val, pkg.Path, fn.Name, t, vt)
			}
		case "setfield":
			bt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Base)
			ft, ferr := fieldType(bt, s.Name, pkgs)
			if ferr != nil {
				r.err(s.ID, pkg.Path, fn.Name, "unknown_field", ferr.Error())
				ft = "invalid"
			}
			vt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Val)
			if vt != ft && vt != "invalid" && ft != "invalid" {
				r.mismatch(s.Val, pkg.Path, fn.Name, ft, vt)
			}
		case "store8", "store64":
			at := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Addr)
			vt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Val)
			if at != "i64" && at != "invalid" {
				r.mismatch(s.Addr, pkg.Path, fn.Name, "i64", at)
			}
			if vt != "i64" && vt != "invalid" {
				r.mismatch(s.Val, pkg.Path, fn.Name, "i64", vt)
			}
		case "expr":
			checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Val)
		case "return":
			vt := "i64"
			if s.Val != nil {
				vt = checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Val)
			} else {
				vt = "void"
			}
			if vt != res && vt != "invalid" {
				target := s.Val
				if target == nil {
					target = s
				}
				r.mismatch(target, pkg.Path, fn.Name, res, vt)
			}
		case "if":
			ct := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Cond)
			if ct != "bool" && ct != "invalid" {
				r.mismatch(s.Cond, pkg.Path, fn.Name, "bool", ct)
			}
			checkStmts(r, pkg, fn, childEnv(e), pkgs, sigs, imported, s.Then, res)
			checkStmts(r, pkg, fn, childEnv(e), pkgs, sigs, imported, s.Else, res)
		case "while":
			ct := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, s.Cond)
			if ct != "bool" && ct != "invalid" {
				r.mismatch(s.Cond, pkg.Path, fn.Name, "bool", ct)
			}
			checkStmts(r, pkg, fn, childEnv(e), pkgs, sigs, imported, s.Body, res)
		default:
			r.err(s.ID, pkg.Path, fn.Name, "bad_op", s.Op)
		}
	}
}

func childEnv(e *env) *env {
	n := &env{vars: map[string]string{}}
	for k, v := range e.vars {
		n.vars[k] = v
	}
	return n
}

func checkExpr(r *result, pkg *ir.Package, fn *ir.Func, e *env, pkgs map[string]*ir.Package, sigs map[string]sig, imported map[string]bool, n *ir.Node) string {
	if n == nil {
		r.err(fn.ID, pkg.Path, fn.Name, "missing_expr", "empty expression")
		return "invalid"
	}
	switch n.Op {
	case "int":
		if n.ValK != 1 {
			r.err(n.ID, pkg.Path, fn.Name, "bad_op", "int needs an integer value")
			return "invalid"
		}
		return "i64"
	case "bool":
		if n.ValK != 2 {
			r.err(n.ID, pkg.Path, fn.Name, "bad_op", "bool needs a boolean value")
			return "invalid"
		}
		return "bool"
	case "strptr", "strlen":
		if n.ValK != 3 {
			r.err(n.ID, pkg.Path, fn.Name, "bad_op", n.Op+" needs a string value")
			return "invalid"
		}
		return "i64"
	case "name":
		if t, ok := e.vars[n.Name]; ok {
			return t
		}
		for _, c := range pkg.Consts {
			if c.Name == n.Name {
				return "i64"
			}
		}
		r.err(n.ID, pkg.Path, fn.Name, "unknown_name", n.Name)
		return "invalid"
	case "add", "sub", "mul", "div", "mod", "and", "or", "xor", "shl", "shr":
		lt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Left)
		rt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Right)
		if lt != "i64" && lt != "invalid" {
			r.mismatch(n.Left, pkg.Path, fn.Name, "i64", lt)
		}
		if rt != "i64" && rt != "invalid" {
			r.mismatch(n.Right, pkg.Path, fn.Name, "i64", rt)
		}
		return "i64"
	case "eq", "ne":
		lt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Left)
		rt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Right)
		if lt != rt && lt != "invalid" && rt != "invalid" {
			r.mismatch(n.Right, pkg.Path, fn.Name, lt, rt)
		}
		return "bool"
	case "lt", "le", "gt", "ge":
		lt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Left)
		rt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Right)
		if lt != "i64" && lt != "invalid" {
			r.mismatch(n.Left, pkg.Path, fn.Name, "i64", lt)
		}
		if rt != "i64" && rt != "invalid" {
			r.mismatch(n.Right, pkg.Path, fn.Name, "i64", rt)
		}
		return "bool"
	case "land", "lor":
		lt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Left)
		rt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Right)
		if lt != "bool" && lt != "invalid" {
			r.mismatch(n.Left, pkg.Path, fn.Name, "bool", lt)
		}
		if rt != "bool" && rt != "invalid" {
			r.mismatch(n.Right, pkg.Path, fn.Name, "bool", rt)
		}
		return "bool"
	case "not":
		t := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Arg)
		if t != "bool" && t != "invalid" {
			r.mismatch(n.Arg, pkg.Path, fn.Name, "bool", t)
		}
		return "bool"
	case "neg", "bnot":
		t := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Arg)
		if t != "i64" && t != "invalid" {
			r.mismatch(n.Arg, pkg.Path, fn.Name, "i64", t)
		}
		return "i64"
	case "cast":
		t, _, err := resolveType(pkg, n.Type, pkgs)
		if err != nil {
			r.err(n.ID, pkg.Path, fn.Name, "bad_type", err.Error())
			return "invalid"
		}
		src := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Arg)
		if !castOK(src, t) && src != "invalid" {
			r.mismatch(n.Arg, pkg.Path, fn.Name, t, src)
		}
		return t
	case "field":
		bt := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Base)
		ft, err := fieldType(bt, n.Name, pkgs)
		if err != nil {
			r.err(n.ID, pkg.Path, fn.Name, "unknown_field", err.Error())
			return "invalid"
		}
		return ft
	case "call":
		path := n.Pkg
		if path == "" {
			path = pkg.Path
		} else if path != pkg.Path && !imported[path] {
			r.err(n.ID, pkg.Path, fn.Name, "unknown_package", "call "+path+"."+n.Func+" without import")
		}
		sg, ok := sigs[path+"."+n.Func]
		if !ok {
			r.err(n.ID, pkg.Path, fn.Name, "unknown_name", path+"."+n.Func)
			for _, a := range n.Args {
				checkExpr(r, pkg, fn, e, pkgs, sigs, imported, a)
			}
			return "invalid"
		}
		if len(n.Args) != len(sg.params) {
			r.err(n.ID, pkg.Path, fn.Name, "arity", fmt.Sprintf("%s wants %d args", n.Func, len(sg.params)))
		}
		for i, a := range n.Args {
			at := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, a)
			if i < len(sg.params) && at != sg.params[i] && at != "invalid" && sg.params[i] != "" {
				r.mismatch(a, pkg.Path, fn.Name, sg.params[i], at)
			}
		}
		return sg.result
	case "syscall":
		if pkg.Path != "ovid/io" {
			r.err(n.ID, pkg.Path, fn.Name, "syscall_forbidden", "syscall is only valid in package ovid/io")
		}
		if len(n.Args) != 7 {
			r.err(n.ID, pkg.Path, fn.Name, "arity", "syscall wants 7 i64 args")
		}
		for _, a := range n.Args {
			at := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, a)
			if at != "i64" && at != "invalid" {
				r.mismatch(a, pkg.Path, fn.Name, "i64", at)
			}
		}
		return "i64"
	case "load8", "load32", "load64":
		at := checkExpr(r, pkg, fn, e, pkgs, sigs, imported, n.Arg)
		if at != "i64" && at != "invalid" {
			r.mismatch(n.Arg, pkg.Path, fn.Name, "i64", at)
		}
		return "i64"
	default:
		r.err(n.ID, pkg.Path, fn.Name, "bad_op", n.Op)
		return "invalid"
	}
}

func castOK(src, dst string) bool {
	if src == dst {
		return true
	}
	isPtr := func(s string) bool { return strings.HasPrefix(s, "*") }
	if (src == "i64" || isPtr(src) || src == "bool") && (dst == "i64" || isPtr(dst) || dst == "bool") {
		return true
	}
	return false
}

func fieldType(baseType, field string, pkgs map[string]*ir.Package) (string, error) {
	if !strings.HasPrefix(baseType, "*") {
		return "", fmt.Errorf("field %s on non-pointer %s", field, baseType)
	}
	full := strings.TrimPrefix(baseType, "*")
	i := strings.LastIndex(full, ".")
	if i < 0 {
		return "", fmt.Errorf("bad pointer type %s", baseType)
	}
	tpkg, name := full[:i], full[i+1:]
	pkg := pkgs[tpkg]
	if pkg == nil {
		return "", fmt.Errorf("unknown package %s", tpkg)
	}
	for _, t := range pkg.Types {
		if t.Name != name {
			continue
		}
		for _, f := range t.Fields {
			if f.Name == field {
				ft, _, err := resolveType(pkg, f.Type, pkgs)
				return ft, err
			}
		}
		return "", fmt.Errorf("unknown field %s.%s", name, field)
	}
	return "", fmt.Errorf("unknown type %s", full)
}
