// Package elf writes a static x86-64 executable with no section headers:
// one read+execute segment for the headers and code, and one read-only
// segment for rodata. Nothing is ever writable and executable at once.
// The image is loaded at 0x400000 and is not dynamically linked.
package elf

import "encoding/binary"

const (
	LoadAddr   = 0x400000
	HeaderSize = 64 + 2*56 // ELF header + two program headers
	pageSize   = 0x1000
)

// CodeVAddr is the virtual address of the first code byte.
func CodeVAddr() uint64 { return LoadAddr + HeaderSize }

// RodataVAddr is where rodata is mapped for codeLen bytes of code: on the
// page after the code, at the same offset within its page as in the file,
// so the file needs no padding.
func RodataVAddr(codeLen int) uint64 {
	end := uint64(LoadAddr + HeaderSize + codeLen)
	page := (end + pageSize - 1) &^ (pageSize - 1)
	return page + uint64(HeaderSize+codeLen)%pageSize
}

// Link packs code and rodata into a static ET_EXEC. entryOff is the offset
// of the entry point within code. Absolute addresses in code must already
// be patched for CodeVAddr and RodataVAddr(len(code)).
func Link(code, rodata []byte, entryOff int) []byte {
	textsz := HeaderSize + len(code)
	out := make([]byte, textsz+len(rodata))
	copy(out, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1}) // ELF64, little endian, v1
	le := binary.LittleEndian
	le.PutUint16(out[16:], 2)    // ET_EXEC
	le.PutUint16(out[18:], 0x3e) // EM_X86_64
	le.PutUint32(out[20:], 1)    // e_version
	le.PutUint64(out[24:], uint64(LoadAddr+HeaderSize+entryOff))
	le.PutUint64(out[32:], 64) // e_phoff
	le.PutUint16(out[52:], 64) // e_ehsize
	le.PutUint16(out[54:], 56) // e_phentsize
	le.PutUint16(out[56:], 2)  // e_phnum
	phdr := func(ph []byte, flags uint32, off, vaddr, size uint64) {
		le.PutUint32(ph[0:], 1) // PT_LOAD
		le.PutUint32(ph[4:], flags)
		le.PutUint64(ph[8:], off)
		le.PutUint64(ph[16:], vaddr)
		le.PutUint64(ph[24:], vaddr)
		le.PutUint64(ph[32:], size)
		le.PutUint64(ph[40:], size)
		le.PutUint64(ph[48:], pageSize)
	}
	const pfX, pfR = 1, 4
	phdr(out[64:], pfR|pfX, 0, LoadAddr, uint64(textsz))
	phdr(out[64+56:], pfR, uint64(textsz), RodataVAddr(len(code)), uint64(len(rodata)))
	copy(out[HeaderSize:], code)
	copy(out[textsz:], rodata)
	return out
}
