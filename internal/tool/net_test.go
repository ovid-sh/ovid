package tool

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// echo listens on the port in its argument, serves one client through
// epoll, sending back what it reads, and exits when the client closes:
// 0 if the clock moved forward and never backward, else 1.
const echo = `package demo
import ovid/io
func main(io *ovid/io.Cap) i64 {
  var a i64 = ovid/io.Arg(io, 1)
  var port i64 = 0
  while load8(a) != 0 {
    port = (port * 10) + (load8(a) - 48)
    a = a + 1
  }
  var lfd i64 = ovid/io.TCPListen(io, 0x7f000001, port, 16)
  if lfd < 0 {
    return 2
  }
  var ts i64 = ovid/io.Alloc(io, 16)
  var t0 i64 = ovid/io.NowMs(ts)
  var ep i64 = ovid/io.EpollCreate()
  var ev i64 = ovid/io.Alloc(io, 16)
  var evs i64 = ovid/io.Alloc(io, 8 * ovid/io.EPOLLEVENT)
  var buf i64 = ovid/io.Alloc(io, 4096)
  ovid/io.EpollCtl(ep, ovid/io.EPOLL_CTL_ADD, lfd, ovid/io.EPOLLIN, 0, ev)
  var open bool = true
  while open {
    var n i64 = ovid/io.EpollWait(ep, evs, 8, -1)
    var i i64 = 0
    while i < n {
      var fd i64 = ovid/io.EpollData(evs, i)
      if fd == 0 {
        fd = ovid/io.Accept(lfd)
        ovid/io.SetNoDelay(fd, ts)
        ovid/io.EpollCtl(ep, ovid/io.EPOLL_CTL_ADD, fd, ovid/io.EPOLLIN, fd, ev)
      } else if (ovid/io.EpollEvents(evs, i) & ovid/io.EPOLLIN) != 0 {
        var r i64 = ovid/io.Read(fd, buf, 4096)
        if r > 0 {
          ovid/io.Send(fd, buf, r)
        } else if r != ovid/io.EAGAIN {
          open = false
        }
      }
      i = i + 1
    }
  }
  // A second Accept has nothing to take: the socket does not block.
  if ovid/io.Accept(lfd) != ovid/io.EAGAIN {
    return 3
  }
  if ovid/io.NowMs(ts) < t0 || t0 <= 0 {
    return 1
  }
  return 0
}
`

func TestNet(t *testing.T) {
	dir := mkmod(t, demo(echo))
	bin := filepath.Join(dir, "bin", "demo")
	var b bytes.Buffer
	if code := Build(dir, bin, &b); code != 0 {
		t.Fatalf("build %d:\n%s", code, b.String())
	}
	needExec(t)
	// Take a free port from the kernel, and give it to the program.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(bin, fmt.Sprint(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var conn net.Conn
	for i := 0; i < 200 && conn == nil; i++ {
		if conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
			conn = nil
			time.Sleep(10 * time.Millisecond)
		}
	}
	if conn == nil {
		t.Fatalf("the program never listened: %v", err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	for _, msg := range []string{"ping", "a longer message, sent second"} {
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != msg {
			t.Fatalf("echo of %q: %q, %v", msg, got, err)
		}
	}
	conn.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the program: %v", err)
	}
}
