package ov

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"ovid/internal/compile"
)

func mustSrc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repositoryRoot(t), "src", "cli", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository sources")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func runFiles(t *testing.T, files map[string]string, args []string) (string, int) {
	t.Helper()
	prog, err := ParseProgram(files, "demo")
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

func TestSHA256ABC(t *testing.T) {
	out, code := runFiles(t, map[string]string{
		"ovid/io":  mustSrc(t, "ovid/io/io.ov"),
		"ovid/mem": mustSrc(t, "ovid/mem/mem.ov"),
		"ovid/sha": mustSrc(t, "ovid/sha/sha.ov"),
		"demo": `
package demo
import ovid/io
import ovid/sha
func main(io *ovid/io.Cap) i64 {
  var raw i64 = ovid/io.Alloc(io, 32)
  var hex i64 = ovid/io.Alloc(io, 80)
  ovid/sha.Sum(io, strptr("abc"), strlen("abc"), raw)
  ovid/sha.Hex(hex, raw)
  ovid/io.Stdout(hex, 64)
  ovid/io.Stdout(strptr("\n"), strlen("\n"))
  return 0
}
`,
	}, nil)
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\n"
	if code != 0 || out != want {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestAsmExit(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "ovid-exit.elf")
	_, code := runFiles(t, map[string]string{
		"ovid/io":  mustSrc(t, "ovid/io/io.ov"),
		"ovid/mem": mustSrc(t, "ovid/mem/mem.ov"),
		"ovid/asm": mustSrc(t, "ovid/asm/asm.ov"),
		"ovid/elf": mustSrc(t, "ovid/elf/elf.ov"),
		"demo": `
package demo
import ovid/io
import ovid/asm
import ovid/elf
func main(io *ovid/io.Cap) i64 {
  var c *ovid/asm.Code = ovid/asm.New(io)
  ovid/asm.MovRegImm(c, 0, 60)
  ovid/asm.MovRegImm(c, 7, 42)
  ovid/asm.Syscall(c)
  ovid/asm.Patch(c)
  var b *ovid/mem.Buf = ovid/elf.Link(io, c)
  var path i64 = ovid/io.Arg(io, 1)
  if ovid/io.WriteFile(io, path, ovid/io.CLen(path), b.data, b.len, 493) != 0 {
    return 8
  }
  return 0
}
`,
	}, []string{outPath})
	if code != 0 {
		t.Fatalf("emitter exit %d", code)
	}
	cmd := exec.Command(outPath)
	err := cmd.Run()
	got := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			got = ee.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if got != 42 {
		t.Fatalf("elf exit %d", got)
	}
}

func TestJSONParse(t *testing.T) {
	_, code := runFiles(t, map[string]string{
		"ovid/io":   mustSrc(t, "ovid/io/io.ov"),
		"ovid/mem":  mustSrc(t, "ovid/mem/mem.ov"),
		"ovid/json": mustSrc(t, "ovid/json/json.ov"),
		"demo": `
package demo
import ovid/io
import ovid/json
func main(io *ovid/io.Cap) i64 {
  var raw i64 = strptr("{\"a\":[1,true,null],\"b\":\"hi\\n\"}")
  var n i64 = strlen("{\"a\":[1,true,null],\"b\":\"hi\\n\"}")
  var root i64 = ovid/json.Parse(io, raw, n)
  if root == 0 {
    return 1
  }
  var a i64 = ovid/json.GetLit(root, strptr("a"), strlen("a"))
  if ovid/json.Int(ovid/json.At(a, 0)) != 1 {
    return 2
  }
  if ovid/json.Int(ovid/json.At(a, 1)) != 1 {
    return 3
  }
  if ovid/json.Kind(ovid/json.At(a, 2)) != 0 {
    return 4
  }
  var b i64 = ovid/json.GetLit(root, strptr("b"), strlen("b"))
  if ovid/json.StrN(b) != 3 {
    return 5
  }
  if load8(ovid/json.StrP(b) + 2) != 10 {
    return 6
  }
  return 0
}
`,
	}, nil)
	if code != 0 {
		t.Fatalf("json parse exit %d", code)
	}
}

func TestJSONEscapes(t *testing.T) {
	// "\u00e9\b\f" decodes to UTF-8 C3 A9, backspace, form feed.
	_, code := runFiles(t, map[string]string{
		"ovid/io":   mustSrc(t, "ovid/io/io.ov"),
		"ovid/mem":  mustSrc(t, "ovid/mem/mem.ov"),
		"ovid/json": mustSrc(t, "ovid/json/json.ov"),
		"demo": `
package demo
import ovid/io
import ovid/json
func main(io *ovid/io.Cap) i64 {
  var raw i64 = strptr("\x22\x5c\x75\x30\x30\x65\x39\x5c\x62\x5c\x66\x22")
  var n i64 = strlen("\x22\x5c\x75\x30\x30\x65\x39\x5c\x62\x5c\x66\x22")
  var root i64 = ovid/json.Parse(io, raw, n)
  if root == 0 {
    return 1
  }
  if ovid/json.StrN(root) != 4 {
    return 2
  }
  var p i64 = ovid/json.StrP(root)
  if load8(p) != 0xC3 {
    return 3
  }
  if load8(p + 1) != 0xA9 {
    return 4
  }
  if load8(p + 2) != 8 {
    return 5
  }
  if load8(p + 3) != 12 {
    return 6
  }
  return 0
}
`,
	}, nil)
	if code != 0 {
		t.Fatalf("json escapes exit %d", code)
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, code := runFiles(t, map[string]string{
		"ovid/io": mustSrc(t, "ovid/io/io.ov"),
		"demo": `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var path i64 = ovid/io.Arg(io, 1)
  var pp i64 = ovid/io.Alloc(io, 8)
  var nn i64 = ovid/io.Alloc(io, 8)
  if ovid/io.ReadFile(io, path, ovid/io.CLen(path), pp, nn) != 0 {
    return 9
  }
  return load64(nn)
}
`,
	}, []string{path})
	if code != 5 {
		t.Fatalf("len %d", code)
	}
}
