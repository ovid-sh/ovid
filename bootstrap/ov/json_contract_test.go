package ov

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ovid/internal/compile"
)

// Agent tools exchange arbitrary JSON strings and signed i64 values. Reject
// ambiguous input instead of accepting a different program from the bootstrap.
func TestJSONProtocolBoundaries(t *testing.T) {
	source := `package demo
import ovid/io
import ovid/json
import ovid/mem
func main(io *ovid/io.Cap) i64 {
  var text i64 = ovid/io.Arg(io, 1)
  var v i64 = ovid/json.Parse(io, text, ovid/io.CLen(text))
  if v == 0 { return 7 }
  var b *ovid/mem.Buf = ovid/mem.New(io, 128)
  if ovid/json.Kind(v) == 3 {
    ovid/json.Quote(io, b, ovid/json.StrP(v), ovid/json.StrN(v))
    ovid/io.Stdout(b.data, b.len)
  }
  if ovid/json.Kind(v) == 2 {
    var p i64 = ovid/io.Alloc(io, 80)
    var n i64 = ovid/mem.FormatI64(p, ovid/json.Int(v))
    ovid/io.Stdout(p, n)
  }
  return 0
}
`
	program, err := ParseProgram(map[string]string{
		"demo":      source,
		"ovid/io":   mustSrc(t, "ovid/io/io.ov"),
		"ovid/mem":  mustSrc(t, "ovid/mem/mem.ov"),
		"ovid/json": mustSrc(t, "ovid/json/json.ov"),
	}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := compile.Compile(program)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "json")
	if err := os.WriteFile(path, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		input  string
		output string
		valid  bool
	}{
		{`9223372036854775807`, `9223372036854775807`, true},
		{`-9223372036854775808`, `-9223372036854775808`, true},
		{`-0`, `0`, true},
		{`"\ud83d\ude00"`, `"😀"`, true},
		{`"\u0000\t\r\n\b\f\"\\"`, `"\u0000\u0009\u000d\u000a\u0008\u000c\"\\"`, true},
		{" [1,\n2, -3] \n", "", true},
		{`{"a":null}`, "", true},
		{`9223372036854775808`, "", false},
		{`-9223372036854775809`, "", false},
		{`[1 2]`, "", false},
		{`[- 1]`, "", false},
		{`[01]`, "", false},
		{`[1.2]`, "", false},
		{`{} {}`, "", false},
		{`{"a":1,"a":2}`, "", false},
		{`{"a":null,"a":2}`, "", false},
		{"\"unescaped\nnewline\"", "", false},
		{`"\ud83d"`, "", false},
		{`"\ude00"`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			out, err := exec.Command(path, tc.input).CombinedOutput()
			if tc.valid {
				if err != nil || string(out) != tc.output {
					t.Fatalf("got %q, %v; want %q", out, err, tc.output)
				}
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
				t.Fatalf("invalid input must be rejected, got %q, %v", out, err)
			}
		})
	}
}
