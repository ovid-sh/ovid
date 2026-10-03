// Package elf writes a single-segment static x86-64 executable.
// The image is loaded at 0x400000 and is not dynamically linked.
package elf

import "encoding/binary"

const (
	LoadAddr   = 0x400000
	HeaderSize = 64 + 56 // ELF header + one program header
)

// Link packs code and rodata into a static ET_EXEC. entryOff is the offset
// of the entry point within code. code must already have absolute addresses
// patched for a code virtual address of LoadAddr+HeaderSize.
func Link(code, rodata []byte, entryOff int) []byte {
	filesz := HeaderSize + len(code) + len(rodata)
	out := make([]byte, filesz)
	// e_ident
	out[0] = 0x7f
	out[1] = 'E'
	out[2] = 'L'
	out[3] = 'F'
	out[4] = 2 // ELFCLASS64
	out[5] = 1 // ELFDATA2LSB
	out[6] = 1 // EV_CURRENT
	binary.LittleEndian.PutUint16(out[16:], 2)      // ET_EXEC
	binary.LittleEndian.PutUint16(out[18:], 0x3e)   // EM_X86_64
	binary.LittleEndian.PutUint32(out[20:], 1)      // e_version
	entry := uint64(LoadAddr + HeaderSize + entryOff)
	binary.LittleEndian.PutUint64(out[24:], entry)  // e_entry
	binary.LittleEndian.PutUint64(out[32:], 64)     // e_phoff
	// e_shoff 40 = 0
	binary.LittleEndian.PutUint16(out[52:], 64) // e_ehsize
	binary.LittleEndian.PutUint16(out[54:], 56) // e_phentsize
	binary.LittleEndian.PutUint16(out[56:], 1)  // e_phnum
	// Program header at 64.
	ph := out[64:]
	binary.LittleEndian.PutUint32(ph[0:], 1) // PT_LOAD
	binary.LittleEndian.PutUint32(ph[4:], 7) // PF_R|PF_W|PF_X
	binary.LittleEndian.PutUint64(ph[8:], 0) // p_offset
	binary.LittleEndian.PutUint64(ph[16:], LoadAddr)
	binary.LittleEndian.PutUint64(ph[24:], LoadAddr)
	binary.LittleEndian.PutUint64(ph[32:], uint64(filesz))
	binary.LittleEndian.PutUint64(ph[40:], uint64(filesz))
	binary.LittleEndian.PutUint64(ph[48:], 0x1000)
	copy(out[HeaderSize:], code)
	copy(out[HeaderSize+len(code):], rodata)
	return out
}

// CodeVAddr is the virtual address of the first code byte.
func CodeVAddr() uint64 { return LoadAddr + HeaderSize }
