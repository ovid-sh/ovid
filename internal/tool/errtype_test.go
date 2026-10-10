package tool

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestErrorTypeHints: error is its own type. Each body gets exactly the
// diagnostic codes listed, and the hint named, if any, word for word.
func TestErrorTypeHints(t *testing.T) {
	const (
		notNumber = "an error is not a number: compare it with == or != against 0 (success) or an E_ const such as ovid/io.E_NOENT; e as i64 converts it on purpose"
		notError  = "an error is 0 (success) or an E_ const such as ovid/io.E_NOENT; a func that fails returns (T, error); n as error converts on purpose"
		notCond   = "an error is not a condition: write e != 0 (failed) or e == 0 (succeeded)"
		swapped   = "the value comes first and the error second: return v, e"
	)
	head := "package demo\nimport ovid/io\nconst E_X error = 3\nfunc F(x i64) (i64, error) {\n  return x, 0\n}\nfunc G() error {\n  return E_X\n}\nfunc H(e error) i64 {\n  return 0\n}\n"
	for _, c := range []struct {
		body  string
		codes string
		hint  string
	}{
		{"var e error = G()\n  if e != 0 && e != E_X && e != ovid/io.E_NOENT && 0 != e {\n    return 1\n  }\n  return 0", "", ""},
		// Dropping a func's one error result stays legal (E4, #194).
		{"G()\n  ovid/io.Close(ovid/io.Stdout(io))\n  return 0", "", ""},
		{"if ovid/io.Close(ovid/io.Stdout(io)) != 0 {\n    return 1\n  }\n  return 0", "", ""},
		{"return G()", "type_mismatch", notNumber},
		{"return G() as i64", "", ""},
		{"var e error = 2\n  return 0", "type_mismatch", notError},
		{"var e error = 2 as error\n  return H(0) + H(e)", "", ""},
		{"return H(1)", "type_mismatch", notError + "; demo: func H(e error) i64"},
		{"var e error = G()\n  return (e + 1) as i64", "type_mismatch", notNumber},
		{"var e error = G()\n  if e > 0 {\n    return 1\n  }\n  return 0", "type_mismatch", notNumber},
		{"var e error = G()\n  if e {\n    return 1\n  }\n  return 0", "type_mismatch", notCond},
		{"var e error = G()\n  if e == 1 {\n    return 1\n  }\n  return 0", "type_mismatch", notError},
		{"var v i64, e i64 = F(1)\n  return v", "type_mismatch", "the second result is an error: var v i64, e error = ..."},
		// e is bound as the error it holds, so a use of it as one is fine.
		{"var v i64, e i64 = F(1)\n  if e != E_X {\n    return v\n  }\n  return 0", "type_mismatch", "the second result is an error: var v i64, e error = ..."},
		{"var v i64, e error = F(1)\n  return v + e", "type_mismatch", notNumber},
		{"var e error = G()\n  return F(1) + 0", "unused_result", "var v i64, e error = F(...), or _ for the one not needed"},
		{"var b bool = G() as bool\n  return 0", "type_mismatch", "e as i64 is its code; n as error makes one from an i64"},
		{"var e error = true as error\n  return 0", "type_mismatch", "e as i64 is its code; n as error makes one from an i64"},
		{"if G() == nil {\n    return 1\n  }\n  return 0", "unknown_name", "a null pointer is 0 as *T: var z i64 = 0, then z as *T; the error that means success is 0: if e != 0"},
	} {
		dir := mkmod(t, demo(head+"func main(io *ovid/io.Cap) i64 {\n  "+c.body+"\n}\n"))
		var b bytes.Buffer
		Check(dir, false, &b)
		var codes []string
		var hints []string
		for _, l := range lines(t, b.String()) {
			if l["fact"] == "error" {
				codes = append(codes, l["code"].(string))
				if h, ok := l["hint"].(string); ok {
					hints = append(hints, h)
				}
			}
		}
		if strings.Join(codes, " ") != c.codes || (c.hint != "" && !(len(hints) == 1 && hints[0] == c.hint)) {
			t.Errorf("%q: got %v (hints %q), want %q with hint %q", c.body, codes, hints, c.codes, c.hint)
		}
	}
	// A swapped return is caught at both values, each with the same hint.
	dir := mkmod(t, demo("package demo\nimport ovid/io\nfunc F(x i64) (i64, error) {\n  var e error = 0\n  return e, x\n}\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	var b bytes.Buffer
	Check(dir, false, &b)
	if got := strings.Count(b.String(), `"hint":"`+swapped+`"`); got != 2 {
		t.Errorf("swapped return: %d hints, want 2:\n%s", got, b.String())
	}
	// The old spelling (T, i64) is a syntax error that names the new one.
	dir = mkmod(t, demo("package demo\nimport ovid/io\nfunc F(x i64) (*Pt, i64) {\n  return 0 as *Pt, 0\n}\ntype Pt struct {\n  x i64\n}\nfunc main(io *ovid/io.Cap) i64 {\n  return 0\n}\n"))
	b.Reset()
	Check(dir, false, &b)
	if !strings.Contains(b.String(), `"message":"the second result is an error; write (*demo.Pt, error)"`) {
		t.Errorf("(*Pt, i64): %s", b.String())
	}
}

// TestHTTPReadError: a request whose input cannot be read is not taken
// for a malformed one (#198): Err is the read's ovid/io error, not
// ERR_SYNTAX, and the stdio host answers it with 500, not 400. A
// directory as standard input fails every read with EISDIR.
func TestHTTPReadError(t *testing.T) {
	dir := mkmod(t, demo(`package demo

import ovid/io
import ovid/http

func main(io *ovid/io.Cap) i64 {
  var req *ovid/http.Request = ovid/http.ReadStdio(io)
  var e error = ovid/http.Err(req)
  if e == ovid/http.ERR_SYNTAX {
    return 3
  }
  if e != ovid/io.E_ISDIR {
    return 4
  }
  return ovid/http.WriteStdio(io, req, ovid/http.NewResponse(io), 0)
}
`))
	bin := filepath.Join(t.TempDir(), "srv")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build: %s", b.String())
	}
	if !canExec {
		t.Skip("built only: ovid programs are linux/amd64 binaries")
	}
	in, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var out, stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v; stdout %q, stderr %q", err, out.String(), stderr.String())
	}
	if want := "HTTP/1.1 500 Internal Server Error\r\nContent-Length: 0\r\n\r\n"; out.String() != want {
		t.Fatalf("stdout %q, want %q", out.String(), want)
	}
}
