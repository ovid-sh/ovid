package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNamesDir(t *testing.T) {
	const dir = "/tmp/ovid"
	for s, want := range map[string]bool{
		`{"command":"ls /tmp/ovid"}`:                    true,
		`{"command":"cat /tmp/ovid/README.md"}`:         true,
		`{"command":"cd /tmp/ovid; ls"}`:                true,
		`{"command":"cd /tmp/ovid&&cat goal.json"}`:     true,
		`{"command":"ls /tmp/ovid|wc -l"}`:              true,
		`{"command":"(cd /tmp/ovid)"}`:                  true,
		`{"file_path":"/tmp/ovid"}`:                     true,
		`/tmp/ovid`:                                     true,
		`{"command":"ls /tmp/ovid-out/work"}`:           false,
		`{"command":"ls /tmp/ovid.bak"}`:                false,
		`{"command":"ls /tmp/ovid+copy /tmp/ovid@old"}`: false,
		`{"command":"ls /tmp/ovid_2"}`:                  false,
		`{"command":"ls /tmp/ovid/"}`:                   true,
		`{"command":"cd '/tmp/ovid'"}`:                  true,
		`{"command":"X=/tmp/ovid ls"}`:                  true,
		`{"command":"ls /tmp/ovid>out"}`:                true,
		`{"command":"ls /tmp/ovid-out /tmp/ovid"}`:      true,
		`{"command":"ls /tmp/ovidian /tmp/other"}`:      false,
		`{"command":"cat /var/tmp/ovid/file"}`:          false,
		`{"command":"cat /var/tmp/ovid /tmp/ovid/x"}`:   true,
		`{"command":"ovid check"}`:                      false,
	} {
		if got := namesDir(s, dir); got != want {
			t.Errorf("namesDir(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestOvidSubs(t *testing.T) {
	for cmd, want := range map[string]map[string]int{
		"ovid check":                                {"check": 1},
		"ovid -C /tmp/mod check":                    {"check": 1},
		`ovid -C "/tmp/my module" check`:            {"check": 1},
		`ovid -C '/tmp/my module' -C x outline`:     {"outline": 1},
		"ovid -C a -C b outline --pkg p":            {"outline": 1},
		"ovid check -C mod && ovid build -C mod":    {"check": 1, "build": 1},
		"cd m; ovid show Sum | head; (ovid refs X)": {"show": 1, "refs": 1},
		"echo void check; myovid check":             {},
		// By path, absolute or relative, quoted or not.
		"/tmp/x/bin/ovid check":                                 {"check": 1},
		"./bin/ovid -C m test --run X":                          {"test": 1},
		"cd m && ../bin/ovid show Sum":                          {"show": 1},
		`"/tmp/my dir/bin/ovid" outline`:                        {"outline": 1},
		`'/tmp/v1:0/bin/ovid' -C "a b" refs X`:                  {"refs": 1},
		`"$BIN/ovid" build -o p m; ~/bin/ovid check`:            {"build": 1, "check": 1},
		"ls bin/ovid && cat /tmp/ovid/x; /usr/bin/myovid check": {},
	} {
		got := map[string]int{}
		ovidSubs(cmd, got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ovidSubs(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestOvidCmd(t *testing.T) {
	for cmd, want := range map[string]bool{
		"ovid check":                        true,
		"cd m; ovid help | head":            true,
		"/tmp/x/bin/ovid check":             true,
		"./bin/ovid edit < fix.json":        true,
		`"/tmp/v1:0/bin/ovid" -C m check`:   true,
		"(cd m && ../ovid test)":            true,
		"ls bin/ovid && echo ok":            false,
		"ls -la bin/ovid":                   false,
		"cat /tmp/ovid/README.md":           false,
		"which ovid; myovid check":          false,
		"/usr/bin/myovid check; ovid-old x": false,
	} {
		if got := ovidCmd.MatchString(cmd); got != want {
			t.Errorf("ovidCmd(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// stream is Claude Code stream-json output: each call a Bash tool_use
// with the command, and its tool_result with the output.
func stream(calls ...[2]string) string {
	var b strings.Builder
	for i, c := range calls {
		in, _ := json.Marshal(map[string]string{"command": c[0]})
		use, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": fmt.Sprint("t", i), "name": "Bash", "input": json.RawMessage(in)}}}})
		res, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprint("t", i), "content": c[1]}}}})
		b.WriteString(string(use) + "\n" + string(res) + "\n")
	}
	return b.String()
}

// An ovid run by a path counts as one run by its name, in ovid_calls,
// ovid_cmds, and diag_codes.
func TestReadStreamOvidByPath(t *testing.T) {
	diag := `{"fact":"error","code":"unused_result","message":"m"}` + "\n" + `{"ok":false}`
	for _, ovid := range []string{"ovid", "/tmp/x:0/bin/ovid", "./bin/ovid", `"/tmp/my dir/bin/ovid"`} {
		var a Agent
		readStream(&a, strings.NewReader(stream(
			[2]string{ovid + " check", diag},
			[2]string{"cd m && " + ovid + " -C . test", diag},
			[2]string{"ls bin/ovid && echo " + `'{"fact":"error","code":"x"}'`, `{"fact":"error","code":"x"}`},
		)), io.Discard, &scope{repo: "/repo", work: "/tmp/w", allow: []string{"/tmp/x:0/bin", "/tmp/my dir/bin"}})
		want := Agent{Calls: 3, OvidCalls: 2, OvidCmds: map[string]int{"check": 1, "test": 1},
			DiagCodes: map[string]int{"unused_result": 2}, ReadB: a.ReadB, WriteB: a.WriteB}
		if !reflect.DeepEqual(a, want) {
			t.Errorf("ovid as %s: got %+v, want %+v", ovid, a, want)
		}
	}
}

func TestBinDir(t *testing.T) {
	out := t.TempDir()
	if dir, tmp, err := binDir(out); err != nil || tmp || dir != filepath.Join(out, "bin") {
		t.Errorf("binDir(%q) = %q, %v, %v; want out/bin", out, dir, tmp, err)
	}
	// A model id in the name holds the PATH list separator.
	sep := string(os.PathListSeparator)
	colon := filepath.Join(out, "claude-haiku-v1"+sep+"0")
	dir, tmp, err := binDir(colon)
	if err != nil || !tmp {
		t.Fatalf("binDir(%q) = %q, %v, %v; want a temporary directory", colon, dir, tmp, err)
	}
	defer os.RemoveAll(dir)
	if strings.Contains(dir, sep) {
		t.Errorf("binDir(%q) = %q, which holds %q", colon, dir, sep)
	}
	for _, kv := range childEnv(dir) {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok && filepath.SplitList(p)[0] != dir {
			t.Errorf("PATH = %q, want %q first", p, dir)
		}
	}
	// Nowhere to put it: the temporary directory holds one too.
	tmpdir := filepath.Join(out, "tmp"+sep+"dir")
	if err := os.MkdirAll(tmpdir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpdir)
	if dir, _, err := binDir(colon); err == nil {
		os.RemoveAll(dir)
		t.Errorf("binDir(%q) with TMPDIR %q = %q, want an error", colon, tmpdir, dir)
	}
}

func TestDiagCodes(t *testing.T) {
	out := `{"fact":"error","code":"unused_result","message":"m","file":"a.ov","line":3}
{"fact":"error","code":"unused_result","message":"m"}
  {"fact":"error","code":"type_mismatch","message":"m"}
{"fact":"test","id":"fn:a.TestX","ok":false}
{"ok":false,"error":"stale"}
{"fact":"error","code":"syntax","message":"cut sh
a.ov:3: not json
{"errors":3,"fact":"summary","ok":false}`
	got := map[string]int{}
	diagCodes(out, got)
	want := map[string]int{"unused_result": 2, "type_mismatch": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diagCodes = %v, want %v", got, want)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	write := func(f, s string) {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("m/main.ov", `package m

// Use _ to discard, and ErrText( in a comment counts nothing.
func main(io *ovid/io.Cap) i64 {
  var d bytes, _ = ovid/io.ReadFile(io, "_ \" ErrText(e) _")
  _, e = F()
  var x_y i64 = _x + y_
  ovid/io.Eprint(ovid/io.ErrText(e))
  ovid/io.Eprint("\n")    // Eprint(
  ovid/io.Stderr(b)
  ovid/io.WriteInt(io, 2, n)
  ovid/io.Write(2, b)
  ovid/io.WriteN(1, p, n)
  ovid/io.WriteInt(io, 1, n)
  return 0
}
`)
	write("m/main_test.ov", "package m\n\nfunc TestA(io *ovid/io.Cap) i64 {\n  var v i64, _ = F()\n  ovid/io.Eprint(\"x\")\n  return 0\n}\n")
	write("m/notes.txt", "_ _ Eprint(")
	if got, want := scan(dir), (Static{Discards: 2, ErrText: 1, Stderr: 5}); got != want {
		t.Errorf("scan = %+v, want %+v", got, want)
	}
}
