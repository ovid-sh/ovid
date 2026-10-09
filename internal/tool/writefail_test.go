package tool

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"ovid/internal/module"
)

// TestWriteFailureReceipt: a write that failed partway says which files
// have their new text and which were not replaced, by module path, so an agent
// knows what to read again.
func TestWriteFailureReceipt(t *testing.T) {
	t.Setenv(module.PathsEnv, "module")
	dir := mkmod(t, moveManyFiles())
	m, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(dir, "app/a.ov"), filepath.Join(dir, "app/util/util.ov")
	r := writeFailure(m, []string{a, b}, []string{a}, errors.New("rename failed"))
	if r["error"] != "write" || !reflect.DeepEqual(r["written_files"], []string{"app/a.ov"}) || !reflect.DeepEqual(r["unwritten_files"], []string{"app/util/util.ov"}) {
		t.Fatalf("%v", r)
	}
	// A directory sync that failed: every file was renamed.
	r = writeFailure(m, []string{a, b}, []string{a, b}, errors.New("sync failed"))
	if !reflect.DeepEqual(r["unwritten_files"], []string{}) || len(r["written_files"].([]string)) != 2 {
		t.Fatalf("%v", r)
	}
}
