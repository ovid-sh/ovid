package tool

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// knownDisagree lists the tests/fail cases on which the self-hosted checker
// does not yet report what the Go one does (#39). A case here is skipped
// while they disagree and fails once they agree, so a fix removes its entry.
// Do not add to it: a new disagreement is a bug in one of the checkers.
var knownDisagree = map[string]bool{
	"fail/bad_abi":          true,
	"fail/bad_main_missing": true,
	"fail/bad_module":       true,
	"fail/bad_type":         true,
	"fail/duplicate_name":   true,
	"fail/missing_import":   true,
	"fail/missing_return":   true,
	"fail/no_entry":         true,
	"fail/struct_value":     true,
}

// TestCorpusSelfHost runs the corpus through the self-hosted compiler too.
// For a tests/run case both compilers must emit the same bytes; for a
// tests/fail case both must report the same errors at the same places.
func TestCorpusSelfHost(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("%s/%s cannot execute the self-hosted compiler", runtime.GOOS, runtime.GOARCH)
	}
	stdDir := filepath.Join(repo(t), "std")
	self := filepath.Join(t.TempDir(), "ovid")
	var b bytes.Buffer
	if code := Build(filepath.Join(repo(t), "prog"), self, &b); code != 0 {
		t.Fatalf("go build of prog: %s", b.String())
	}
	for _, c := range corpus(t, "run") {
		t.Run(c.name, func(t *testing.T) {
			tmp := t.TempDir()
			g, s := filepath.Join(tmp, "go"), filepath.Join(tmp, "self")
			var b bytes.Buffer
			if code := Build(c.root, g, &b); code != 0 {
				t.Fatalf("tests/%s does not build:\n%s", c.name, b.String())
			}
			if out, code := run(t, self, "build", c.root, "-o", s, "--std", stdDir); code != 0 {
				t.Fatalf("tests/%s: self-hosted build exits %d:\n%s", c.name, code, out)
			}
			x, _ := os.ReadFile(g)
			y, _ := os.ReadFile(s)
			if len(x) == 0 || !bytes.Equal(x, y) {
				t.Fatalf("tests/%s: binaries differ: go %d bytes, self-hosted %d bytes", c.name, len(x), len(y))
			}
		})
	}
	// where lists diagnostics as sorted "file:line:col code" lines.
	where := func(c *corpusCase, ds []diag) []string {
		var ls []string
		for _, d := range ds {
			ls = append(ls, fmt.Sprintf("%s:%d:%d %s", cmp.Or(c.shown[d.File], d.File), d.Line, d.Col, d.Code))
		}
		sort.Strings(ls)
		return ls
	}
	for _, c := range corpus(t, "fail") {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			Check(c.root, false, &b)
			out, _ := run(t, self, "check", c.root, "--std", stdDir)
			g, s := where(&c, c.diags(t, b.String())), where(&c, c.diags(t, out))
			both := fmt.Sprintf("  go:\n    %s\n  self-hosted:\n    %s", strings.Join(g, "\n    "), strings.Join(s, "\n    "))
			if knownDisagree[c.name] {
				if slices.Equal(g, s) {
					t.Fatalf("tests/%s: the compilers now agree; remove it from knownDisagree", c.name)
				}
				t.Skipf("tests/%s: a known disagreement (#39)\n%s", c.name, both)
			}
			if !slices.Equal(g, s) {
				t.Fatalf("tests/%s: the compilers disagree\n%s", c.name, both)
			}
		})
	}
}
