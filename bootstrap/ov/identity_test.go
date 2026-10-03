package ov

import (
	"bytes"
	"testing"

	"ovid/internal/ir"
)

func identityProgram(t *testing.T, source string) *ir.Program {
	t.Helper()
	program, err := ParseProgram(map[string]string{"demo": "package demo\n" + source}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func preserve(t *testing.T, next, previous *ir.Program) {
	t.Helper()
	before, err := ir.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreserveIDs(next, previous); err != nil {
		t.Fatal(err)
	}
	after, err := ir.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("reconciliation mutated the previous canonical program")
	}
	seen := map[string]bool{}
	doc, err := identityDocument(next)
	if err != nil {
		t.Fatal(err)
	}
	visitIdentityObjects(doc, func(obj map[string]any) {
		if id, ok := obj["id"].(string); ok {
			if id == "" || seen[id] {
				t.Errorf("empty or reused ID %q", id)
			}
			seen[id] = true
		}
	})
}

func TestPreserveIDsInsertionReorderingAndLiteralEdit(t *testing.T) {
	old := identityProgram(t, `
func Compute(a i64) i64 {
  var x i64 = 10
  var y i64 = 20
  return a + x + y
}
`)
	priorBytes, _ := ir.Marshal(old)
	oldFn := old.Packages[0].Funcs[0]
	x, y, ret := oldFn.Body[0], oldFn.Body[1], oldFn.Body[2]
	next := identityProgram(t, `
func Compute(a i64) i64 {
  var z i64 = 99
  var y i64 = 20
  var x i64 = 11
  return a + x + y
}
`)
	preserve(t, next, old)
	fn := next.Packages[0].Funcs[0]
	if fn.ID != oldFn.ID || fn.Params[0].ID != oldFn.Params[0].ID {
		t.Fatal("declaration identity changed")
	}
	if fn.Body[1].ID != y.ID || fn.Body[1].Val.ID != y.Val.ID {
		t.Fatal("reordered unchanged statement lost its identity")
	}
	if fn.Body[2].ID != x.ID || fn.Body[2].Val.ID != x.Val.ID || fn.Body[2].Val.Int != 11 {
		t.Fatal("literal edit did not preserve the statement and expression identities")
	}
	if fn.Body[3].ID != ret.ID || fn.Body[3].Val.ID != ret.Val.ID {
		t.Fatal("insertion shifted existing IDs")
	}
	if bytes.Contains(priorBytes, []byte(fn.Body[0].ID)) || bytes.Contains(priorBytes, []byte(fn.Body[0].Val.ID)) {
		t.Fatal("inserted statement recycled an old ID")
	}
}

func TestPreserveIDsUnambiguousFunctionRename(t *testing.T) {
	old := identityProgram(t, `
func Compute(a i64) i64 { return a + 1 }
func Other() i64 { return 9 }
`)
	next := identityProgram(t, `
func Other() i64 { return 9 }
func Calculate(a i64) i64 { return a + 1 }
`)
	preserve(t, next, old)
	before, after := old.Packages[0].Funcs[0], next.Packages[0].Funcs[1]
	if after.ID != before.ID || after.Params[0].ID != before.Params[0].ID || after.Body[0].Val.ID != before.Body[0].Val.ID {
		t.Fatal("unique unchanged function rename lost subtree identities")
	}
	if after.Name != "Calculate" {
		t.Fatal("reconciliation reverted the rename")
	}
}

func TestPreserveIDsAmbiguousRenameDoesNotGuess(t *testing.T) {
	old := identityProgram(t, `
func A() i64 { return 1 }
func B() i64 { return 1 }
`)
	next := identityProgram(t, `
func C() i64 { return 1 }
func D() i64 { return 1 }
`)
	preserve(t, next, old)
	oldIDs := map[string]bool{}
	for _, fn := range old.Packages[0].Funcs {
		oldIDs[fn.ID] = true
		oldIDs[fn.Body[0].ID] = true
		oldIDs[fn.Body[0].Val.ID] = true
	}
	for _, fn := range next.Packages[0].Funcs {
		if oldIDs[fn.ID] || oldIDs[fn.Body[0].ID] || oldIDs[fn.Body[0].Val.ID] {
			t.Fatal("ambiguous rename guessed an old identity")
		}
	}
}

func TestPreserveIDsAmbiguousStatementsDoNotGuess(t *testing.T) {
	old := identityProgram(t, `
func F() i64 {
  return 1
  return 1
}
`)
	next := identityProgram(t, `
func F() i64 { return 1 }
`)
	preserve(t, next, old)
	got := next.Packages[0].Funcs[0].Body[0]
	for _, previous := range old.Packages[0].Funcs[0].Body {
		if got.ID == previous.ID || got.Val.ID == previous.Val.ID {
			t.Fatal("ambiguous deletion assigned a positional identity")
		}
	}
}

func TestPreserveIDsNoOpKeepsRepeatedNodesAndLargeIntegers(t *testing.T) {
	const source = `
const Big i64 = 9223372036854775807
func F() i64 {
  return 9223372036854775807
  return 9223372036854775807
}
`
	old, next := identityProgram(t, source), identityProgram(t, source)
	preserve(t, next, old)
	want, _ := ir.Marshal(old)
	got, _ := ir.Marshal(next)
	if !bytes.Equal(want, got) {
		t.Fatalf("no-op import changed identities or integer values:\n%s", got)
	}
}

func TestPreserveIDsNewObjectsDoNotRecycleDeletedPositions(t *testing.T) {
	old := identityProgram(t, `
func F() i64 {
  var removed i64 = 10
  return 7
}
`)
	middle := identityProgram(t, `
func F() i64 { return 7 }
`)
	preserve(t, middle, old)
	next := identityProgram(t, `
func F() i64 {
  var created i64 = 10
  return 7
}
`)
	preserve(t, next, middle)
	removed, created := old.Packages[0].Funcs[0].Body[0], next.Packages[0].Funcs[0].Body[0]
	if removed.ID == created.ID || removed.Val.ID == created.Val.ID {
		t.Fatal("later import recycled the deleted statement's parser position")
	}
	if next.Packages[0].Funcs[0].Body[1].ID != old.Packages[0].Funcs[0].Body[1].ID {
		t.Fatal("surviving statement lost its identity")
	}
}

func TestPreserveIDsDeclarationEditsAndLiteralTypeRepair(t *testing.T) {
	old := identityProgram(t, `
const N i64 = 1
type Pair struct {
  first i64
  second i64
}
func F() i64 { return true }
`)
	next := identityProgram(t, `
const N i64 = 2
type Pair struct {
  inserted i64
  second i64
  first i64
}
func F() i64 { return 42 }
`)
	preserve(t, next, old)
	a, b := old.Packages[0], next.Packages[0]
	if b.Consts[0].ID != a.Consts[0].ID || b.Consts[0].Value != 2 || b.Types[0].ID != a.Types[0].ID {
		t.Fatal("declaration modification lost identity or new content")
	}
	if b.Types[0].Fields[1].ID != a.Types[0].Fields[1].ID || b.Types[0].Fields[2].ID != a.Types[0].Fields[0].ID {
		t.Fatal("field insertion or reordering lost identity")
	}
	if b.Funcs[0].Body[0].Val.ID != a.Funcs[0].Body[0].Val.ID || b.Funcs[0].Body[0].Val.Op != "int" {
		t.Fatal("scalar type repair lost the expression identity or new type")
	}
}
