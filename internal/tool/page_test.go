package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pageMod is a module with 25 funcs, each called once from main.
func pageMod(t *testing.T) string {
	t.Helper()
	var src, calls strings.Builder
	src.WriteString("package demo\nimport ovid/io\nfunc One() i64 {\n  return 1\n}\n")
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&src, "func F%02d() i64 {\n  return One()\n}\n", i)
		fmt.Fprintf(&calls, "  n = n + F%02d()\n", i)
	}
	src.WriteString("func main(io *ovid/io.Cap) i64 {\n  var n i64 = 0\n" + calls.String() + "  return n\n}\n")
	return mkmod(t, demo(src.String()))
}

// TestPagedCommands: outline, refs, and grep print one page and say where
// the next starts. Following next_offset visits every record exactly once,
// and the pages together equal the unpaged result.
func TestPagedCommands(t *testing.T) {
	dir := pageMod(t)
	cmds := map[string]func(Page, *bytes.Buffer) int{
		"outline": func(p Page, b *bytes.Buffer) int { return Outline(dir, "demo", false, false, false, true, p, b) },
		"refs":    func(p Page, b *bytes.Buffer) int { return Refs(dir, "One", "", true, p, b) },
		"grep":    func(p Page, b *bytes.Buffer) int { return Grep(dir, `return`, "", false, true, p.Offset, p.Limit, b) },
	}
	for name, run := range cmds {
		var all bytes.Buffer
		if code := run(Page{}, &all); code != 0 {
			t.Fatalf("%s: %s", name, all.String())
		}
		whole := lines(t, all.String())
		sum, want := whole[len(whole)-1], whole[:len(whole)-1]
		if sum["total"] != float64(len(want)) || sum["count"] != float64(len(want)) || sum["has_more"] != false || sum["next_offset"] != nil || len(want) < 20 {
			t.Fatalf("%s unpaged: %d records, last line %v", name, len(want), sum)
		}
		var got []map[string]any
		pages, offset := 0, 0
		for {
			var b bytes.Buffer
			run(Page{Offset: offset, Limit: 7}, &b)
			rs := lines(t, b.String())
			last := rs[len(rs)-1]
			got = append(got, rs[:len(rs)-1]...)
			pages++
			if last["total"] != sum["total"] || last["offset"] != float64(offset) || last["count"] != float64(len(rs)-1) || last["revision"] != sum["revision"] || last["revision"] == nil {
				t.Fatalf("%s page at %d: %v", name, offset, last)
			}
			if last["has_more"] != true {
				break
			}
			offset = int(last["next_offset"].(float64))
			if pages > 20 {
				t.Fatalf("%s does not end", name)
			}
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if pages < 3 || !bytes.Equal(a, b) {
			t.Fatalf("%s: %d pages give %d records, want the %d of the unpaged run in order", name, pages, len(got), len(want))
		}
	}
	// An offset past the end is an empty page, not an error.
	var b bytes.Buffer
	if code := Refs(dir, "One", "", true, Page{Offset: 1000, Limit: 5}, &b); code != 0 {
		t.Fatal(b.String())
	}
	if rs := lines(t, b.String()); len(rs) != 1 || rs[0]["count"] != float64(0) || rs[0]["total"] != float64(24) || rs[0]["has_more"] != false {
		t.Fatalf("%v", rs)
	}
	// refs' summary of where the uses are covers all of them, not the page.
	b.Reset()
	Refs(dir, "One", "", true, Page{Limit: 2}, &b)
	if r := last(t, b.String()); r["by_pkg"].(map[string]any)["demo"] != float64(24) || r["count"] != float64(2) {
		t.Fatalf("%v", r)
	}
}

// TestDumpOptions: dump is one document and is not paged; --pkg limits it
// to a package and -o sends it to a file, leaving a one-line receipt.
func TestDumpOptions(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": "package demo\nimport ovid/io\nimport util\nfunc main(io *ovid/io.Cap) i64 {\n  return util.Two()\n}\n",
		"util/util.ov": "package util\nfunc Two() i64 {\n  return 2\n}\n",
	})
	paths := func(raw []byte) []string {
		t.Helper()
		var d struct {
			Revision string
			Packages []struct{ Path string }
		}
		if err := json.Unmarshal(raw, &d); err != nil || d.Revision == "" {
			t.Fatalf("not a dump: %v: %.200s", err, raw)
		}
		var out []string
		for _, p := range d.Packages {
			out = append(out, p.Path)
		}
		return out
	}
	var whole, one bytes.Buffer
	if Dump(dir, "", "", &whole) != 0 || Dump(dir, "util", "", &one) != 0 {
		t.Fatal(whole.String(), one.String())
	}
	if got := paths(whole.Bytes()); len(got) < 3 {
		t.Fatalf("whole dump has packages %v", got)
	}
	if got := paths(one.Bytes()); len(got) != 1 || got[0] != "util" || one.Len() >= whole.Len() {
		t.Fatalf("--pkg util gave %v, %d bytes of %d", got, one.Len(), whole.Len())
	}
	var b bytes.Buffer
	if code := Dump(dir, "nope", "", &b); code != ExitFail || last(t, b.String())["error"] != "not_found" {
		t.Fatalf("%d %s", code, b.String())
	}
	out := filepath.Join(t.TempDir(), "d.json")
	b.Reset()
	if code := Dump(dir, "", out, &b); code != 0 {
		t.Fatal(b.String())
	}
	raw, _ := os.ReadFile(out)
	if r := last(t, b.String()); !bytes.Equal(raw, whole.Bytes()) || r["bytes"] != float64(len(raw)) || r["output"] != out || strings.Count(b.String(), "\n") != 1 {
		t.Fatalf("receipt %s for a file of %d bytes", b.String(), len(raw))
	}
	// A destination that cannot be synced is still written: a character
	// device takes the bytes and has nothing to flush.
	if _, err := os.Stat(os.DevNull); err == nil {
		b.Reset()
		if code := Dump(dir, "", os.DevNull, &b); code != 0 || last(t, b.String())["bytes"] != float64(whole.Len()) {
			t.Fatalf("dump -o %s: exit %d %s", os.DevNull, code, b.String())
		}
	}
}
