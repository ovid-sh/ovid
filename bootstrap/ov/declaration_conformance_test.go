package ov

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"ovid/internal/ir"
)

func TestDeclarationConformance(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("Ovid targets Linux x86-64")
	}
	cases := []struct {
		name, code string
		mutate     func(*ir.Program)
	}{
		{"abi_order", "bad_abi", func(p *ir.Program) { f := p.Packages[0].Types[0].Fields; f[0], f[1] = f[1], f[0] }},
		{"abi_extra_field", "bad_abi", func(p *ir.Program) {
			ty := &p.Packages[0].Types[0]
			ty.Fields = append(ty.Fields, ir.Field{ID: "extra", Name: "extra", Type: "i64"})
		}},
		{"abi_missing_cap", "bad_abi", func(p *ir.Program) { p.Packages[0].Types = nil }},
		{"duplicate_package", "duplicate_package", func(p *ir.Program) { p.Packages = append(p.Packages, ir.Package{ID: "other_package", Path: "demo"}) }},
		{"duplicate_function", "duplicate_func", func(p *ir.Program) {
			p.Packages[1].Funcs = append(p.Packages[1].Funcs, ir.Func{ID: "other_func", Name: "Compute", Result: "i64", Body: []*ir.Node{{ID: "other_return", Op: "return", Val: &ir.Node{ID: "other_value", Op: "int", ValK: 1}}}})
		}},
		{"duplicate_type", "duplicate_type", func(p *ir.Program) { p.Packages[1].Types = []ir.TypeDecl{{ID: "t1", Name: "T"}, {ID: "t2", Name: "T"}} }},
		{"duplicate_field", "duplicate_field", func(p *ir.Program) {
			p.Packages[1].Types = []ir.TypeDecl{{ID: "t1", Name: "T", Fields: []ir.Field{{ID: "f1", Name: "x", Type: "i64"}, {ID: "f2", Name: "x", Type: "i64"}}}}
		}},
		{"duplicate_constant", "duplicate_const", func(p *ir.Program) {
			p.Packages[1].Consts = append(p.Packages[1].Consts, ir.Const{ID: "other_const", Name: "Answer", Type: "i64"})
		}},
		{"unknown_field_type", "bad_type", func(p *ir.Program) {
			p.Packages[1].Types = []ir.TypeDecl{{ID: "t1", Name: "T", Fields: []ir.Field{{ID: "f1", Name: "x", Type: "*demo.Missing"}}}}
		}},
		{"unknown_parameter_type", "bad_type", func(p *ir.Program) {
			p.Packages[1].Funcs[0].Params = []ir.Param{{ID: "p1", Name: "x", Type: "*demo.Missing"}}
		}},
		{"unknown_result_type", "bad_type", func(p *ir.Program) { p.Packages[1].Funcs[0].Result = "*demo.Missing" }},
		{"unknown_local_type", "bad_type", func(p *ir.Program) {
			fn := &p.Packages[1].Funcs[0]
			fn.Body = append([]*ir.Node{{ID: "local", Op: "var", Name: "x", Type: "*demo.Missing"}}, fn.Body...)
		}},
		{"unknown_cast_type", "bad_type", func(p *ir.Program) {
			p.Packages[1].Funcs[0].Body[0].Val = &ir.Node{ID: "cast", Op: "cast", Type: "*demo.Missing", Arg: &ir.Node{ID: "cast_arg", Op: "int", ValK: 1}}
		}},
	}
	for name, cli := range conformanceCLIs(t) {
		t.Run(name, func(t *testing.T) {
			for _, tt := range cases {
				t.Run(tt.name, func(t *testing.T) {
					dir := conformanceFixture(t, false)
					_, p, err := ir.ReadFile(dir)
					if err != nil {
						t.Fatal(err)
					}
					tt.mutate(p)
					p.Revision = ir.RevZeros
					raw, err := ir.Marshal(p)
					if err != nil {
						t.Fatal(err)
					}
					raw, err = ir.Stamp(raw)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "ovid.json"), raw, 0o644); err != nil {
						t.Fatal(err)
					}
					result := invokeCLI(t, cli, "check", dir)
					if result.code != 1 {
						t.Fatalf("invalid declaration accepted: %s", result)
					}
					found := false
					for _, f := range jsonFacts(t, result.out) {
						if f["fact"] == "error" && f["code"] == tt.code {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing %s error: %s", tt.code, result)
					}
				})
			}
		})
	}
}
