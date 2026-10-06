package asm

import (
	"bytes"
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

// A scaled index, against objdump's reading of the same bytes.
func TestScaledIndex(t *testing.T) {
	var b Buf
	b.LoadMem(64, RBX, R12|3<<4, 0)          // mov rax, [rbx+r12*8]
	b.LoadMem(8, R13, RSI|1<<4, -120)        // movzx rax, byte [r13+rsi*2-0x78]
	b.LoadMem(32, RAX, R9|2<<4, 4096)        // mov eax, [rax+r9*4+0x1000]
	b.StoreMemReg(64, R10, R15, R14|3<<4, 8) // mov [r15+r14*8+0x8], r10
	b.StoreMemReg(8, RSI, RDI, RBX|3<<4, 0)  // mov [rdi+rbx*8], sil
	b.StoreMemImm(64, RAX, R8|3<<4, 16, -1)  // mov qword [rax+r8*8+0x10], -1
	b.StoreMemImm(8, R12, RCX, 0, 7)         // mov byte [r12+rcx*1], 7
	b.LoadMem(64, RSP, -1, 8)                // mov rax, [rsp+0x8]
	want := []byte{
		0x4a, 0x8b, 0x04, 0xe3,
		0x49, 0x0f, 0xb6, 0x44, 0x75, 0x88,
		0x42, 0x8b, 0x84, 0x88, 0x00, 0x10, 0x00, 0x00,
		0x4f, 0x89, 0x54, 0xf7, 0x08,
		0x40, 0x88, 0x34, 0xdf,
		0x4a, 0xc7, 0x44, 0xc0, 0x10, 0xff, 0xff, 0xff, 0xff,
		0x41, 0xc6, 0x04, 0x0c, 0x07,
		0x48, 0x8b, 0x44, 0x24, 0x08,
	}
	if !bytes.Equal(b.Code, want) {
		t.Fatalf("got % x\nwant % x", b.Code, want)
	}
}
