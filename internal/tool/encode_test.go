package tool

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"runtime"
	"testing"

	"ovid/internal/ir"
	"ovid/internal/module"
)

// oddStrings is a module whose string literals hold what JSON must escape
// or may not, and a package with types and no funcs.
func oddStrings(t *testing.T) string {
	t.Helper()
	return mkmod(t, map[string]string{
		"demo/main.ov": "package demo\nimport ovid/io\nimport shapes\nfunc main(io *ovid/io.Cap) i64 {\n" +
			"  ovid/io.Print(strptr(\"<a href=\\\"x\\\">&amp;</a>\\n\\t\\\\ é \u2028 \x7f end\"))\n" +
			"  return sizeof(shapes.Point) + strlen(\"\")\n}\n",
		"shapes/shapes.ov": "package shapes\n\nconst Sides i64 = 4\n\ntype Point struct {\n  x i64\n  y i64\n}\n",
	})
}

// TestEncodeIsMarshal: the streaming encoder writes exactly the document
// Marshal builds. Marshal indents with encoding/json and Encode with its
// own indenter, and Encode splits the program at its last lists, so this
// also holds that those lists are still the last keys.
func TestEncodeIsMarshal(t *testing.T) {
	for _, dir := range []string{filepath.Join(repo(t), "prog"), filepath.Join(repo(t), "tests", "run", "split_package"), oddStrings(t)} {
		m, err := module.Load(dir)
		if err != nil || len(m.Errors) != 0 {
			t.Fatal(dir, err, m.Errors)
		}
		want, err := ir.Marshal(m.Prog)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := ir.Encode(&got, m.Prog); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			i := 0
			for i < got.Len() && i < len(want) && got.Bytes()[i] == want[i] {
				i++
			}
			lo, hi := max(0, i-60), i+60
			t.Fatalf("%s: Encode and Marshal differ at byte %d:\n%s\n---\n%s", dir, i, got.Bytes()[lo:min(hi, got.Len())], want[lo:min(hi, len(want))])
		}
		if !json.Valid(got.Bytes()) {
			t.Fatalf("%s: not JSON", dir)
		}
	}
	// A program without packages has no list to split at.
	for _, p := range []*ir.Program{{Module: "m", Entry: "m"}, {Module: "m", Packages: []ir.Package{}}, {Packages: []ir.Package{{ID: "pkg:a", Path: "a"}}}} {
		want, _ := ir.Marshal(p)
		var got bytes.Buffer
		if err := ir.Encode(&got, p); err != nil || !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("%v:\n%s\n---\n%s", err, got.Bytes(), want)
		}
	}
}

// TestDumpStringsSurvive: what a string literal holds comes back out of the
// dump, whatever JSON has to do to it on the way.
func TestDumpStringsSurvive(t *testing.T) {
	var b bytes.Buffer
	if code := Dump(oddStrings(t), "demo", "", &b); code != 0 {
		t.Fatal(b.String())
	}
	var found []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["op"] == "strptr" {
				found = append(found, x["value"].(string))
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	var doc any
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	walk(doc)
	if want := "<a href=\"x\">&amp;</a>\n\t\\ é \u2028 \x7f end"; len(found) != 1 || found[0] != want {
		t.Fatalf("got %q, want %q", found, want)
	}
	// <, >, and & are written as themselves, as the encoder is set to.
	if !bytes.Contains(b.Bytes(), []byte(`<a href=\"x\">&amp;</a>`)) {
		t.Fatalf("the literal's markup is escaped: %.300s", b.String())
	}
}

// TestEncodeDoesNotHoldTheDocument: encoding allocates on the order of the
// document, a func at a time. Marshal allocates several times that and
// holds the whole text, which is what made dump cost more than check.
func TestEncodeDoesNotHoldTheDocument(t *testing.T) {
	m, err := module.Load(filepath.Join(repo(t), "prog"))
	if err != nil {
		t.Fatal(err)
	}
	allocated := func(f func()) uint64 {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		f()
		runtime.ReadMemStats(&b)
		return b.TotalAlloc - a.TotalAlloc
	}
	cw := &countWriter{w: io.Discard}
	enc := allocated(func() { ir.Encode(cw, m.Prog) })
	mar := allocated(func() { ir.Marshal(m.Prog) })
	t.Logf("document %d KB; Encode allocates %d KB, Marshal %d KB", cw.n>>10, enc>>10, mar>>10)
	// Measured when written: 7.4 MB against Marshal's 56 MB, for 4.4 MB.
	if enc > 3*uint64(cw.n) || 3*enc > mar {
		t.Errorf("Encode allocated %d bytes for a document of %d, Marshal %d: want under three times the document and a third of Marshal", enc, cw.n, mar)
	}
}
