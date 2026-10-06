package module

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
)

// allAtOnce is the digest as it was first computed: every node of the
// module in one pass, the count of identical texts kept per decl.
func allAtOnce(m *Module) map[*Loc]string {
	out := map[*Loc]string{}
	type key struct {
		decl *Loc
		k    string
	}
	seen := map[key]int{}
	for _, o := range m.Locs() {
		text := m.Text(o.Full)
		in := text
		if o.Kind == "stmt" || o.Kind == "expr" {
			k := key{o.decl, o.Kind + "\x00" + o.Decl + "\x00" + text}
			in = fmt.Sprintf("%s\x00%d\x00%s", k.k, seen[k], out[o.decl])
			seen[k]++
		}
		sum := sha256.Sum256([]byte(in))
		out[o] = hex.EncodeToString(sum[:])
	}
	return out
}

// TestDigestPerDeclMatchesAllAtOnce: computing a decl's digests on demand
// gives every node the value the one-pass computation gave it, in whatever
// order the nodes are asked for, so hashes agents hold stay valid.
func TestDigestPerDeclMatchesAllAtOnce(t *testing.T) {
	for _, dir := range []string{filepath.Join("..", "..", "prog"), filepath.Join("..", "..", "tests", "run", "split_package")} {
		m, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := allAtOnce(m)
		locs := m.Locs()
		// Ask from the end, so a statement's digest is wanted before its
		// decl's and before the nodes ahead of it.
		for i := len(locs) - 1; i >= 0; i-- {
			if got := m.LocDigest(locs[i]); got != want[locs[i]] {
				t.Fatalf("%s: %s has %s, the one-pass computation %s", dir, locs[i].ID, got, want[locs[i]])
			}
		}
		if len(m.hashes) != len(want) {
			t.Fatalf("%s: %d digests computed for %d nodes", dir, len(m.hashes), len(want))
		}
	}
}

// TestDigestOfOneDeclTouchesOneDecl: asking for one func's digests computes
// that func's nodes and no other's.
func TestDigestOfOneDeclTouchesOneDecl(t *testing.T) {
	m, err := Load(filepath.Join("..", "..", "prog"))
	if err != nil {
		t.Fatal(err)
	}
	l := m.Index()["fn:ovid/sha.Add"]
	if l == nil {
		t.Fatal("no fn:ovid/sha.Add")
	}
	var first *Loc
	for _, o := range m.Locs() {
		if o.decl == l && o.Kind == "stmt" {
			first = o
			break
		}
	}
	m.LocDigest(first)
	for o := range m.hashes {
		if o != l && o.decl != l {
			t.Fatalf("hashing a statement of %s also hashed %s", l.ID, o.ID)
		}
	}
	if n := len(m.hashes); n < 3 || n > 40 {
		t.Fatalf("%d digests for one small func", n)
	}
}

// TestReleaseKeepsWhatNamesTheModule: after Release the module still says
// where it is and what its files are called, and holds no tree and none of
// the caches built from it.
func TestReleaseKeepsWhatNamesTheModule(t *testing.T) {
	m, err := Load(filepath.Join("..", "..", "tests", "run", "split_package"))
	if err != nil {
		t.Fatal(err)
	}
	root, rev, path := m.Root, m.Revision(), m.DisplayPath(m.Files[0])
	// Build every cache: the id index and its companions, and a statement's
	// digest, which fills hashes and byDecl.
	var stmt *Loc
	for _, l := range m.Locs() {
		if l.Kind == "stmt" {
			stmt = l
			break
		}
	}
	if stmt == nil {
		t.Fatal("no statement in the module")
	}
	m.LocDigest(stmt)
	if m.Prog == nil || m.index == nil || m.order == nil || m.locs == nil || m.copies == nil ||
		m.byNode == nil || m.hashes == nil || m.byDecl == nil {
		t.Fatal("a cache was not built before Release")
	}
	m.Release()
	if m.Prog != nil || m.index != nil || m.order != nil || m.locs != nil || m.copies != nil ||
		m.byNode != nil || m.hashes != nil || m.byDecl != nil {
		t.Fatal("the tree or a cache survived Release")
	}
	if m.Root != root || m.Revision() != rev || m.DisplayPath(m.Files[0]) != path {
		t.Fatal("Release changed what names the module")
	}
}
