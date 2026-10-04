package std_test

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"ovid/internal/ir"
	"ovid/internal/syntax"
	"ovid/std"
)

// TestSyscallsNamed: every syscall in the shipped packages names its number
// with a SYS_ constant of its package. Only the shipped ovid/io may call
// syscall, so those constants are the whole list of system calls a program
// can make, and a seccomp allowlist built from them must not miss one
// (#102). The files are parsed, not scanned, since a call may span lines.
func TestSyscallsNamed(t *testing.T) {
	n := 0
	err := fs.WalkDir(std.FS, "ovid", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ov") {
			return err
		}
		src, err := fs.ReadFile(std.FS, p)
		if err != nil {
			return err
		}
		pkg, perr := syntax.ParseFile(0, src)
		if perr != nil {
			t.Fatalf("%s: %v", p, perr)
		}
		consts := map[string]bool{}
		for _, c := range pkg.Consts {
			consts[c.Name] = true
		}
		for _, f := range pkg.Funcs {
			walk(f.Body, func(c *ir.Node) {
				if c.Op != "syscall" {
					return
				}
				n++
				num := "nothing"
				if len(c.Args) > 0 {
					a := c.Args[0]
					if a.Op == "name" && a.Pkg == "" && strings.HasPrefix(a.Name, "SYS_") && consts[a.Name] {
						return
					}
					num = string(src[a.Span.Off:a.Span.End])
				}
				line := 1 + bytes.Count(src[:c.Span.Off], []byte("\n"))
				t.Errorf("%s:%d: in %s, syscall's number is %s: name it with a SYS_ const of %s", p, line, f.Name, num, pkg.Path)
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no syscall found in the shipped packages")
	}
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
