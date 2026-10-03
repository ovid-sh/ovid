package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ovid/bootstrap/ov"
	"ovid/internal/tool"
)

func main() {
	root := flag.String("root", "", "directory of package trees")
	out := flag.String("out", "", "module directory to write")
	entry := flag.String("entry", "", "entry package path")
	flag.Parse()
	if *root == "" || *out == "" || *entry == "" {
		fmt.Fprintln(os.Stderr, "usage: front -root src -out prog -entry ovid/cli")
		os.Exit(1)
	}
	files := map[string]string{}
	err := filepath.Walk(*root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".ov") {
			return nil
		}
		rel, err := filepath.Rel(*root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		if pkg == "." {
			return fmt.Errorf("%s is not inside a package directory", path)
		}
		if _, ok := files[pkg]; ok {
			return fmt.Errorf("two .ov files in %s", pkg)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[pkg] = string(b)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	prog, err := ov.ParseProgram(files, *entry)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var projection strings.Builder
	projection.WriteString("// Ovid projection. Canonical program is ovid.json.\n")
	projection.WriteString("// entry " + *entry + "\n\n")
	for _, pkg := range prog.Packages {
		projection.WriteString("// ----- ")
		projection.WriteString(pkg.Path)
		projection.WriteString(" -----\n\n")
		projection.WriteString(files[pkg.Path])
		if !strings.HasSuffix(files[pkg.Path], "\n") {
			projection.WriteByte('\n')
		}
		projection.WriteByte('\n')
	}
	if err := tool.WriteModule(*out, prog, projection.String()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
