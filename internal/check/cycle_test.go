package check

import (
	"fmt"
	"strings"
	"testing"

	"ovid/internal/ir"
)

// chain returns n packages p0000 -> p0001 -> ..., each importing the next;
// with closed set, the last imports the first.
func chain(n int, closed bool) *ir.Program {
	p := &ir.Program{Module: "demo"}
	name := func(i int) string { return fmt.Sprintf("p%04d", i) }
	for i := 0; i < n; i++ {
		pkg := ir.Package{ID: "pkg:" + name(i), Path: name(i)}
		if j := i + 1; j < n || closed {
			pkg.Imports = []ir.Import{{ID: "im:" + name(i) + ":" + name(j%n), Path: name(j % n)}}
		}
		p.Packages = append(p.Packages, pkg)
	}
	return p
}

func cycles(p *ir.Program) ([]Issue, int) {
	c := &checker{r: &Result{}, pkgs: map[string]*ir.Package{}}
	for i := range p.Packages {
		c.pkgs[p.Packages[i].Path] = &p.Packages[i]
	}
	steps := c.checkCycles(p)
	return c.r.Issues, steps
}

// A valid import chain costs steps linear in its length. Walking from
// every package, as the check once did, took n(n-1)/2.
func TestCycleCheckLinearOnDAG(t *testing.T) {
	const n = 2000
	for _, rev := range []bool{false, true} {
		p := chain(n, false)
		if rev {
			for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
				p.Packages[i], p.Packages[j] = p.Packages[j], p.Packages[i]
			}
		}
		issues, steps := cycles(p)
		if len(issues) != 0 {
			t.Fatalf("valid chain reported %v", issues)
		}
		if steps > 2*n {
			t.Errorf("chain of %d (reversed %v) took %d steps; want at most %d", n, rev, steps, 2*n)
		}
	}
}

// One cycle through every package is reported once, at the first one's
// import, with the whole path.
func TestCycleCheckOneBigCycle(t *testing.T) {
	const n = 2000
	issues, _ := cycles(chain(n, true))
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
	is := issues[0]
	if is.Code != "import_cycle" || is.ID != "im:p0000:p0001" {
		t.Fatalf("got %s at %s", is.Code, is.ID)
	}
	path := strings.Split(strings.TrimPrefix(is.Message, "import cycle: "), " -> ")
	if len(path) != n+1 || path[0] != "p0000" || path[n-1] != "p1999" || path[n] != "p0000" {
		t.Fatalf("path has %d packages: %s ... %s", len(path), path[0], path[len(path)-1])
	}
}
