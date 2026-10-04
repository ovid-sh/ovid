package asm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"ovid/internal/elf"
)

func runBin(t *testing.T, bin []byte, args ...string) (string, int) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("%s/%s cannot execute linux/amd64 binaries", runtime.GOOS, runtime.GOARCH)
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

func TestWriteRef(t *testing.T) {
	var b Buf
	b.MovRegImm64(RAX, 60)
	b.MovRegImm64(RDI, 42)
	b.Syscall()
	if err := b.PatchRel(); err != nil {
		t.Fatal(err)
	}
	bin := elf.Link(b.Code, nil, 0)
	if err := os.WriteFile("/tmp/ref.elf", bin, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestExit42(t *testing.T) {
	var b Buf
	// mov rax, 60; mov rdi, 42; syscall
	b.MovRegImm64(RAX, 60)
	b.MovRegImm64(RDI, 42)
	b.Syscall()
	if err := b.PatchRel(); err != nil {
		t.Fatal(err)
	}
	bin := elf.Link(b.Code, nil, 0)
	_, code := runBin(t, bin)
	if code != 42 {
		t.Fatalf("exit %d", code)
	}
}

func TestWriteHi(t *testing.T) {
	var b Buf
	// mov rax, 1; mov rdi, 1; mov rsi, str; mov rdx, 3; syscall; mov rax, 60; xor rdi,rdi; syscall
	b.MovRegImm64(RAX, 1)
	b.MovRegImm64(RDI, 1)
	b.MovRegImm64(RSI, 0)
	b.AbsImmLabel(0)
	b.MovRegImm64(RDX, 3)
	b.Syscall()
	b.MovRegImm64(RAX, 60)
	b.XorRaxRax()
	b.MovRegReg(RDI, RAX)
	b.MovRegImm64(RAX, 60)
	b.Syscall()
	if err := b.PatchRel(); err != nil {
		t.Fatal(err)
	}
	b.PatchAbs(elf.RodataVAddr(len(b.Code)))
	bin := elf.Link(b.Code, []byte("hi\n"), 0)
	out, code := runBin(t, bin)
	if code != 0 || out != "hi\n" {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestAddFrame(t *testing.T) {
	var b Buf
	// function that returns 40+2, called from _start-like exit
	fn := b.NewLabel()
	epi := b.NewLabel()
	b.Call(fn)
	b.MovRegReg(RDI, RAX)
	b.MovRegImm64(RAX, 60)
	b.Syscall()
	b.Mark(fn)
	b.PushReg(RBP)
	b.MovRegReg(RBP, RSP)
	b.SubRspImm(16)
	b.MovRegImm64(RAX, 40)
	b.MovMemRbpRax(-8)
	b.MovRegImm64(RAX, 2)
	b.MovRegReg(RCX, RAX)
	b.MovRaxMemRbp(-8)
	b.AddRaxRcx()
	b.Jmp(epi)
	b.Mark(epi)
	b.MovRegReg(RSP, RBP)
	b.PopReg(RBP)
	b.Ret()
	if err := b.PatchRel(); err != nil {
		t.Fatal(err)
	}
	bin := elf.Link(b.Code, nil, 0)
	_, code := runBin(t, bin)
	if code != 42 {
		t.Fatalf("exit %d", code)
	}
}
