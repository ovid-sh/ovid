package tool

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHTTPStdioLimit: ovid/http.ReadStdio takes a request of exactly
// MAX_REQUEST bytes and answers one byte more with 413, by its
// Content-Length or by a head that does not end, reading no further than
// one byte past the limit. A corpus case
// cannot carry 8 MiB of input in a comment, so this one builds the program
// itself.
func TestHTTPStdioLimit(t *testing.T) {
	const max = 8 << 20 // ovid/http.MAX_REQUEST
	dir := mkmod(t, demo(`package demo

import ovid/io
import ovid/http

func main(io *ovid/io.Cap) i64 {
  var req *ovid/http.Request = ovid/http.ReadStdio(io)
  var res *ovid/http.Response = ovid/http.NewResponse(io)
  if ovid/http.Err(req) != 0 {
    return ovid/http.WriteStdio(io, req, res, 0)
  }
  ovid/http.WriteInt(io, res, ovid/http.BodyLen(req))
  return ovid/http.WriteStdio(io, req, res, 0)
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
	// The Content-Length values below are all 7 digits, so the head is
	// the same length in each case.
	head := func(n int) string { return fmt.Sprintf("POST / HTTP/1.1\r\nContent-Length: %d\r\n\r\n", n) }
	body := max - len(head(1000000))
	tooLarge := "HTTP/1.1 413 Content Too Large\r\nContent-Length: 0\r\n\r\n"
	for _, c := range []struct {
		in   string
		want string
	}{
		{head(body) + strings.Repeat("x", body), fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%d", len(fmt.Sprint(body)), body)},
		// Answered from the head: the body is not read at all.
		{head(body+1) + strings.Repeat("x", body+1), tooLarge},
		{"POST / HTTP/1.1\r\nContent-Length: 99999999999999999999\r\n\r\n" + strings.Repeat("x", max), tooLarge},
		// A head with no end, longer than a request may be.
		{"GET / HTTP/1.1\r\nX: " + strings.Repeat("x", 2*max), tooLarge},
	} {
		// stdin is a regular file, which a single read can drain whole,
		// unlike a pipe; the program shares its offset, so afterwards the
		// offset is how far it read.
		f, err := os.Create(filepath.Join(t.TempDir(), "in"))
		if err != nil {
			t.Fatal(err)
		}
		in := c.in
		if _, err := f.WriteString(in); err != nil {
			t.Fatal(err)
		}
		f.Seek(0, io.SeekStart)
		var out, stderr bytes.Buffer
		cmd := exec.Command(bin)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = f, &out, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%d bytes: %v; stderr %q", len(in), err, stderr.String())
		}
		if out.String() != c.want {
			t.Errorf("%d bytes: got %q, want %q", len(in), out.String(), c.want)
		}
		if off, _ := f.Seek(0, io.SeekCurrent); off > max+1 {
			t.Errorf("%d bytes: read %d of them, past the limit of %d and the byte after", len(in), off, max)
		}
		f.Close()
	}
}

// TestHandleEntry: an entry package with handle and no main is served by
// the stdio host the toolchain writes; handle cannot be renamed or moved
// away, as main cannot, and a fault in it is reported at its line.
func TestHandleEntry(t *testing.T) {
	dir := mkmod(t, map[string]string{
		"demo/main.ov": `package demo

import ovid/io
import ovid/http

func handle(io *ovid/io.Cap, req *ovid/http.Request, res *ovid/http.Response) i64 {
  if ovid/http.PathIs(req, strptr("/crash"), 6) {
    return load64(8)
  }
  ovid/http.Write(io, res, strptr("hi"), 2)
  return 0
}
`,
		"other/x.ov": "package other\n",
	})
	var b bytes.Buffer
	if code := Rename(dir, "fn:demo.handle", "serve", false, &b); code != ExitFail || !strings.Contains(b.String(), `"bad_name"`) {
		t.Errorf("rename: exit %d, %s", code, b.String())
	}
	b.Reset()
	if code := Move(dir, "fn:demo.handle", "other", "", false, &b); code != ExitFail || !strings.Contains(b.String(), `"bad_move"`) {
		t.Errorf("move: exit %d, %s", code, b.String())
	}
	bin := filepath.Join(t.TempDir(), "srv")
	b.Reset()
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build: %s", b.String())
	}
	if !canExec {
		t.Skip("built only: ovid programs are linux/amd64 binaries")
	}
	var out bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Stdin, cmd.Stdout = strings.NewReader("GET / HTTP/1.1\r\n\r\n"), &out
	if err := cmd.Run(); err != nil || out.String() != "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi" {
		t.Errorf("served: %v, %q", err, out.String())
	}
	// run has no source for the host's own frame; the fault is still
	// placed in handle.
	f, err := os.Create(filepath.Join(t.TempDir(), "in"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString("GET /crash HTTP/1.1\r\n\r\n")
	f.Seek(0, io.SeekStart)
	stdin := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = stdin }()
	b.Reset()
	RunWith(dir, nil, RunOpts{JSON: true}, &b)
	if !strings.Contains(b.String(), `"id":"st:demo.handle:2"`) {
		t.Errorf("run: %s", b.String())
	}
}
