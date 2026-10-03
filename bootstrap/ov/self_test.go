package ov

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/tool"
)

func compilerFiles(t *testing.T, demo string) map[string]string {
	t.Helper()
	return map[string]string{
		"ovid/io":   mustSrc(t, "ovid/io/io.ov"),
		"ovid/mem":  mustSrc(t, "ovid/mem/mem.ov"),
		"ovid/json": mustSrc(t, "ovid/json/json.ov"),
		"ovid/asm":  mustSrc(t, "ovid/asm/asm.ov"),
		"ovid/elf":  mustSrc(t, "ovid/elf/elf.ov"),
		"ovid/cg":   mustSrc(t, "ovid/cg/cg.ov"),
		"demo":      demo,
	}
}

const demoCompiler = `
package demo
import ovid/io
import ovid/json
import ovid/cg
func main(io *ovid/io.Cap) i64 {
  var path i64 = ovid/io.Arg(io, 1)
  var pp i64 = ovid/io.Alloc(io, 8)
  var nn i64 = ovid/io.Alloc(io, 8)
  if ovid/io.ReadFile(io, path, ovid/io.CLen(path), pp, nn) != 0 {
    return 9
  }
  var root i64 = ovid/json.Parse(io, load64(pp), load64(nn))
  if root == 0 {
    return 8
  }
  var errp i64 = ovid/io.Alloc(io, 8)
  store64(errp, 0)
  var b *ovid/mem.Buf = ovid/cg.Compile(io, root, errp)
  if load64(errp) != 0 {
    return 20 + load64(errp)
  }
  var out i64 = ovid/io.Arg(io, 2)
  if ovid/io.WriteFile(io, out, ovid/io.CLen(out), b.data, b.len, 493) != 0 {
    return 7
  }
  return 0
}
`

func TestSelfHostCodegen(t *testing.T) {
	prog, err := ParseProgram(compilerFiles(t, demoCompiler), "demo")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := tool.WriteModule(dir, prog, "projection\n"); err != nil {
		t.Fatal(err)
	}
	var chk bytes.Buffer
	if code := tool.Check(dir, &chk); code != 0 {
		t.Fatalf("compiler does not check:\n%s", chk.String())
	}
	bin, err := compile.Compile(prog)
	if err != nil {
		t.Fatal(err)
	}
	comp := filepath.Join(dir, "cg")
	if err := os.WriteFile(comp, bin, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		src  string
		args []string
		out  string
		code int
	}{
		{
			name: "ret",
			src: `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  return 42
}
`,
			code: 42,
		},
		{
			name: "add",
			src: `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  return (40 + 2)
}
`,
			code: 42,
		},
		{
			name: "if",
			src: `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var x i64 = 1
  if x == 1 {
    return 7
  }
  return 8
}
`,
			code: 7,
		},
		{
			name: "loop",
			src: `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var i i64 = 0
  var s i64 = 0
  while i < 10 {
    s = s + i
    i = i + 1
  }
  return s
}
`,
			code: 45,
		},
		{
			name: "call",
			src: `
package demo
import ovid/io
func Add(a i64, b i64) i64 {
  return a + b
}
func main(io *ovid/io.Cap) i64 {
  return Add(20, 22)
}
`,
			code: 42,
		},
		{
			name: "write",
			src: `
package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  if ovid/io.Stdout(strptr("hi\n"), strlen("hi\n")) != 0 {
    return 4
  }
  return 0
}
`,
			out:  "hi\n",
			code: 0,
		},
		{
			name: "short",
			src: `
package demo
import ovid/io
func Boom() i64 {
  return 1 / 0
}
func main(io *ovid/io.Cap) i64 {
  var n i64 = 0
  if false && (Boom() == 1) {
    n = 1
  }
  if true || (Boom() == 1) {
    n = n + 7
  }
  return n
}
`,
			code: 7,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guest, err := ParseProgram(map[string]string{
				"ovid/io": mustSrc(t, "ovid/io/io.ov"),
				"demo":    tc.src,
			}, "demo")
			if err != nil {
				t.Fatal(err)
			}
			guest.Revision = ir.RevZeros
			raw, err := ir.Marshal(guest)
			if err != nil {
				t.Fatal(err)
			}
			in := filepath.Join(dir, tc.name+".json")
			out := filepath.Join(dir, tc.name+".elf")
			if err := os.WriteFile(in, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(comp, in, out)
			msg, err := cmd.CombinedOutput()
			if err != nil {
				code := -1
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				}
				t.Fatalf("compiler exit %d %s", code, msg)
			}
			run := exec.Command(out, tc.args...)
			got, err := run.CombinedOutput()
			code := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.code || string(got) != tc.out {
				t.Fatalf("code %d out %q", code, got)
			}
		})
	}
}
