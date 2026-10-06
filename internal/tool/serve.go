package tool

import (
	"fmt"
	"sort"
	"strings"

	"ovid/internal/ir"
	"ovid/internal/module"
	"ovid/internal/syntax"
)

// servePkg is the package that holds the stdio host the toolchain writes
// for an entry package that has handle and no main.
const servePkg = "ovid/servemain"

const serveSrc = `package ovid/servemain

import ovid/io
import ovid/http
import %[1]s

func main(io *ovid/io.Cap) i64 {
  var req *ovid/http.Request = ovid/http.ReadStdio(io)
  var res *ovid/http.Response = ovid/http.NewResponse(io)
  if ovid/http.Err(req) != 0 {
    return ovid/http.WriteStdio(io, req, res, 0)
  }
  return ovid/http.WriteStdio(io, req, res, %[1]s.handle(io, req, res))
}
`

// serveProgram returns p with the stdio host as its entry when the entry
// package has handle and no main; otherwise p itself.
func serveProgram(p *ir.Program) *ir.Program {
	var entry *ir.Package
	for i := range p.Packages {
		if p.Packages[i].Path == p.Entry {
			entry = &p.Packages[i]
		}
	}
	if entry == nil || hasFunc(entry, "main") || !hasFunc(entry, "handle") {
		return p
	}
	pkg, err := syntax.ParseFile(-1, []byte(fmt.Sprintf(serveSrc, p.Entry)))
	if err != nil {
		panic("serve host: " + err.Msg)
	}
	// In path order, as the packages are, so that both compilers lay the
	// program out the same.
	q := *p
	q.Entry = servePkg
	at := sort.Search(len(p.Packages), func(i int) bool { return p.Packages[i].Path >= servePkg })
	q.Packages = append(append(append([]ir.Package{}, p.Packages[:at]...), *pkg), p.Packages[at:]...)
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
