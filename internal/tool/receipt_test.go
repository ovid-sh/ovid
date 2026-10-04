package tool

import (
	"bytes"
	"reflect"
	"testing"
)

const receiptMain = "package app\n\nimport ovid/io\n\nfunc A() i64 {\n  return 1\n}\n\nfunc main(io *ovid/io.Cap) i64 {\n  return A()\n}\n"

// opReceipt runs one op with --show and returns its receipt's ops[0] and
// the module's decl hashes afterwards, by id, from outline.
func opReceipt(t *testing.T, dir string, op EditOp) (map[string]any, map[string]string) {
	t.Helper()
	var b bytes.Buffer
	if code := runEdit(dir, &EditReq{Ops: []EditOp{op}}, EditOpts{Show: true}, &b); code != ExitOK {
		t.Fatalf("%s: %d %s", op.Op, code, b.String())
	}
	r := last(t, b.String())
	o := r["ops"].([]any)[0].(map[string]any)
	var ob bytes.Buffer
	Outline(dir, "app", false, false, Page{}, &ob)
	hs := map[string]string{}
	for _, d := range lines(t, ob.String()) {
		if id, ok := d["id"].(string); ok {
			hs[id] = d["hash"].(string)
		}
	}
	return o, hs
}

// wantDecls checks that a receipt op names exactly these decls, in order,
// each with its current hash and text.
func wantDecls(t *testing.T, o map[string]any, hs map[string]string, ids []string, texts []string) {
	t.Helper()
	ds, _ := o["decls"].([]any)
	var want []any
	for i, id := range ids {
		want = append(want, map[string]any{"id": id, "hash": hs[id], "text": texts[i]})
	}
	if !reflect.DeepEqual(ds, want) {
		t.Fatalf("decls %v\nwant  %v", ds, want)
	}
}

const (
	textX = "func X() i64 {\n  return 2\n}"
	textY = "// Y is three.\nfunc Y() i64 {\n  return 3\n}"
	textK = "const K i64 = 4"
)

// TestReceiptAppendSeveralDecls: appending several decls into a package
// reports every one of them, not only the one at the middle of the text.
func TestReceiptAppendSeveralDecls(t *testing.T) {
	for _, tc := range []struct {
		name  string
		texts []string
		ids   []string
	}{
		{"two", []string{textX, textY}, []string{"fn:app.X", "fn:app.Y"}},
		{"three", []string{textX, textY, textK}, []string{"fn:app.X", "fn:app.Y", "cn:app.K"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := mkmod(t, map[string]string{"ovid.mod": "module app\nentry app\n", "app/main.ov": receiptMain})
			text := ""
			for i, s := range tc.texts {
				if i > 0 {
					text += "\n\n"
				}
				text += s
			}
			o, hs := opReceipt(t, dir, EditOp{Op: "append", Into: "app", Text: text})
			if !reflect.DeepEqual(o["ids"], toAny(tc.ids)) {
				t.Fatalf("ids %v", o["ids"])
			}
			wantDecls(t, o, hs, tc.ids, tc.texts)
			if got := readFile(t, dir, "app/main.ov"); got != receiptMain+"\n"+text+"\n" {
				t.Fatalf("main.ov:\n%s", got)
			}
		})
	}
}

// TestReceiptInsertSeveralDecls: insert after a decl reports the decls it
// wrote and not the anchor.
func TestReceiptInsertSeveralDecls(t *testing.T) {
	dir := mkmod(t, map[string]string{"ovid.mod": "module app\nentry app\n", "app/main.ov": receiptMain})
	o, hs := opReceipt(t, dir, EditOp{Op: "insert", After: "fn:app.A", Text: textX + "\n\n" + textY, Expect: hashOf(t, dir, "fn:app.A")})
	wantDecls(t, o, hs, []string{"fn:app.X", "fn:app.Y"}, []string{textX, textY})
	// And before: the anchor still is not one of them.
	o, hs = opReceipt(t, dir, EditOp{Op: "insert", Before: "fn:app.A", Text: textK, Expect: hashOf(t, dir, "fn:app.A")})
	wantDecls(t, o, hs, []string{"cn:app.K"}, []string{textK})
}

// TestReceiptReplaceBySeveralDecls: a decl replaced by several reports
// each; a statement edit still reports its enclosing decl.
func TestReceiptReplaceBySeveralDecls(t *testing.T) {
	dir := mkmod(t, map[string]string{"ovid.mod": "module app\nentry app\n", "app/main.ov": receiptMain})
	newA := "func A() i64 {\n  return X()\n}"
	o, hs := opReceipt(t, dir, EditOp{Op: "replace", ID: "fn:app.A", Text: newA + "\n\n" + textX, Expect: hashOf(t, dir, "fn:app.A")})
	wantDecls(t, o, hs, []string{"fn:app.A", "fn:app.X"}, []string{newA, textX})

	o, hs = opReceipt(t, dir, EditOp{Op: "replace", ID: "st:app.X:1", Text: "return 5", Expect: hashOf(t, dir, "fn:app.X")})
	wantDecls(t, o, hs, []string{"fn:app.X"}, []string{"func X() i64 {\n  return 5\n}"})
	if !reflect.DeepEqual(o["ids"], []any{"st:app.X:1"}) {
		t.Fatalf("ids %v", o["ids"])
	}
}

func toAny(ss []string) []any {
	var out []any
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}
