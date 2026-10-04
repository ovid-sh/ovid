package tool

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fileState is what a write to a file would change: its bytes, its inode
// (a rename puts a new one in place), and its mtime.
type fileState struct {
	src  string
	info os.FileInfo
}

// treeState records every file under dir, with mtimes set into the past so
// that any write shows.
func treeState(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	out := map[string]fileState{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if err := os.Chtimes(p, old, old); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fileState{string(b), info}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameTree fails unless dir holds exactly the files of before, each with
// the same bytes, inode, and mtime.
func sameTree(t *testing.T, dir string, before map[string]fileState) {
	t.Helper()
	var now []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			now = append(now, rel)
		}
		if err == nil && d.IsDir() && p != dir {
			if _, err := os.ReadDir(p); err == nil {
				rel, _ := filepath.Rel(dir, p)
				if !hasPrefixIn(before, rel+string(filepath.Separator)) {
					t.Errorf("new directory %s", rel)
				}
			}
		}
		return nil
	})
	var want []string
	for rel := range before {
		want = append(want, rel)
	}
	if !sameSet(now, want) {
		t.Fatalf("files now %v, want %v", now, want)
	}
	for rel, st := range before {
		p := filepath.Join(dir, rel)
		b, _ := os.ReadFile(p)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != st.src {
			t.Errorf("%s changed:\n%s", rel, b)
		}
		if !os.SameFile(info, st.info) {
			t.Errorf("%s was replaced (new inode)", rel)
		}
		if !info.ModTime().Equal(st.info.ModTime()) {
			t.Errorf("%s was written: mtime %v, was %v", rel, info.ModTime(), st.info.ModTime())
		}
	}
}

func hasPrefixIn(m map[string]fileState, prefix string) bool {
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	set := map[string]int{}
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		set[s]--
	}
	for _, n := range set {
		if n != 0 {
			return false
		}
	}
	return len(a) == len(b)
}

func moveManyFiles() map[string]string {
	return map[string]string{
		"app/main.ov":      "package app\n\nimport ovid/io\n\n// A is one.\nfunc A() i64 {\n  return 1\n}\n\nfunc B() i64 {\n  return A() + 1\n}\n\nfunc main(io *ovid/io.Cap) i64 {\n  return B()\n}\n",
		"app/util/util.ov": "package app/util\n\nfunc Z() i64 {\n  return 0\n}\n",
		"ovid.mod":         "module app\nentry app\n",
	}
}

// TestMoveManyDryRunTouchesNothing: a dry run of several moves is planned
// in memory; no file is written, replaced, created, or touched.
func TestMoveManyDryRunTouchesNothing(t *testing.T) {
	for _, to := range []string{"app/util", "app/fresh"} {
		dir := mkmod(t, moveManyFiles())
		before := treeState(t, dir)
		var b bytes.Buffer
		if code := MoveMany(dir, []string{"A", "B"}, to, "", true, &b); code != ExitOK {
			t.Fatalf("%s: dry run %d:\n%s", to, code, b.String())
		}
		rs := lines(t, b.String())
		if len(rs) != 3 {
			t.Fatalf("%s: want two move receipts and a summary:\n%s", to, b.String())
		}
		for _, r := range rs[:2] {
			if r["ok"] != true || r["written"] != false {
				t.Fatalf("%s: move receipt %v", to, r)
			}
		}
		if rs[1]["to"] != "fn:"+to+".B" || rs[0]["to"] != "fn:"+to+".A" {
			t.Fatalf("%s: receipts %v", to, rs)
		}
		if got := rs[2]; got["ok"] != true || got["written"] != false || !reflect.DeepEqual(got["moved"], []any{"A", "B"}) {
			t.Fatalf("%s: summary %v", to, got)
		}
		sameTree(t, dir, before)
	}
}

// TestMoveManyWritesOnce: a real multi-move writes the result of all the
// moves, and the revision its last receipt reports is the module's.
func TestMoveManyWritesOnce(t *testing.T) {
	dir := mkmod(t, moveManyFiles())
	var dry bytes.Buffer
	if code := MoveMany(dir, []string{"A", "B"}, "app/util", "", true, &dry); code != ExitOK {
		t.Fatalf("dry run %d:\n%s", code, dry.String())
	}
	var b bytes.Buffer
	if code := MoveMany(dir, []string{"A", "B"}, "app/util", "", false, &b); code != ExitOK {
		t.Fatalf("move %d:\n%s", code, b.String())
	}
	rs := lines(t, b.String())
	if len(rs) != 3 || rs[2]["written"] != true || rs[1]["written"] != true {
		t.Fatalf("receipts:\n%s", b.String())
	}
	// The dry run predicted exactly this.
	if dr := lines(t, dry.String()); dr[1]["revision"] != rs[1]["revision"] {
		t.Fatalf("dry run revision %v, real %v", dr[1]["revision"], rs[1]["revision"])
	}
	main, _ := os.ReadFile(filepath.Join(dir, "app/main.ov"))
	util, _ := os.ReadFile(filepath.Join(dir, "app/util/util.ov"))
	wantMain := "package app\n\nimport ovid/io\nimport app/util\n\nfunc main(io *ovid/io.Cap) i64 {\n  return app/util.B()\n}\n"
	wantUtil := "package app/util\n\nfunc Z() i64 {\n  return 0\n}\n\n// A is one.\nfunc A() i64 {\n  return 1\n}\n\nfunc B() i64 {\n  return A() + 1\n}\n"
	if string(main) != wantMain || string(util) != wantUtil {
		t.Fatalf("main.ov:\n%s\nutil.ov:\n%s", main, util)
	}
	var c bytes.Buffer
	if code := Check(dir, false, &c); code != ExitOK || last(t, c.String())["revision"] != rs[1]["revision"] {
		t.Fatalf("check %d:\n%s\nreceipt revision %v", code, c.String(), rs[1]["revision"])
	}
	// No temp file is left behind.
	ents, _ := os.ReadDir(filepath.Join(dir, "app"))
	for _, e := range ents {
		if strings.Contains(e.Name(), "ovid-tmp") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

// TestMoveManyRefusalTouchesNothing: when a later move is refused, the
// earlier ones were never written, so nothing needs putting back.
func TestMoveManyRefusalTouchesNothing(t *testing.T) {
	dir := mkmod(t, moveManyFiles())
	before := treeState(t, dir)
	var b bytes.Buffer
	if code := MoveMany(dir, []string{"A", "main"}, "app/fresh", "", false, &b); code == ExitOK {
		t.Fatalf("accepted:\n%s", b.String())
	}
	if r := last(t, b.String()); r["error"] != "rolled_back" || !reflect.DeepEqual(r["moved_before_failure"], []any{"A"}) {
		t.Fatalf("result %v", r)
	}
	for _, r := range lines(t, b.String()) {
		if r["ok"] == true {
			t.Fatalf("a receipt of a move that was not written: %v", r)
		}
	}
	sameTree(t, dir, before)
}

// TestMoveManyStale: a file changed behind the lock between the
// plan and the write is not overwritten.
func TestMoveManyStale(t *testing.T) {
	dir := mkmod(t, moveManyFiles())
	p := filepath.Join(dir, "app/util/util.ov")
	loaded := map[string][]byte{p: []byte("package app/util\n")}
	var b bytes.Buffer
	m, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if code := commitFiles(&b, m, loaded, map[string][]byte{p: []byte("x")}); code != ExitStale {
		t.Fatalf("commit %d %s", code, b.String())
	}
	if got, _ := os.ReadFile(p); string(got) != moveManyFiles()["app/util/util.ov"] {
		t.Fatalf("overwritten: %s", got)
	}
	// A file the plan creates must not have appeared meanwhile.
	b.Reset()
	if code := commitFiles(&b, m, map[string][]byte{}, map[string][]byte{p: []byte("x")}); code != ExitStale {
		t.Fatalf("commit over a new file %d %s", code, b.String())
	}
}
