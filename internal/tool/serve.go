package tool

import (
	"fmt"
	"strings"

	"ovid/internal/ir"
	"ovid/internal/module"
	"ovid/internal/syntax"
)

// serveSrc is the stdio host the toolchain writes for an entry package
// that has handle and no main. Its main joins the entry package itself, so
// it cannot collide with a package of the module's, and the imports it
// needs, ovid/io and ovid/http, are ones handle's signature already made
// the package declare.
const serveSrc = `package %s

func main(io *ovid/io.Cap) i64 {
  var req *ovid/http.Request = ovid/http.ReadStdio(io)
  var res *ovid/http.Response = ovid/http.NewResponse(io)
  if ovid/http.Err(req) != 0 {
    return ovid/http.WriteStdio(io, req, res, 0)
  }
  return ovid/http.WriteStdio(io, req, res, handle(io, req, res))
}
`

// serveProgram returns p with the stdio host's main added to the entry
// package, after its own funcs, when that package has handle and no main;
// otherwise p itself.
func serveProgram(p *ir.Program) *ir.Program {
	at := -1
	for i := range p.Packages {
		if p.Packages[i].Path == p.Entry {
			at = i
		}
	}
	if at < 0 || hasFunc(&p.Packages[at], "main") || !hasFunc(&p.Packages[at], "handle") {
		return p
	}
	host, err := syntax.ParseFile(-1, []byte(fmt.Sprintf(serveSrc, p.Entry)))
	if err != nil {
		panic("serve host: " + err.Msg)
	}
	q := *p
	q.Packages = append([]ir.Package{}, p.Packages...)
	entry := &q.Packages[at]
	entry.Funcs = append(append([]ir.Func{}, entry.Funcs...), host.Funcs...)
	return &q
}

// servedHandle reports whether t is the handle the stdio host calls: a
// func handle in the entry package, which has no main.
func servedHandle(m *module.Module, t *module.Loc) bool {
	if t.Kind != "func" || t.Pkg != m.Entry || !strings.HasSuffix(t.ID, ".handle") {
		return false
	}
	pkg := findPkg(m, m.Entry)
	return pkg != nil && !hasFunc(pkg, "main")
}

func hasFunc(pkg *ir.Package, name string) bool {
	for i := range pkg.Funcs {
		if pkg.Funcs[i].Name == name {
			return true
		}
	}
	return false
}
