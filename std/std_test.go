package std_test

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ovid/internal/check"
	"ovid/internal/ir"
	"ovid/internal/module"
	"ovid/std"
)

// TestSyscallsNamed: every syscall in the shipped packages names its number
// with a SYS_ constant of its package. Only the shipped ovid/io may call
// syscall, so those constants are the whole list of system calls a program
// can make, and a seccomp allowlist built from them must not miss one
// (#102). The check goes by what the checker resolved the argument to, so
// a local or param named SYS_X, which shadows the const, does not count.
func TestSyscallsNamed(t *testing.T) {
	m, res := loadStd(t)
	target := map[string]string{} // expression id -> the declaration it names
	for _, u := range res.Uses {
		target[u.ID] = u.Target
	}
	n := 0
	for _, p := range m.Prog.Packages {
		if !m.Std[p.Path] {
			continue
		}
		for _, f := range p.Funcs {
			walk(f.Body, func(c *ir.Node) {
				if c.Op != "syscall" {
					return
				}
				n++
				if len(c.Args) > 0 {
					a := c.Args[0]
					if a.Op == "name" && strings.HasPrefix(a.Name, "SYS_") && target[a.ID] == "cn:"+p.Path+"."+a.Name {
						return
					}
				}
				t.Errorf("%s: in %s, syscall's number is not a SYS_ const of %s", where(m, c), f.Name, p.Path)
			})
		}
	}
	if n == 0 {
		t.Fatal("no syscall found in the shipped packages")
	}
}

// loadStd loads and checks a module that imports every shipped package, so
// they come from the toolchain as a program's would.
func loadStd(t *testing.T) (*module.Module, *check.Result) {
	t.Helper()
	var pkgs []string
	fs.WalkDir(std.FS, "ovid", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ov") && !strings.HasSuffix(p, "_test.ov") {
			pkgs = append(pkgs, path.Dir(p))
		}
		return nil
	})
	slices.Sort(pkgs)
	pkgs = slices.Compact(pkgs)
	if len(pkgs) == 0 {
		t.Fatal("no shipped packages")
	}
	dir := t.TempDir()
	var src strings.Builder
	src.WriteString("package app\n\n")
	for _, p := range pkgs {
		fmt.Fprintf(&src, "import %s\n", p)
	}
	src.WriteString("\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n")
	os.MkdirAll(filepath.Join(dir, "app"), 0o755)
	os.WriteFile(filepath.Join(dir, "ovid.mod"), []byte("module app\nentry app\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "app/main.ov"), []byte(src.String()), 0o644)
	m, err := module.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Errors) > 0 {
		t.Fatalf("loading the shipped packages: %v", m.Errors)
	}
	for _, p := range pkgs {
		if !m.Std[p] {
			t.Fatalf("%s was not loaded from the toolchain", p)
		}
	}
	res := check.Run(m.Prog)
	for _, is := range res.Issues {
		t.Errorf("checking the shipped packages: %s: %s", is.Code, is.Message)
	}
	if t.Failed() {
		t.FailNow()
	}
	return m, res
}

// where is n's file:line.
func where(m *module.Module, n *ir.Node) string {
	f := m.Files[n.Span.File]
	return fmt.Sprintf("%s:%d", f.Path, f.Pos(n.Span.Off).Line)
}

// walk calls f on every node under ns.
func walk(ns []*ir.Node, f func(*ir.Node)) {
	for _, n := range ns {
		if n == nil {
			continue
		}
		f(n)
		walk([]*ir.Node{n.Left, n.Right, n.Arg, n.Base, n.Addr, n.Val, n.Cond}, f)
		walk(n.Args, f)
		walk(n.Then, f)
		walk(n.Else, f)
		walk(n.Body, f)
	}
}
