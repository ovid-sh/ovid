package tool

import (
	"ovid/internal/async"
	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/wasm"
)

// compileAll is compile.CompileAll after async funcs are lowered to plain
// IR (internal/async); a program without them compiles as before.
func compileAll(p *ir.Program) (*compile.Output, error) {
	q, err := async.Lower(p)
	if err != nil {
		return nil, err
	}
	return compile.CompileAll(q)
}

// wasmCompile is wasm.Compile after async funcs are lowered.
func wasmCompile(p *ir.Program) ([]byte, error) {
	q, err := async.Lower(p)
	if err != nil {
		return nil, err
	}
	return wasm.Compile(q)
}
