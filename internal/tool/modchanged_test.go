package tool

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ovid/internal/module"
)

// writerMod is a module whose tests and whose main write the files named,
// by absolute path: plant creates one, and each test rewrites or removes
// what its name says.
func writerMod(t *testing.T, outside string) string {
	t.Helper()
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	lit := func(rel string) string {
		return fmt.Sprintf("%q", filepath.Join(dir, rel))
	}
	out := filepath.Join(outside, "notes.txt")
	src := `package demo

import ovid/io

func Put(io *ovid/io.Cap, path bytes) i64 {
  return ovid/io.WriteFile(io, path, "package demo\n", 420)
}

func TestA_Passes(io *ovid/io.Cap) i64 {
  return 0
}

func TestB_PlantsAFile(io *ovid/io.Cap) i64 {
  return Put(io, ` + lit("demo/planted.ov") + `)
}

func TestC_RewritesASource(io *ovid/io.Cap) i64 {
  return Put(io, ` + lit("demo/other.ov") + `)
}

func TestD_RewritesOvidMod(io *ovid/io.Cap) i64 {
  return ovid/io.WriteFile(io, ` + lit("ovid.mod") + `, "module demo\nentry demo\n\n", 420)
}

func TestE_RemovesASource(io *ovid/io.Cap) i64 {
  return ovid/io.Unlink(` + fmt.Sprintf("strptr(%q)", filepath.Join(dir, "demo/gone.ov")) + `)
}

func TestF_WritesElsewhere(io *ovid/io.Cap) i64 {
  return ovid/io.WriteFile(io, ` + fmt.Sprintf("%q", out) + `, "fine\n", 420)
}
`
	for rel, text := range map[string]string{
		"demo/writer_test.ov": src,
		"demo/other.ov":       "package demo\n\nconst Other i64 = 1\n",
		"demo/gone.ov":        "package demo\n\nconst Gone i64 = 2\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestTestReportsAChangedModule: a test can write files, its own module's
// among them (run unconfined here: confinement is what stops that). When the module on disk is no longer the one that was tested,
// the summary says so, names the files, and is not ok, though every test
// passed. Writing anywhere else is nobody's concern.
func TestTestReportsAChangedModule(t *testing.T) {
	needExec(t)
	t.Setenv(module.PathsEnv, "module")
	run := func(filter string) (map[string]any, int, string) {
		t.Helper()
		dir := writerMod(t, t.TempDir())
		var b bytes.Buffer
		code := TestWith(dir, TestOpts{Filter: filter, NoConfine: true}, &b)
		rs := lines(t, b.String())
		for _, r := range rs[:len(rs)-1] {
			if r["ok"] != true {
				t.Fatalf("--run %s: a test failed: %v", filter, r)
			}
		}
		return rs[len(rs)-1], code, dir
	}
	for filter, want := range map[string][]string{
		"PlantsAFile":     {"demo/planted.ov"},
		"RewritesASource": {"demo/other.ov"},
		"RewritesOvidMod": {"ovid.mod"},
		"RemovesASource":  {"demo/gone.ov"},
		"s":               {"demo/gone.ov", "demo/other.ov", "demo/planted.ov", "ovid.mod"}, // all six tests
		"Passes":          nil,
		"WritesElsewhere": nil,
	} {
		sum, code, _ := run(filter)
		if want == nil {
			if code != ExitOK || sum["ok"] != true || sum["module_changed"] != nil || sum["changed_files"] != nil {
				t.Fatalf("--run %s: exit %d %v", filter, code, sum)
			}
			continue
		}
		var got []string
		for _, f := range sum["changed_files"].([]any) {
			got = append(got, f.(string))
		}
		if code != ExitFail || sum["ok"] != false || sum["module_changed"] != true || !reflect.DeepEqual(got, want) || sum["failed"] != float64(0) {
			t.Fatalf("--run %s: exit %d, changed %v, want %v: %v", filter, code, got, want, sum)
		}
		before, _ := sum["revision_before"].(string)
		after, _ := sum["revision_after"].(string)
		if len(before) != 16 || len(after) != 16 || before == after {
			t.Fatalf("--run %s: revisions %q and %q", filter, before, after)
		}
	}
}

// TestRunReportsAChangedModule: run --json says the same of a program that
// writes into its own module. ok still means the program ran.
func TestRunReportsAChangedModule(t *testing.T) {
	needExec(t)
	t.Setenv(module.PathsEnv, "module")
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	p := filepath.Join(dir, "demo", "gen.ov")
	main := fmt.Sprintf("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  if ovid/io.Argc(io) > 1 {\n    return ovid/io.WriteFile(io, %q, \"package demo\\n\", 420)\n  }\n  return 0\n}\n", p)
	if err := os.WriteFile(filepath.Join(dir, "demo", "main.ov"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	// The module has a test file, which run does not load: the revisions it
	// reports are those of the program, before and after.
	if err := os.WriteFile(filepath.Join(dir, "demo", "main_test.ov"), []byte("package demo\nimport ovid/io\nfunc TestNothing(io *ovid/io.Cap) i64 {\n  return 0\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rev := func() string {
		t.Helper()
		m, err := module.LoadBuild(dir)
		if err != nil {
			t.Fatal(err)
		}
		return m.Revision()
	}
	before := rev()
	var b bytes.Buffer
	if code := RunWith(dir, nil, RunOpts{JSON: true, NoConfine: true}, &b); code != 0 || last(t, b.String())["module_changed"] != nil {
		t.Fatalf("a program that writes nothing: %s", b.String())
	}
	b.Reset()
	code := RunWith(dir, []string{"write"}, RunOpts{JSON: true, NoConfine: true}, &b)
	r := last(t, b.String())
	if code != 0 || r["ok"] != true || r["exit"] != float64(0) || r["module_changed"] != true || !reflect.DeepEqual(r["changed_files"], []any{"demo/gen.ov"}) {
		t.Fatalf("exit %d: %s", code, b.String())
	}
	if r["revision_before"] != before || r["revision_after"] != rev() || before == rev() {
		t.Fatalf("revisions %v -> %v, want the program's own %s -> %s", r["revision_before"], r["revision_after"], before, rev())
	}
}

// TestChangedModuleEdgeCases: two ways the module can stop being readable
// as it was. Neither may pass for "unchanged", and neither may answer with
// some other module's revision.
func TestChangedModuleEdgeCases(t *testing.T) {
	needExec(t)
	t.Setenv(module.PathsEnv, "module")
	summary := func(dir string) (map[string]any, int) {
		t.Helper()
		var b bytes.Buffer
		code := TestWith(dir, TestOpts{NoConfine: true}, &b)
		return last(t, b.String()), code
	}
	testFile := func(body string) string {
		return "package inner\nimport ovid/io\nfunc TestIt(io *ovid/io.Cap) i64 {\n" + body + "\n}\n"
	}

	// A module inside another deletes its own ovid.mod. Loading its
	// directory again would find the outer module.
	outer := mkmod(t, demo("package demo\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	inner := filepath.Join(outer, "nested")
	mod := filepath.Join(inner, "ovid.mod")
	for rel, text := range map[string]string{
		"ovid.mod":           "module inner\nentry inner\n",
		"inner/main.ov":      "package inner\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n",
		"inner/main_test.ov": testFile(fmt.Sprintf("  return ovid/io.Unlink(strptr(%q))", mod)),
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(inner, rel)), 0o755)
		if err := os.WriteFile(filepath.Join(inner, rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sum, code := summary(inner)
	if code != ExitFail || sum["module_changed"] != true || !reflect.DeepEqual(sum["changed_files"], []any{"ovid.mod"}) || sum["revision_after"] != nil || sum["revision_before"] == nil {
		t.Fatalf("a module that removed its ovid.mod: exit %d %v", code, sum)
	}

	// A test moves a directory that cannot be read into the module's view.
	// The walk fails partway; that is not the same as finding no change.
	if os.Getuid() == 0 {
		return // root reads any directory
	}
	dir := mkmod(t, map[string]string{"ovid.mod": "module inner\nentry inner\n",
		"inner/main.ov": "package inner\nimport ovid/io\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"})
	hidden, shown := filepath.Join(dir, ".hidden"), filepath.Join(dir, "zzz")
	os.MkdirAll(hidden, 0o755)
	os.WriteFile(filepath.Join(hidden, "x.ov"), []byte("package zzz\n"), 0o644)
	if err := os.WriteFile(filepath.Join(dir, "inner", "main_test.ov"),
		[]byte(testFile(fmt.Sprintf("  return ovid/io.Rename(strptr(%q), strptr(%q))", hidden, shown))), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(hidden, 0)
	t.Cleanup(func() { os.Chmod(hidden, 0o755); os.Chmod(shown, 0o755) })
	sum, code = summary(dir)
	if code != ExitFail || sum["ok"] != false || sum["module_changed"] != true || sum["scan_error"] == nil {
		t.Fatalf("a module with a directory that cannot be read: exit %d %v", code, sum)
	}
}
