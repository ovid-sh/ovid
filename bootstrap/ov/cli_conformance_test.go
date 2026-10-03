package ov

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/tool"
)

// These are agent workflows against both public CLIs. Fixtures contain only
// canonical JSON: neither PROJECTION nor package marker files are consulted.
// Build the self-hosted CLI from current .ov sources, never checked-in prog/.
func TestCLIConformance(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("Ovid executables target Linux x86-64")
	}
	clis := conformanceCLIs(t)
	initialModules := map[string][]byte{}
	for name, cli := range clis {
		t.Run(name, func(t *testing.T) {
			t.Run("init_without_hidden_inputs", func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "nested", "module")
				created := invokeCLI(t, cli, "init", dir)
				body := oneObject(t, created.out)
				if created.code != 0 || body["ok"] != true || body["entry"] != "app" {
					t.Fatalf("init: %s", created)
				}
				initialModules[name] = canonicalBytes(t, dir)
				q := queryCLI(t, cli, dir)
				if body["revision"] != q.Revision {
					t.Fatalf("init receipt does not identify canonical revision: %#v", body)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != "ovid.json" && entry.Name() != ".ovid.lock" {
						t.Fatalf("init created unexpected compiler input %q", entry.Name())
					}
				}
				assertBuildExit(t, cli, dir, 0)
				again := invokeCLI(t, cli, "init", dir)
				if again.code != 1 || oneObject(t, again.out)["error"] != "exists" || !bytes.Equal(initialModules[name], canonicalBytes(t, dir)) {
					t.Fatalf("init must preserve an existing module: %s", again)
				}
			})
			t.Run("init_preserves_dangling_symlink", func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "ovid.json")
				target := filepath.Join(dir, "missing.json")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				result := invokeCLI(t, cli, "init", dir)
				if result.code != 1 || oneObject(t, result.out)["error"] != "exists" {
					t.Fatalf("init must reject an existing path: %s", result)
				}
				if link, err := os.Readlink(path); err != nil || link != target {
					t.Fatalf("init replaced dangling symlink: target %q, error %v", link, err)
				}
			})
			t.Run("init_rejects_file_as_directory", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
					t.Fatal(err)
				}
				result := invokeCLI(t, cli, "init", path)
				if result.code != 1 || oneObject(t, result.out)["error"] != "mkdir" {
					t.Fatalf("init must reject a non-directory destination: %s", result)
				}
				if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep me" {
					t.Fatal("init changed the existing file")
				}
			})
			t.Run("check_facts_are_opt_in", func(t *testing.T) {
				dir := conformanceFixture(t, false)
				q := queryCLI(t, cli, dir)
				checked := invokeCLI(t, cli, "check", dir)
				summary := oneObject(t, checked.out)
				if checked.code != 0 || summary["fact"] != "summary" || summary["ok"] != true || summary["revision"] != q.Revision {
					t.Fatalf("successful check must return one revisioned summary: %s", checked)
				}
				verbose := invokeCLI(t, cli, "check", dir, "--facts")
				facts := jsonFacts(t, verbose.out)
				if verbose.code != 0 || len(facts) <= 1 || facts[len(facts)-1]["fact"] != "summary" || facts[len(facts)-1]["revision"] != q.Revision {
					t.Fatalf("check --facts must retain full introspection: %s", verbose)
				}
			})
			t.Run("repair_from_diagnostic", func(t *testing.T) {
				dir := conformanceFixture(t, true)
				checked := invokeCLI(t, cli, "check", dir)
				if checked.code != 1 {
					t.Fatalf("broken fixture: %s", checked)
				}
				var mismatch map[string]any
				for _, fact := range jsonFacts(t, checked.out) {
					if fact["code"] == "type_mismatch" {
						mismatch = fact
					}
				}
				if mismatch["expected"] != "i64" || mismatch["actual"] != "bool" {
					t.Fatalf("diagnostic must describe both types: %#v", mismatch)
				}
				id, ok := mismatch["id"].(string)
				if !ok || id == "" {
					t.Fatalf("diagnostic must identify repair target: %#v", mismatch)
				}
				q := queryCLI(t, cli, dir, "--id", id)
				if len(q.Matches) != 1 || q.Matches[0].Node["op"] != "bool" {
					t.Fatalf("diagnostic must locate the bad expression: %#v", q)
				}
				patch := writePatch(t, q.Revision, replaceOp(id, map[string]any{"id": id, "op": "int", "value": 42}))
				applied := invokeCLI(t, cli, "patch", dir, patch)
				response := oneObject(t, applied.out)
				if applied.code != 0 || response["ok"] != true || response["revision"] == q.Revision {
					t.Fatalf("repair: %s", applied)
				}
				fixed := queryCLI(t, cli, dir, "--id", id)
				if response["revision"] != fixed.Revision {
					t.Fatalf("patch receipt does not identify committed revision: %#v", response)
				}
				if len(fixed.Matches) != 1 || fixed.Matches[0].ID != id || fixed.Matches[0].Node["value"] != float64(42) {
					t.Fatalf("replacement must retain identity: %#v", fixed)
				}
				assertBuildExit(t, cli, dir, 42)
				before := canonicalBytes(t, dir)
				stale := invokeCLI(t, cli, "patch", dir, patch)
				staleBody := oneObject(t, stale.out)
				if stale.code != 2 || staleBody["error"] != "stale_patch" {
					t.Fatalf("stale retry: %s", stale)
				}
				if staleBody["baseRevision"] != q.Revision || staleBody["currentRevision"] != fixed.Revision || staleBody["revision"] != fixed.Revision {
					t.Fatalf("stale conflict must identify both revisions: %#v", staleBody)
				}
				if !bytes.Equal(before, canonicalBytes(t, dir)) {
					t.Fatal("stale retry changed canonical program")
				}
			})

			t.Run("failed_transaction_preserves_bytes", func(t *testing.T) {
				dir := conformanceFixture(t, false)
				q := queryCLI(t, cli, dir, "--id", "ex:compute:value")
				before := canonicalBytes(t, dir)
				cases := []struct {
					name string
					ops  []map[string]any
				}{
					{"late_missing_target", []map[string]any{
						replaceOp("ex:compute:value", map[string]any{"id": "ex:compute:value", "op": "int", "value": 99}),
						replaceOp("ex:missing", map[string]any{"id": "ex:missing", "op": "int", "value": 0}),
					}},
					{"identity_change", []map[string]any{replaceOp("ex:compute:value", map[string]any{"id": "ex:different", "op": "int", "value": 99})}},
					{"empty", nil},
				}
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						result := invokeCLI(t, cli, "patch", dir, writePatch(t, q.Revision, tc.ops...))
						body := oneObject(t, result.out)
						if result.code != 1 || body["ok"] != false || body["error"] != "bad_patch" {
							t.Fatalf("invalid patch: %s", result)
						}
						if !bytes.Equal(before, canonicalBytes(t, dir)) {
							t.Fatal("rejected transaction changed canonical program")
						}
					})
				}
				// A failed transaction must also release the writer lock.
				valid := invokeCLI(t, cli, "patch", dir, writePatch(t, q.Revision, replaceOp("ex:compute:value", map[string]any{"id": "ex:compute:value", "op": "int", "value": 43})))
				if valid.code != 0 {
					t.Fatalf("patch after failed transaction: %s", valid)
				}
				assertBuildExit(t, cli, dir, 43)
			})
			t.Run("revision_mismatch_is_not_a_valid_base", func(t *testing.T) {
				dir := conformanceFixture(t, false)
				q := queryCLI(t, cli, dir, "--id", "ex:compute:value")
				altered := append(canonicalBytes(t, dir), '\n')
				if err := os.WriteFile(filepath.Join(dir, "ovid.json"), altered, 0o644); err != nil {
					t.Fatal(err)
				}
				query := invokeCLI(t, cli, "query", dir)
				if query.code != 1 || oneObject(t, query.out)["error"] != "revision_mismatch" {
					t.Fatalf("query must reject unstamped canonical edits: %s", query)
				}
				patch := writePatch(t, q.Revision, replaceOp("ex:compute:value", map[string]any{"id": "ex:compute:value", "op": "int", "value": 43}))
				result := invokeCLI(t, cli, "patch", dir, patch)
				if result.code != 1 || oneObject(t, result.out)["error"] != "revision_mismatch" || !bytes.Equal(altered, canonicalBytes(t, dir)) {
					t.Fatalf("patch must preserve and reject unstamped canonical edits: %s", result)
				}
			})
			t.Run("patch_from_stdin", func(t *testing.T) {
				dir := conformanceFixture(t, false)
				q := queryCLI(t, cli, dir, "--id", "ex:compute:value")
				path := writePatch(t, q.Revision, replaceOp("ex:compute:value", map[string]any{"id": "ex:compute:value", "op": "int", "value": 43}))
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				result := runCLIInput(cli, raw, "patch", dir, "-")
				if result.code != 0 || oneObject(t, result.out)["ok"] != true {
					t.Fatalf("stdin patch: %s", result)
				}
				assertBuildExit(t, cli, dir, 43)
			})

			t.Run("constant_and_package_replacement", func(t *testing.T) {
				dir := conformanceFixture(t, false)
				q := queryCLI(t, cli, dir, "--id", "cn:demo.Answer")
				c := q.Matches[0].Node
				c["value"] = 7
				result := invokeCLI(t, cli, "patch", dir, writePatch(t, q.Revision, replaceOp("cn:demo.Answer", c)))
				if result.code != 0 {
					t.Fatalf("replace constant: %s", result)
				}
				q = queryCLI(t, cli, dir, "--id", "cn:demo.Answer")
				if q.Matches[0].Node["value"] != float64(7) {
					t.Fatalf("constant replacement missing: %#v", q)
				}
				q = queryCLI(t, cli, dir, "--id", "pkg:demo")
				pkg := q.Matches[0].Node
				extra := map[string]any{"id": "fn:extra", "name": "Extra", "result": "i64", "body": []any{map[string]any{"id": "st:extra:return", "op": "return", "val": map[string]any{"id": "ex:extra:answer", "op": "int", "value": 7}}}}
				pkg["funcs"] = append(pkg["funcs"].([]any), extra)
				result = invokeCLI(t, cli, "patch", dir, writePatch(t, q.Revision, replaceOp("pkg:demo", pkg)))
				if result.code != 0 {
					t.Fatalf("add function by replacing package: %s", result)
				}
				q = queryCLI(t, cli, dir, "--id", "fn:extra")
				if len(q.Matches) != 1 || q.Matches[0].Name != "Extra" {
					t.Fatalf("added function missing: %#v", q)
				}
				assertBuildExit(t, cli, dir, 42)
			})

			t.Run("bounded_discovery_and_callsite_rename", func(t *testing.T) {
				dir := conformanceFixture(t, false)
				page := queryCLI(t, cli, dir, "--pkg", "demo", "--limit", "2")
				if page.Returned != 2 || len(page.Matches) != 2 || !page.HasMore || page.NextOffset != 2 || page.Total <= 2 {
					t.Fatalf("first page: %#v", page)
				}
				seen := map[string]bool{}
				for _, m := range page.Matches {
					if m.Node != nil {
						t.Fatal("discovery unexpectedly expands AST nodes")
					}
					seen[m.ID] = true
				}
				rest := queryCLI(t, cli, dir, "--pkg", "demo", "--limit", "0", "--offset", "2")
				if rest.HasMore || rest.Total != page.Total || len(rest.Matches)+len(page.Matches) != page.Total {
					t.Fatalf("remaining page: %#v", rest)
				}
				for _, m := range rest.Matches {
					if seen[m.ID] || m.Node != nil {
						t.Fatalf("duplicate or expanded summary: %#v", m)
					}
				}
				calls := queryCLI(t, cli, dir, "--calls-to", "fn:demo.Compute", "--full")
				if len(calls.Matches) != 1 || calls.Matches[0].ID != "ex:main:call" || calls.Matches[0].FuncID != "fn:demo.main" {
					t.Fatalf("callsite discovery: %#v", calls)
				}
				definition := queryCLI(t, cli, dir, "--id", "fn:demo.Compute")
				fn := definition.Matches[0].Node
				fn["name"] = "Calculate"
				call := calls.Matches[0].Node
				call["func"] = "Calculate"
				result := invokeCLI(t, cli, "patch", dir, writePatch(t, calls.Revision, replaceOp("fn:demo.Compute", fn), replaceOp("ex:main:call", call)))
				if result.code != 0 {
					t.Fatalf("rename definition and discovered callsite: %s", result)
				}
				renamed := queryCLI(t, cli, dir, "--id", "fn:demo.Compute")
				if renamed.Matches[0].Name != "Calculate" {
					t.Fatal("rename changed or lost definition identity")
				}
				assertBuildExit(t, cli, dir, 42)
			})
		})
	}
	if !bytes.Equal(initialModules["go"], initialModules["self"]) {
		t.Error("Go and self-hosted init produced different canonical modules")
	}

	// Include mixed Go/self-hosted processes: both implementations must use the
	// same cross-process transaction boundary, not merely an in-process mutex.
	for _, names := range [][]string{{"go", "go"}, {"self", "self"}, {"go", "self"}} {
		t.Run("concurrent_"+strings.Join(names, "_"), func(t *testing.T) {
			for round := 0; round < 4; round++ {
				dir := conformanceFixture(t, false)
				q := queryCLI(t, clis["go"], dir, "--id", "ex:compute:value")
				const writers = 8
				patches := make([]string, writers)
				for i := range patches {
					patches[i] = writePatch(t, q.Revision, replaceOp("ex:compute:value", map[string]any{"id": "ex:compute:value", "op": "int", "value": 50 + i}))
				}
				results := make([]cliResult, writers)
				lock, err := os.OpenFile(filepath.Join(dir, ".ovid.lock"), os.O_CREATE|os.O_RDWR, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
					lock.Close()
					t.Fatal(err)
				}
				start := make(chan struct{})
				finished := make(chan int, writers)
				var wg sync.WaitGroup
				for i := range patches {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						<-start
						results[i] = runCLI(clis[names[i%len(names)]], "patch", dir, patches[i])
						finished <- i
					}(i)
				}
				close(start)
				// Queue the competing processes at the public lock boundary.
				// A writer completing while this lock is held breaks interop.
				premature := -1
				select {
				case premature = <-finished:
				case <-time.After(50 * time.Millisecond):
				}
				unlockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				lock.Close()
				wg.Wait()
				if unlockErr != nil {
					t.Fatal(unlockErr)
				}
				if premature >= 0 {
					t.Fatalf("writer %d ignored the shared module lock: %s", premature, results[premature])
				}
				succeeded, winner := 0, -1
				for i, result := range results {
					body := oneObject(t, result.out)
					if result.code == 0 && body["ok"] == true {
						succeeded++
						winner = i
					} else if result.code != 2 || body["error"] != "stale_patch" {
						t.Fatalf("writer %d round %d: %s", i, round, result)
					}
				}
				if succeeded != 1 {
					t.Fatalf("round %d: %d writers reported success; want exactly one", round, succeeded)
				}
				final := queryCLI(t, clis["go"], dir, "--id", "ex:compute:value")
				if final.Matches[0].Node["value"] != float64(50+winner) {
					t.Fatal("successful writer's change was lost")
				}
				for _, cli := range clis {
					checked := invokeCLI(t, cli, "check", dir)
					if checked.code != 0 {
						t.Fatalf("concurrent writes left invalid canonical file: %s", checked)
					}
				}
			}
		})
	}
}

type cliResult struct {
	code int
	out  []byte
	err  error
}

func (r cliResult) String() string {
	return fmt.Sprintf("exit %d, error %v:\n%s", r.code, r.err, r.out)
}

func runCLI(cli string, args ...string) cliResult {
	return runCLIInput(cli, nil, args...)
}

func runCLIInput(cli string, input []byte, args ...string) cliResult {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	result := cliResult{out: out, err: err}
	if err != nil {
		result.code = -1
		if ee, ok := err.(*exec.ExitError); ok {
			result.code = ee.ExitCode()
		}
	}
	return result
}

func invokeCLI(t *testing.T, cli string, args ...string) cliResult {
	t.Helper()
	result := runCLI(cli, args...)
	if result.code < 0 {
		t.Fatalf("execute %s %v: %s", cli, args, result)
	}
	return result
}

type cliQuery struct {
	Revision   string `json:"revision"`
	Total      int    `json:"total"`
	Returned   int    `json:"returned"`
	HasMore    bool   `json:"hasMore"`
	NextOffset int    `json:"nextOffset"`
	Matches    []struct {
		ID     string         `json:"id"`
		Name   string         `json:"name"`
		FuncID string         `json:"funcId"`
		Node   map[string]any `json:"node"`
	} `json:"matches"`
}

func queryCLI(t *testing.T, cli, dir string, flags ...string) cliQuery {
	t.Helper()
	result := invokeCLI(t, cli, append([]string{"query", dir}, flags...)...)
	if result.code != 0 {
		t.Fatalf("query: %s", result)
	}
	oneObject(t, result.out)
	var q cliQuery
	if err := json.Unmarshal(result.out, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Revision) != 64 {
		t.Fatalf("query revision: %#v", q)
	}
	return q
}

func oneObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value map[string]any
	if err := dec.Decode(&value); err != nil {
		t.Fatalf("expected JSON object: %v\n%s", err, raw)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("expected exactly one JSON object, trailing value/error %v: %v\n%s", extra, err, raw)
	}
	return value
}

func jsonFacts(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var facts []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		facts = append(facts, oneObject(t, line))
	}
	return facts
}

func replaceOp(id string, node map[string]any) map[string]any {
	return map[string]any{"op": "replace", "id": id, "node": node}
}

func writePatch(t *testing.T, revision string, ops ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"baseRevision": revision, "ops": ops})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func canonicalBytes(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "ovid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertBuildExit(t *testing.T, cli, dir string, want int) {
	t.Helper()
	checked := invokeCLI(t, cli, "check", dir)
	if checked.code != 0 {
		t.Fatalf("check: %s", checked)
	}
	facts := jsonFacts(t, checked.out)
	if last := facts[len(facts)-1]; last["fact"] != "summary" || last["ok"] != true {
		t.Fatalf("check summary: %#v", last)
	}
	binary := filepath.Join(t.TempDir(), "program")
	built := invokeCLI(t, cli, "build", dir, "-o", binary)
	body := oneObject(t, built.out)
	if built.code != 0 || body["ok"] != true || body["output"] != binary {
		t.Fatalf("build: %s", built)
	}
	run := invokeCLI(t, binary)
	if run.code != want {
		t.Fatalf("program: %s; want exit %d", run, want)
	}
}

func conformanceCLIs(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	goCLI := filepath.Join(dir, "ovid-go")
	cmd := exec.Command("go", "build", "-o", goCLI, "./cmd/ovid")
	cmd.Dir = repositoryRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go CLI: %v\n%s", err, out)
	}
	root := filepath.Join(repositoryRoot(t), "src", "cli")
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".ov") {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(source)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	program, err := ParseProgram(files, "ovid/cli")
	if err != nil {
		t.Fatalf("parse current CLI sources: %v", err)
	}
	bin, err := compile.Compile(program)
	if err != nil {
		t.Fatalf("compile current CLI sources: %v", err)
	}
	stage1 := filepath.Join(dir, "ovid-stage1")
	if err := os.WriteFile(stage1, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(dir, "compiler")
	if err := tool.WriteModule(module, program, ""); err != nil {
		t.Fatal(err)
	}
	selfCLI := filepath.Join(dir, "ovid-self")
	built := invokeCLI(t, stage1, "build", module, "-o", selfCLI)
	if built.code != 0 {
		t.Fatalf("self-host current CLI: %s", built)
	}
	return map[string]string{"go": goCLI, "self": selfCLI}
}

func conformanceFixture(t *testing.T, broken bool) string {
	t.Helper()
	const canonical = `{
  "revision": "0000000000000000000000000000000000000000000000000000000000000000",
  "module": "demo", "entry": "demo", "packages": [
    {"id":"pkg:ovid/io", "path":"ovid/io", "types":[
      {"id":"ty:ovid/io.Cap", "name":"Cap", "fields":[
        {"id":"fld:cap:argc", "name":"argc", "type":"i64"},
        {"id":"fld:cap:argv", "name":"argv", "type":"i64"},
        {"id":"fld:cap:heap", "name":"heap", "type":"i64"},
        {"id":"fld:cap:used", "name":"used", "type":"i64"},
        {"id":"fld:cap:size", "name":"size", "type":"i64"}
      ]}
    ]},
    {"id":"pkg:demo", "path":"demo", "imports":[{"id":"im:demo:io", "path":"ovid/io"}],
     "consts":[{"id":"cn:demo.Answer", "name":"Answer", "type":"i64", "value":41}],
     "funcs":[
       {"id":"fn:demo.Compute", "name":"Compute", "result":"i64", "body":[
         {"id":"st:compute:return", "op":"return", "val":{"id":"ex:compute:value", "op":"int", "value":42}}
       ]},
       {"id":"fn:demo.main", "name":"main", "result":"i64", "params":[{"id":"pa:main:io", "name":"io", "type":"*ovid/io.Cap"}], "body":[
         {"id":"st:main:return", "op":"return", "val":{"id":"ex:main:call", "op":"call", "func":"Compute"}}
       ]}
     ]}
  ]
}`
	raw := []byte(canonical)
	if broken {
		raw = bytes.Replace(raw, []byte(`"op":"int", "value":42`), []byte(`"op":"bool", "value":true`), 1)
	}
	stamped, err := ir.Stamp(raw)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ovid.json"), stamped, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
