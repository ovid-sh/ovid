package ov

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ovid/internal/compile"
	"ovid/internal/tool"
)

const ioSrc = `
package ovid/io

type Cap struct {
  argc i64
  argv i64
  heap i64
  used i64
  size i64
}

const SYS_WRITE i64 = 1

func Write(fd i64, p i64, n i64) i64 {
  var off i64 = 0
  while off < n {
    var w i64 = syscall(SYS_WRITE, fd, p + off, n - off, 0, 0, 0)
    if w <= 0 {
      return -1
    }
    off = off + w
  }
  return 0
}

func Alloc(io *Cap, n i64) i64 {
  var align i64 = (n + 7) & ^7
  var u i64 = io.used
  if u + align > io.size {
    return 0
  }
  var p i64 = io.heap + u
  io.used = u + align
  return p
}
`

func runSrc(t *testing.T, demo string, args ...string) (string, int) {
	t.Helper()
	prog, err := ParseProgram(map[string]string{
		"ovid/io": ioSrc,
		"demo":    demo,
	}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := compile.Compile(prog)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "prog")
	if err := os.WriteFile(path, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run: %v\n%s", err, out)
		}
	}
	return string(out), code
}

func TestLangBasics(t *testing.T) {
	_, code := runSrc(t, `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  return (1 + 2) * 3 + 1
}
`)
	if code != 10 {
		t.Fatalf("arith %d", code)
	}
}

func TestShortCircuit(t *testing.T) {
	out, code := runSrc(t, `
package demo
import ovid/io
func Boom() i64 {
  return 1 / 0
}
func main(io *ovid/io.Cap) i64 {
  if false && Boom() == 1 {
    return 1
  }
  if true || Boom() == 1 {
    return 7
  }
  return 0
}
`)
	if code != 7 {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestHello(t *testing.T) {
	out, code := runSrc(t, `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  ovid/io.Write(1, strptr("hi\n"), strlen("hi\n"))
  return 0
}
`)
	if code != 0 || out != "hi\n" {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestAllocRoundtrip(t *testing.T) {
	_, code := runSrc(t, `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var p i64 = ovid/io.Alloc(io, 8)
  store64(p, 40)
  store64(p, load64(p) + 2)
  return load64(p)
}
`)
	if code != 42 {
		t.Fatalf("exit %d", code)
	}
}

func TestQueryPatchCheck(t *testing.T) {
	prog, err := ParseProgram(map[string]string{
		"ovid/io": ioSrc,
		"demo": `
package demo
import ovid/io
func Add(a i64, b i64) i64 {
  return a + b
}
func main(io *ovid/io.Cap) i64 {
  return Add(1, true)
}
`,
	}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := tool.WriteModule(dir, prog, ""); err != nil {
		t.Fatal(err)
	}
	var cerr bytesBuf
	if code := tool.Check(dir, &cerr); code == 0 {
		t.Fatalf("expected type error\n%s", cerr.String())
	}
	var buf bytesBuf
	if code := tool.Query(dir, "", "main", "demo", "func", &buf); code != 0 {
		t.Fatal(buf.String())
	}
	text := buf.String()
	if !strings.Contains(text, `"op": "bool"`) || !strings.Contains(text, `"revision"`) {
		t.Fatalf("query missing bool or revision: %s", text)
	}
	rev := extract(text, `"revision": "`, `"`)
	idx := strings.Index(text, `"op": "bool"`)
	if idx < 0 {
		t.Fatalf("no bool in %s", text)
	}
	idPos := strings.LastIndex(text[:idx], `"id": "`)
	boolID := extract(text[idPos:], `"id": "`, `"`)
	patch := `{"baseRevision":"` + rev + `","ops":[{"op":"replace","id":"` + boolID + `","node":{"id":"` + boolID + `","op":"int","value":2}}]}`
	patchPath := filepath.Join(dir, "fix.json")
	if err := os.WriteFile(patchPath, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	var pout bytesBuf
	if code := tool.Patch(dir, patchPath, &pout); code != 0 {
		t.Fatalf("patch %d %s boolID %s rev %s", code, pout.String(), boolID, rev)
	}
	var cout bytesBuf
	if code := tool.Check(dir, &cout); code != 0 {
		t.Fatalf("check after patch:\n%s", cout.String())
	}
	var stale bytesBuf
	if code := tool.Patch(dir, patchPath, &stale); code != 2 {
		t.Fatalf("stale code %d %s", code, stale.String())
	}
	if !strings.Contains(stale.String(), "stale_patch") {
		t.Fatalf("stale body %s", stale.String())
	}
	outPath := filepath.Join(dir, "demo.bin")
	var bout bytesBuf
	if code := tool.Build(dir, outPath, &bout); code != 0 {
		t.Fatalf("build %s", bout.String())
	}
	cmd := exec.Command(outPath)
	_, err = cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if code != 3 {
		t.Fatalf("fixed program exit %d", code)
	}
}

type bytesBuf struct{ b []byte }

func (b *bytesBuf) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}
func (b *bytesBuf) String() string { return string(b.b) }

func extract(s, a, end string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	rest := s[i+len(a):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
