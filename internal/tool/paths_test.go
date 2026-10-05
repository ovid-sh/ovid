package tool

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ovid/internal/module"
)

// TestModuleRelativePaths: with OVID_PATHS=module the records of a module
// are the same wherever ovid is run from, so two copies of one module (a
// sandbox's and its host's) can be compared. By default paths follow the
// working directory.
func TestModuleRelativePaths(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": "package demo\nimport ovid/io\nfunc Two() i64 {\n  return 2\n}\nfunc main(io *ovid/io.Cap) i64 {\n  return Two()\n}\n",
		"demo/bad.ov":  "package demo\nfunc Bad() i64 {\n  return true\n}\n",
	})
	// The working directory is reported with symlinks resolved (/var is one
	// on macOS), so name the module the same way for the default mode.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	// The records of four commands, less the lines that carry a duration.
	records := func(from string) string {
		t.Helper()
		if err := os.Chdir(from); err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		Check(dir, false, &b)
		Outline(dir, "demo", false, false, Page{}, &b)
		Grep(dir, "func Alloc", "", true, 0, GrepLimit, &b)
		Rename(dir, "Two", "Deux", true, &b)
		var keep []string
		for _, ln := range strings.Split(b.String(), "\n") {
			if !strings.Contains(ln, `"ms":`) {
				keep = append(keep, ln)
			}
		}
		return strings.Join(keep, "\n")
	}
	inner := filepath.Join(dir, "demo")

	t.Setenv(module.PathsEnv, "module")
	a, b := records(dir), records(inner)
	if a != b {
		t.Fatalf("records differ by working directory:\n%s\n---\n%s", a, b)
	}
	for _, want := range []string{`"file":"demo/bad.ov"`, `"file":"demo/main.ov"`, `"file":"std:ovid/io/io.ov"`, `"files":["demo/main.ov"]`} {
		if !strings.Contains(a, want) {
			t.Fatalf("no %s in:\n%s", want, a)
		}
	}

	t.Setenv(module.PathsEnv, "")
	if a, b := records(dir), records(inner); a == b || !strings.Contains(b, `"file":"bad.ov"`) {
		t.Fatalf("by default paths are relative to the working directory:\n%s", b)
	}
}
