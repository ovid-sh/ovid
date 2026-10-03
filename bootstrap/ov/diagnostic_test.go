package ov

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ovid/internal/ir"
)

func TestCLITypeDiagnostics(t *testing.T) {
	clis := conformanceCLIs(t)
	cases := []struct {
		name, body, expected, actual, op string
		count                            int
	}{
		{"return", "return true", "i64", "bool", "bool", 1},
		{"initializer", "var x i64 = true\nreturn x", "i64", "bool", "bool", 1},
		{"assignment", "var x i64 = 0\nx = Flag()\nreturn x", "i64", "bool", "call", 1},
		{"nested_argument", "return Add(1, Flag())", "i64", "bool", "call", 1},
		{"both_operands", "return true + false", "i64", "bool", "bool", 2},
		{"condition", "if 1 { return 2 }\nreturn 3", "bool", "i64", "int", 1},
		{"comparison", "return (true == 1) as i64", "bool", "i64", "int", 1},
		{"unary", "return -true", "i64", "bool", "bool", 1},
		{"memory_load", "return load64(true)", "i64", "bool", "bool", 1},
		{"memory_store", "store64(true, false)\nreturn 0", "i64", "bool", "bool", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseProgram(map[string]string{
				"ovid/io": ioSrc,
				"demo":    "package demo\nimport ovid/io\nfunc Flag() bool { return true }\nfunc Add(a i64, b i64) i64 { return a + b }\nfunc main(io *ovid/io.Cap) i64 {\n" + tc.body + "\n}\n",
			}, "demo")
			if err != nil {
				t.Fatal(err)
			}
			p.Revision = ir.RevZeros
			raw, err := ir.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = ir.Stamp(raw)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "ovid.json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			var previous []map[string]any
			for name, cli := range clis {
				result := invokeCLI(t, cli, "check", dir)
				if result.code != 1 {
					t.Fatalf("%s must reject ill-typed program: %s", name, result)
				}
				var diagnostics []map[string]any
				for _, fact := range jsonFacts(t, result.out) {
					if fact["fact"] != "error" {
						continue
					}
					if fact["code"] != "type_mismatch" || fact["expected"] != tc.expected || fact["actual"] != tc.actual {
						t.Fatalf("%s unexpected diagnostic: %#v", name, fact)
					}
					q := queryCLI(t, cli, dir, "--id", fact["id"].(string))
					if len(q.Matches) != 1 || q.Matches[0].Node["op"] != tc.op {
						t.Fatalf("%s diagnosis targets wrong expression: %#v", name, q)
					}
					diagnostics = append(diagnostics, fact)
				}
				if len(diagnostics) != tc.count {
					t.Fatalf("%s got %d diagnostics, want %d", name, len(diagnostics), tc.count)
				}
				if previous != nil && !reflect.DeepEqual(previous, diagnostics) {
					t.Fatalf("diagnostics disagree: %#v vs %#v", previous, diagnostics)
				}
				previous = diagnostics
			}
		})
	}
}
