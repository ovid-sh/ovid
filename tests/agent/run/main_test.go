package main

import (
	"os"
	"path/filepath"
	"reflect"
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
	} {
		got := map[string]int{}
		ovidSubs(cmd, got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ovidSubs(%q) = %v, want %v", cmd, got, want)
		}
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
