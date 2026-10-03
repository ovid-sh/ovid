package tool

import (
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"ovid/internal/ir"
)

// The self-hosted CmdInit uses these same bytes. Cross-runtime conformance tests
// ensure that both initializers produce the same program and revision.
//
//go:embed init.json
var initialProgram []byte

// Init creates a standalone module with the entry ABI and a main returning zero.
// It shares the patch lock, and refuses any existing ovid.json, including links.
func Init(dir string, w io.Writer) int {
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeErr(w, "mkdir", err.Error())
		return 1
	}
	lock, err := lockModule(dir)
	if err != nil {
		writeErr(w, "lock", err.Error())
		return 1
	}
	defer lock.Close()
	path := filepath.Join(dir, "ovid.json")
	if _, err := os.Lstat(path); err == nil {
		writeErr(w, "exists", "ovid.json already exists")
		return 1
	} else if !os.IsNotExist(err) {
		writeErr(w, "stat", err.Error())
		return 1
	}
	file, err := ir.Stamp(initialProgram)
	if err != nil {
		writeErr(w, "revision", err.Error())
		return 1
	}
	if err := atomicModuleFile(dir, file); err != nil {
		writeErr(w, "write", err.Error())
		return 1
	}
	_, revision, _ := ir.Hash(file)
	_ = json.NewEncoder(w).Encode(struct {
		OK       bool   `json:"ok"`
		Revision string `json:"revision"`
		Entry    string `json:"entry"`
	}{true, revision, "app"})
	return 0
}
