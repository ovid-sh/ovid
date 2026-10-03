package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ovid/bootstrap/ov"
	"ovid/internal/ir"
	"ovid/internal/tool"
)

func importProgram(t *testing.T, value string) *ir.Program {
	t.Helper()
	p, err := ov.ParseProgram(map[string]string{"demo": "package demo\nfunc Value() i64 { return " + value + " }\n"}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImportPreservesAgentEditsUntilExplicitReplace(t *testing.T) {
	dir := t.TempDir()
	if err := writeImported(dir, importProgram(t, "1"), "", false); err != nil {
		t.Fatal(err)
	}
	if err := writeImported(dir, importProgram(t, "2"), "", false); err != nil {
		t.Fatal("ordinary source iteration:", err)
	}
	_, current, err := ir.ReadFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	value := current.Packages[0].Funcs[0].Body[0].Val
	patch := `{"baseRevision":"` + current.Revision + `","ops":[{"op":"replace","id":"` + value.ID + `","node":{"id":"` + value.ID + `","op":"int","value":42}}]}`
	patchFile := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(patchFile, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := tool.Patch(dir, patchFile, &out); code != 0 {
		t.Fatalf("patch: %s", out.String())
	}
	before, err := os.ReadFile(filepath.Join(dir, "ovid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeImported(dir, importProgram(t, "3"), "", false); err == nil || !strings.Contains(err.Error(), "canonical_changes") {
		t.Fatalf("must refuse overwritten agent edit: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "ovid.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("refused import changed canonical bytes")
	}
	if err := writeImported(dir, importProgram(t, "3"), "", true); err != nil {
		t.Fatal("explicit replacement:", err)
	}
	if err := writeImported(dir, importProgram(t, "4"), "", false); err != nil {
		t.Fatal("replacement did not update provenance:", err)
	}
}

func TestImportRefusesUntrackedExistingModule(t *testing.T) {
	dir := t.TempDir()
	if err := tool.WriteModule(dir, importProgram(t, "42"), ""); err != nil {
		t.Fatal(err)
	}
	if err := writeImported(dir, importProgram(t, "0"), "", false); err == nil {
		t.Fatal("existing canonical module has no source-import provenance")
	}
}

func TestImportPreservesDanglingCanonicalLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ovid.json")
	target := filepath.Join(dir, "missing.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := writeImported(dir, importProgram(t, "0"), "", false); err == nil {
		t.Fatal("an existing dangling link is not an empty workspace")
	}
	if got, err := os.Readlink(path); err != nil || got != target {
		t.Fatalf("existing link replaced: %q, %v", got, err)
	}
}
