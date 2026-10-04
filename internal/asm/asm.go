// Package asm is a tiny x86-64 encoder for the Ovid bootstrap and the shape
// the self-hosted compiler copies. Locals live in a frame; this package only
// appends instruction bytes and patches relative branches.
package asm

import "encoding/binary"

const (
	RAX = 0
	RCX = 1
	RDX = 2
	RBX = 3
	RSP = 4
	RBP = 5
	RSI = 6
	RDI = 7
	R8  = 8
	R9  = 9
	R10 = 10
	R11 = 11
	R12 = 12
	R13 = 13
	R14 = 14
	R15 = 15
)

// Buf is a code buffer with label fixups. Label positions are -1 until Mark.
type Buf struct {
	Code   []byte
	labels []int
	fixups []fixup
	abs    []absFix
}

type fixup struct {
	at    int // offset of rel32
	end   int // IP immediately after the instruction
	label int
}

type absFix struct {
	at    int // offset of imm64
	roOff int
}

func (b *Buf) NewLabel() int {
	id := len(b.labels)
	b.labels = append(b.labels, -1)
	return id
}

func (b *Buf) Mark(id int) {
	b.labels[id] = len(b.Code)
}

func (b *Buf) Pos() int { return len(b.Code) }

func (b *Buf) emit(bytes ...byte) { b.Code = append(b.Code, bytes...) }

func rex(w, r, x, regb bool) byte {
	v := byte(0x40)
	if w {
		v |= 0x08
	}
	if r {
		v |= 0x04
	}
	if x {
		v |= 0x02
	}
	if regb {
		v |= 0x01
	}
	return v
}

func modrm(mod, reg, rm byte) byte {
	return (mod << 6) | (reg << 3) | rm
}

// MovRegImm64 encodes mov r64, imm64.
func (b *Buf) MovRegImm64(reg int, imm int64) {
	b.emit(rex(true, false, false, reg >= 8))
	b.emit(0xB8 + byte(reg&7))
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(imm))
	b.emit(buf[:]...)
}

func (b *Buf) u32(v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	b.emit(buf[:]...)
}

// MovRegImm encodes mov reg, imm in its shortest form: a 32-bit move that
// zero-extends, a sign-extended imm32, or the full imm64.
func (b *Buf) MovRegImm(reg int, imm int64) {
	if imm >= 0 && imm <= 0xffffffff {
		if reg >= 8 {
			b.emit(0x41)
		}
		b.emit(0xB8 + byte(reg&7))
		b.u32(uint32(imm))
		return
	}
	if imm >= -0x80000000 && imm < 0 {
		b.emit(rex(true, false, false, reg >= 8), 0xC7, modrm(3, 0, byte(reg&7)))
		b.u32(uint32(imm))
		return
	}
	b.MovRegImm64(reg, imm)
}

// AluRegImm encodes op reg, imm for the group-1 operation with /digit n
// (0 add, 1 or, 4 and, 5 sub, 6 xor, 7 cmp); the imm is sign-extended.
func (b *Buf) AluRegImm(n, reg int, imm int32) {
	b.emit(rex(true, false, false, reg >= 8))
	if imm >= -128 && imm <= 127 {
		b.emit(0x83, modrm(3, byte(n), byte(reg&7)), byte(int8(imm)))
		return
	}
	b.emit(0x81, modrm(3, byte(n), byte(reg&7)))
	b.u32(uint32(imm))
}

// AluRegReg encodes op dst, src.
func (b *Buf) AluRegReg(n, dst, src int) {
	b.emit(rex(true, dst >= 8, false, src >= 8), byte(n*8+3), modrm(3, byte(dst&7), byte(src&7)))
}

// AluRegMem encodes op reg, [rbp+disp].
func (b *Buf) AluRegMem(n, reg int, disp int32) {
	b.movMemRbp(byte(n*8+3), reg, disp)
}

// AluMemReg encodes op [rbp+disp], reg.
func (b *Buf) AluMemReg(n int, disp int32, reg int) {
	b.movMemRbp(byte(n*8+1), reg, disp)
}

// AluMemImm encodes op qword [rbp+disp], imm.
func (b *Buf) AluMemImm(n int, disp int32, imm int32) {
	if imm >= -128 && imm <= 127 {
		b.movMemRbp(0x83, n, disp)
		b.emit(byte(int8(imm)))
		return
	}
	b.movMemRbp(0x81, n, disp)
	b.u32(uint32(imm))
}

// MovMemRbpImm encodes mov qword [rbp+disp], imm (sign-extended).
func (b *Buf) MovMemRbpImm(disp int32, imm int32) {
	b.movMemRbp(0xC7, 0, disp)
	b.u32(uint32(imm))
}

// ImulRaxImm encodes imul rax, rax, imm.
func (b *Buf) ImulRaxImm(imm int32) {
	if imm >= -128 && imm <= 127 {
		b.emit(0x48, 0x6B, 0xC0, byte(int8(imm)))
		return
	}
	b.emit(0x48, 0x69, 0xC0)
	b.u32(uint32(imm))
}

// ImulRaxReg encodes imul rax, reg.
func (b *Buf) ImulRaxReg(reg int) {
	b.emit(rex(true, false, false, reg >= 8), 0x0F, 0xAF, modrm(3, RAX, byte(reg&7)))
}

// ImulRaxMem encodes imul rax, [rbp+disp].
func (b *Buf) ImulRaxMem(disp int32) {
	b.emit(0x48, 0x0F, 0xAF)
	b.rbpOperand(RAX, disp)
}

// ShiftRaxImm encodes shl rax, n, or sar rax, n.
func (b *Buf) ShiftRaxImm(sar bool, n byte) {
	if sar {
		b.emit(0x48, 0xC1, 0xF8, n)
		return
	}
	b.emit(0x48, 0xC1, 0xE0, n)
}

// rexMem emits the REX prefix for an instruction with operands reg and
// [base+index+disp] (index -1 for none), when one is needed or forced.
func (b *Buf) rexMem(w bool, reg, base, index int, force bool) {
	v := rex(w, reg >= 8, index >= 8, base >= 8)
	if v != 0x40 || force {
		b.emit(v)
	}
}

// LoadMem loads rax from width bits at [base+index+disp], zero-extended.
// index is -1 for none.
func (b *Buf) LoadMem(width, base, index int, disp int32) {
	switch width {
	case 8:
		b.rexMem(true, RAX, base, index, false)
		b.emit(0x0F, 0xB6)
	case 32:
		b.rexMem(false, RAX, base, index, false)
		b.emit(0x8B)
	default:
		b.rexMem(true, RAX, base, index, false)
		b.emit(0x8B)
	}
	b.memOperand(RAX, base, index, disp)
}

// StoreMemReg stores the low width bits (8 or 64) of src at
// [base+index+disp].
func (b *Buf) StoreMemReg(width, src, base, index int, disp int32) {
	if width == 8 {
		// sil and dil are only named with a REX prefix.
		b.rexMem(false, src, base, index, src >= 4 && src < 8)
		b.emit(0x88)
	} else {
		b.rexMem(true, src, base, index, false)
		b.emit(0x89)
	}
	b.memOperand(src, base, index, disp)
}

// StoreMemImm stores imm, as a byte or sign-extended to 64 bits, at
// [base+index+disp].
func (b *Buf) StoreMemImm(width, base, index int, disp int32, imm int32) {
	if width == 8 {
		b.rexMem(false, 0, base, index, false)
		b.emit(0xC6)
		b.memOperand(0, base, index, disp)
		b.emit(byte(imm))
		return
	}
	b.rexMem(true, 0, base, index, false)
	b.emit(0xC7)
	b.memOperand(0, base, index, disp)
	b.u32(uint32(imm))
}

// Leave encodes leave: mov rsp, rbp; pop rbp.
func (b *Buf) Leave() { b.emit(0xC9) }

// AbsImmLabel records that the imm64 just written (8 bytes ending at Pos)
// should become the absolute virtual address of rodata[roOff].
func (b *Buf) AbsImmLabel(roOff int) {
	b.abs = append(b.abs, absFix{at: len(b.Code) - 8, roOff: roOff})
}

func (b *Buf) MovRegReg(dst, src int) {
	b.emit(rex(true, src >= 8, false, dst >= 8))
	b.emit(0x89)
	b.emit(modrm(3, byte(src&7), byte(dst&7)))
}

func (b *Buf) PushReg(reg int) {
	if reg >= 8 {
		b.emit(rex(false, false, false, true))
	}
	b.emit(0x50 + byte(reg&7))
}

func (b *Buf) PopReg(reg int) {
	if reg >= 8 {
		b.emit(rex(false, false, false, true))
	}
	b.emit(0x58 + byte(reg&7))
}

func (b *Buf) Ret() { b.emit(0xC3) }

func (b *Buf) Syscall() { b.emit(0x0F, 0x05) }

func (b *Buf) Cqo() { b.emit(0x48, 0x99) }

func (b *Buf) IdivRcx() { b.emit(0x48, 0xF7, 0xF9) }

func (b *Buf) NegRax() { b.emit(0x48, 0xF7, 0xD8) }

func (b *Buf) NotRax() { b.emit(0x48, 0xF7, 0xD0) }

func (b *Buf) XorRaxRax() { b.emit(0x48, 0x31, 0xC0) }

func (b *Buf) TestRaxRax() { b.emit(0x48, 0x85, 0xC0) }

func (b *Buf) CmpRaxRcx() { b.emit(0x48, 0x39, 0xC8) }

func (b *Buf) AddRaxRcx() { b.emit(0x48, 0x01, 0xC8) }

func (b *Buf) SubRaxRcx() { b.emit(0x48, 0x29, 0xC8) }

func (b *Buf) AndRaxRcx() { b.emit(0x48, 0x21, 0xC8) }

func (b *Buf) OrRaxRcx() { b.emit(0x48, 0x09, 0xC8) }

func (b *Buf) XorRaxRcx() { b.emit(0x48, 0x31, 0xC8) }

func (b *Buf) ImulRaxRcx() { b.emit(0x48, 0x0F, 0xAF, 0xC1) }

func (b *Buf) ShlRaxCl() { b.emit(0x48, 0xD3, 0xE0) }

func (b *Buf) SarRaxCl() { b.emit(0x48, 0xD3, 0xF8) }

// SetccAl uses a condition code: sete 0x94, setne 0x95, setl 0x9C,
// setge 0x9D, setle 0x9E, setg 0x9F.
func (b *Buf) SetccAl(cc byte) {
	b.emit(0x0F, cc, 0xC0)
}

func (b *Buf) MovzxRaxAl() { b.emit(0x48, 0x0F, 0xB6, 0xC0) }

func (b *Buf) MovzxRaxByteRcx() { b.emit(0x48, 0x0F, 0xB6, 0x01) }

func (b *Buf) MovEaxDwordRcx() { b.emit(0x8B, 0x01) }

func (b *Buf) MovRaxQwordRcx() { b.emit(0x48, 0x8B, 0x01) }

func (b *Buf) MovByteRcxAl() { b.emit(0x88, 0x01) }

func (b *Buf) MovQwordRcxRax() { b.emit(0x48, 0x89, 0x01) }

// MovRegMemRbp loads reg from [rbp+disp].
func (b *Buf) MovRegMemRbp(reg int, disp int32) {
	b.movMemRbp(0x8B, reg, disp)
}

// MovMemRbpReg stores reg to [rbp+disp].
func (b *Buf) MovMemRbpReg(reg int, disp int32) {
	b.movMemRbp(0x89, reg, disp)
}

// MovRaxMemRbp loads rax from [rbp+disp].
func (b *Buf) MovRaxMemRbp(disp int32) {
	b.MovRegMemRbp(RAX, disp)
}

// MovMemRbpRax stores rax to [rbp+disp].
func (b *Buf) MovMemRbpRax(disp int32) {
	b.movMemRbp(0x89, RAX, disp)
}

// MovRcxMemRbp loads rcx from [rbp+disp].
func (b *Buf) MovRcxMemRbp(disp int32) {
	b.movMemRbp(0x8B, RCX, disp)
}

func (b *Buf) movMemRbp(op byte, reg int, disp int32) {
	b.emit(rex(true, reg >= 8, false, false))
	b.emit(op)
	b.rbpOperand(reg, disp)
}

// rbpOperand encodes the ModRM bytes for reg, [rbp+disp].
func (b *Buf) rbpOperand(reg int, disp int32) {
	if disp >= -128 && disp <= 127 {
		b.emit(modrm(1, byte(reg&7), RBP))
		b.emit(byte(int8(disp)))
		return
	}
	b.emit(modrm(2, byte(reg&7), RBP))
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(disp))
	b.emit(buf[:]...)
}

// MovRaxMemRegDisp loads rax from [base+disp]. base must be RAX or RCX.
func (b *Buf) MovRaxMemRegDisp(base int, disp int32) {
	b.memReg(0x8B, RAX, base, disp, true)
}

// MovMemRegDispRax stores rax to [base+disp].
func (b *Buf) MovMemRegDispRax(base int, disp int32) {
	b.memReg(0x89, RAX, base, disp, true)
}

func (b *Buf) memReg(op byte, reg, base int, disp int32, wide bool) {
	b.emit(rex(wide, reg >= 8, false, base >= 8))
	b.emit(op)
	b.memOperand(reg, base, -1, disp)
}

// memOperand encodes the ModRM, SIB, and displacement bytes for reg,
// [base+index+disp]; index is -1 for none and is never rsp.
func (b *Buf) memOperand(reg, base, index int, disp int32) {
	rb := byte(base & 7)
	rg := byte(reg & 7)
	// rsp and r12 as a base, and any index, need a SIB byte.
	sib := index >= 0 || rb == RSP
	rm := rb
	if sib {
		rm = 4
	}
	// rbp and r13 as a base have no form without a displacement.
	mod := byte(2)
	if disp == 0 && rb != RBP {
		mod = 0
	} else if disp >= -128 && disp <= 127 {
		mod = 1
	}
	b.emit(modrm(mod, rg, rm))
	if sib {
		x := byte(4)
		if index >= 0 {
			x = byte(index & 7)
		}
		b.emit(x<<3 | rb)
	}
	if mod == 1 {
		b.emit(byte(int8(disp)))
	} else if mod == 2 {
		b.u32(uint32(disp))
	}
}

func (b *Buf) SubRspImm(n int32) {
	if n == 0 {
		return
	}
	if n >= -128 && n <= 127 {
		b.emit(0x48, 0x83, 0xEC, byte(int8(n)))
		return
	}
	b.emit(0x48, 0x81, 0xEC)
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(n))
	b.emit(buf[:]...)
}

func (b *Buf) AndRspAlign() {
	// and rsp, -16
	b.emit(0x48, 0x83, 0xE4, 0xF0)
}

func (b *Buf) LeaR13RspPlus8() {
	// lea r13, [rsp+8]
	b.emit(0x4C, 0x8D, 0x6C, 0x24, 0x08)
}

func (b *Buf) MovR12MemRsp() {
	// mov r12, [rsp]
	b.emit(0x4C, 0x8B, 0x24, 0x24)
}

func (b *Buf) rel32(op []byte, label int) {
	b.emit(op...)
	at := len(b.Code)
	b.emit(0, 0, 0, 0)
	b.fixups = append(b.fixups, fixup{at: at, end: len(b.Code), label: label})
}

func (b *Buf) Jmp(label int) { b.rel32([]byte{0xE9}, label) }

func (b *Buf) Call(label int) { b.rel32([]byte{0xE8}, label) }

// Jcc emits 0F cc rel32. cc is the second opcode (0x84 = jz, 0x85 = jnz, 0x8C = jl).
func (b *Buf) Jcc(cc byte, label int) {
	b.rel32([]byte{0x0F, cc}, label)
}

func (b *Buf) Jz(label int)  { b.Jcc(0x84, label) }
func (b *Buf) Jnz(label int) { b.Jcc(0x85, label) }
func (b *Buf) Jl(label int)  { b.Jcc(0x8C, label) }

// PatchRel resolves branch fixups. Call after every label is marked.
func (b *Buf) PatchRel() error {
	for _, f := range b.fixups {
		target := b.labels[f.label]
		if target < 0 {
			return errUnset(f.label)
		}
		rel := int32(target - f.end)
		binary.LittleEndian.PutUint32(b.Code[f.at:f.at+4], uint32(rel))
	}
	return nil
}

type unsetErr int

func errUnset(label int) error { return unsetErr(label) }

func (e unsetErr) Error() string { return "unset label" }

// PatchAbs writes the absolute address of each rodata reference, given
// where rodata is mapped.
func (b *Buf) PatchAbs(rodataVAddr uint64) {
	base := rodataVAddr
	for _, f := range b.abs {
		addr := base + uint64(f.roOff)
		binary.LittleEndian.PutUint64(b.Code[f.at:f.at+8], addr)
	}
}

// AbsFixups exposes rodata fixups for tests.
func (b *Buf) AbsCount() int { return len(b.abs) }
