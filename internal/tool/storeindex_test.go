package tool

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestStoreScaledIndex: a store of a computed value through a base that is
// not a local, plus a scaled index, keeps the index in the address instead
// of computing i * 8 first. TestSelfHost holds prog/ to the same bytes.
func TestStoreScaledIndex(t *testing.T) {
	dir := mkmod(t, demo(`package demo
import ovid/io
type R struct {
  data i64
}
func F(r *R, n i64, v i64) i64 {
  var i i64 = 0
  while i < n {
    store64(r.data + (i * 8), (v * 3) - 1)
    i = i + 1
  }
  return 0
}
func main(io *ovid/io.Cap) i64 {
  var r *R = ovid/io.Alloc(io, 8) as *R
  r.data = ovid/io.Alloc(io, 80)
  return F(r, 10, 5)
}
`))
	out := filepath.Join(t.TempDir(), "out")
	if code := Build(dir, out, io.Discard); code != 0 {
		t.Fatalf("build exits %d", code)
	}
	bin, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// mov [rcx+r8*8], rax: i lives in r8 and the base comes through rcx.
	if !bytes.Contains(bin, []byte{0x4a, 0x89, 0x04, 0xc1}) {
		t.Errorf("no mov [rcx+r8*8], rax")
	}
	// imul rax, rax, 8: the index computed apart from the address.
	if bytes.Contains(bin, []byte{0x48, 0x6b, 0xc0, 0x08}) {
		t.Errorf("i * 8 is computed apart from the store")
	}
}
