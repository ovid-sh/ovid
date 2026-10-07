package tool

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// asyncSrc exercises the lowering of async funcs (POC A of #75): spawn and
// Join, await of a call that finishes at once, and awaits inside an if and
// a while, whose locals must survive the frame's round trips.
const asyncSrc = `package demo

import ovid/io
import ovid/async

async func add(a i64, b i64) i64 {
  var t i64 = spawn twice(a)
  var x i64 = await twice(b)
  var y i64 = await ovid/async.Join(t)
  if x > 0 {
    var z i64 = await twice(x)
    x = z
  }
  var i i64 = 0
  while i < 3 {
    var w i64 = await twice(1)
    x = x + w - 2
    i = i + 1
  }
  return x + y
}

async func twice(a i64) i64 {
  return a * 2
}

async func main(io *ovid/io.Cap) i64 {
  var r i64 = await add(3, 4)
  ovid/io.PrintInt(io, r)
  ovid/io.Print(strptr("\n"))
  return 0
}
`

// TestAsyncWasm builds asyncSrc for wasm and runs it under node, which any
// host can do; natively it would need Linux x86-64.
func TestAsyncWasm(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	dir := mkmod(t, map[string]string{"demo/main.ov": asyncSrc})
	mod := filepath.Join(t.TempDir(), "prog.wasm")
	var b bytes.Buffer
	if code := BuildTarget(dir, mod, "wasm", &b); code != 0 {
		t.Fatalf("build: %s", b.String())
	}
	out, err := exec.Command(node, filepath.Join(repo(t), "internal", "wasm", "run.mjs"), mod).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "22" {
		t.Fatalf("got %q, %v; want 22", out, err)
	}
}

// TestAsyncChecks: the checker's rules for await, spawn, and async calls.
func TestAsyncChecks(t *testing.T) {
	src := `package demo

import ovid/io
import ovid/async

async func f() i64 {
  return 1
}

func g() i64 {
  var a i64 = await f()
  return f()
}

async func h() i64 {
  var b i64 = 1 + await f()
  var c i64 = await ovid/io.CLen(0)
  return b + c
}

func main(io *ovid/io.Cap) i64 {
  return 0
}
`
	dir := mkmod(t, map[string]string{"demo/main.ov": src})
	var b bytes.Buffer
	Check(dir, false, &b)
	got := b.String()
	for _, want := range []string{
		`"code":"bad_await","message":"await is only valid in an async func"`,
		`"code":"async_call","message":"f is async; only await or spawn can call it"`,
		`"code":"bad_await","message":"await must be the whole value of a var, an assignment, or a statement"`,
		`"code":"bad_await","message":"CLen is not async; call it without await"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}
