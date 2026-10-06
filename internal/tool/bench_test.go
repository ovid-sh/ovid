package tool

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The numbers the README quotes, from one command:
//
//	go test ./internal/tool -run '^$' -bench .
//
// BuildProg and BuildLarge time the Go compiler, in process; the SelfBuild
// pair time the self-hosted compiler as a command, so they need a host that
// can execute it. Each reports the size of what it built as "bytes".

// selfHostBudget is the most the self-hosted compiler may weigh, in bytes.
// It has room above the current size so that ordinary work on prog/ does
// not trip it; a jump past it should be a decision. When raising it, update
// the size the README states.
const selfHostBudget = 140_000

// TestSelfHostSize fails when the self-hosted compiler outgrows its budget.
func TestSelfHostSize(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ovid")
	if code := Build(filepath.Join(repo(t), "prog"), out, io.Discard); code != 0 {
		t.Fatalf("build of prog exits %d", code)
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the self-hosted compiler is %d bytes (budget %d)", st.Size(), selfHostBudget)
	if st.Size() > selfHostBudget {
		t.Fatalf("the self-hosted compiler is %d bytes, over its budget of %d; if the growth is meant, raise selfHostBudget and update the size in README.md",
			st.Size(), selfHostBudget)
	}
}

// largeSource is a program of n funcs, each calling the one before it and
// using four string literals, two of them shared with its neighbour.
func largeSource(n int) string {
	var src strings.Builder
	src.WriteString("package demo\nimport ovid/io\nfunc F0() i64 {\n  return 1\n}\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&src, "func F%d() i64 {\n  return (F%d() + strlen(\"a%d\") + strlen(\"b%d\") + load8(strptr(\"c%d\")) + strlen(\"a%d\")) & 1023\n}\n", i, i-1, i, i, i, i-1)
	}
	fmt.Fprintf(&src, "func main(io *ovid/io.Cap) i64 {\n  return F%d() & 127\n}\n", n-1)
	return src.String()
}

// benchLarge is how many funcs the Large benchmarks build.
const benchLarge = 100_000

// benchModule is the module a benchmark builds: prog/, or a generated one.
func benchModule(b *testing.B, large bool) string {
	b.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	if !large {
		return filepath.Join(root, "prog")
	}
	dir := b.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
		b.Fatal(err)
	}
	for rel, src := range map[string]string{"ovid.mod": "module demo\nentry demo\n", "demo/main.ov": largeSource(benchLarge)} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(src), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

func reportSize(b *testing.B, out string) {
	b.Helper()
	st, err := os.Stat(out)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(st.Size()), "bytes")
}

func benchBuild(b *testing.B, large bool) {
	dir := benchModule(b, large)
	out := filepath.Join(b.TempDir(), "out")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if code := Build(dir, out, io.Discard); code != 0 {
			b.Fatalf("build exits %d", code)
		}
	}
	b.StopTimer()
	reportSize(b, out)
}

func benchSelfBuild(b *testing.B, large bool) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		b.Skipf("%s/%s cannot execute the self-hosted compiler", runtime.GOOS, runtime.GOARCH)
	}
	dir := benchModule(b, large)
	root, _ := filepath.Abs("../..")
	tmp := b.TempDir()
	self, out := filepath.Join(tmp, "ovid"), filepath.Join(tmp, "out")
	if code := Build(filepath.Join(root, "prog"), self, io.Discard); code != 0 {
		b.Fatalf("build of prog exits %d", code)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if msg, err := exec.Command(self, "build", dir, "-o", out, "--std", filepath.Join(root, "std")).CombinedOutput(); err != nil {
			b.Fatalf("self-hosted build: %v\n%s", err, msg)
		}
	}
	b.StopTimer()
	reportSize(b, out)
}

func BenchmarkBuildProg(b *testing.B)      { benchBuild(b, false) }
func BenchmarkSelfBuildProg(b *testing.B)  { benchSelfBuild(b, false) }
func BenchmarkBuildLarge(b *testing.B)     { benchBuild(b, true) }
func BenchmarkSelfBuildLarge(b *testing.B) { benchSelfBuild(b, true) }
