package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ovid/bootstrap/ov"
	"ovid/internal/ir"
	"ovid/internal/tool"
)

func main() {
	root := flag.String("root", "", "directory of package trees")
	out := flag.String("out", "", "module directory to write")
	entry := flag.String("entry", "", "entry package path")
	replace := flag.Bool("replace", false, "explicitly replace canonical changes made since the last source import")
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
	if err := writeImported(*out, prog, projection.String(), *replace); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// BOOTSTRAP.json records only the last imported canonical revision. It is not
// a compiler input. An agent patch deliberately leaves it unchanged, so source
// authoring can never silently overwrite canonical edits on the next import.
type importState struct {
	GeneratedRevision string `json:"generatedRevision"`
}

func writeImported(dir string, next *ir.Program, projection string, replace bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lock, err := tool.LockModule(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	file, previous, readErr := ir.ReadFile(dir)
	if readErr == nil {
		stored, revision, hashErr := ir.Hash(file)
		var state importState
		meta, metaErr := os.ReadFile(filepath.Join(dir, "BOOTSTRAP.json"))
		if metaErr == nil {
			metaErr = json.Unmarshal(meta, &state)
		}
		if !replace && (hashErr != nil || stored != revision || metaErr != nil || state.GeneratedRevision != revision) {
			return fmt.Errorf("canonical_changes: %s/ovid.json differs from the last source import; keep editing the canonical program, or pass -replace to explicitly discard those changes", dir)
		}
	} else if !replace {
		_, pathErr := os.Lstat(filepath.Join(dir, "ovid.json"))
		if pathErr == nil || !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("cannot read existing canonical program: %w; -replace explicitly discards it", readErr)
		}
		if !errors.Is(pathErr, os.ErrNotExist) {
			return pathErr
		}
	}
	if err := ov.PreserveIDs(next, previous); err != nil {
		return err
	}
	if err := tool.WriteModuleLocked(dir, next, projection); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(importState{GeneratedRevision: next.Revision}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".bootstrap-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(meta, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "BOOTSTRAP.json"))
}
