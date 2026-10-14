package tool

import (
	"path/filepath"
	"runtime"
	"testing"

	"ovid/internal/check"
	"ovid/internal/module"
)

// liveBytes is the heap f's results hold once f has returned, per byte of
// module source: the memory a command needs however the collector is tuned.
func liveBytes(t *testing.T, dir string, f func(m *module.Module) any) float64 {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	m, err := module.Load(dir)
	if err != nil || len(m.Errors) != 0 {
		t.Fatal(err, m.Errors)
	}
	res := f(m)
	runtime.GC()
	runtime.ReadMemStats(&after)
	src := 0
	for _, file := range m.Files {
		src += len(file.Src)
	}
	runtime.KeepAlive(m)
	runtime.KeepAlive(res)
	return float64(after.HeapAlloc-before.HeapAlloc) / float64(src)
}

// TestCheckMemoryBudget holds the memory of a loaded and checked module to
// a budget, on prog/. How large a sandbox must be follows from this number
// (#105), and it drifts up unnoticed: check, build, run, and test once
// carried the id index and the checker's type and use tables, which they
// never read, and paid a third more for it.
func TestCheckMemoryBudget(t *testing.T) {
	prog := filepath.Join(repo(t), "prog")
	lean := liveBytes(t, prog, func(m *module.Module) any { return check.Errors(m.Prog) })
	full := liveBytes(t, prog, func(m *module.Module) any {
		m.Index()
		return check.Run(m.Prog)
	})
	t.Logf("live heap per byte of source: %.0f for check, build, run, test; %.0f with the index and the checker's tables", lean, full)
	// The budgets sit about a seventh above what was measured when they
	// were set (53 and 91). Raise one only with the cause in hand.
	if lean > 61 {
		t.Errorf("a loaded and checked module holds %.0f bytes per byte of source, over the budget of 61", lean)
	}
	if full > 105 {
		t.Errorf("with the id index and the checker's tables it holds %.0f bytes per byte of source, over the budget of 105", full)
	}
	if lean > 0.75*full {
		t.Errorf("the lean path (%.0f) is no longer clearly below the full one (%.0f): something on it builds the index or records types and uses", lean, full)
	}
}
