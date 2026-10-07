package tool

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestRunInterrupt: ^C goes to the whole foreground process group. The
// program dies of it, and ovid run must outlive it to report the signal
// and exit 130. ovid runs here as this test binary re-executed into a
// process group of its own, so the signal does not reach go test.
func TestRunInterrupt(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("ovid programs are linux/amd64 binaries")
	}
	dir := mkmod(t, demo(`package demo

import ovid/io

func main(io *ovid/io.Cap) i64 {
  var buf i64 = ovid/io.Alloc(io, 8)
  ovid/io.Stdout(strptr("ready\n"), 6)
  var n i64, _ = ovid/io.Read(0, buf, 8)
  return n
}
`))
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRun$")
	cmd.Env = append(os.Environ(), "OVID_HELPER_RUN="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe() // held open, so the program blocks in read
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ready := make([]byte, 6)
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("program did not start: %q %v; stderr %s", ready, err, stderr.String())
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { io.Copy(io.Discard, stdout); done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ovid run did not end after SIGINT")
	}
	if code := cmd.ProcessState.ExitCode(); code != 130 {
		t.Fatalf("exit %d (%v); stderr %s", code, cmd.ProcessState, stderr.String())
	}
	r := last(t, stderr.String())
	if r["error"] != "killed" || r["signal"] != "interrupt" || r["exit"] != 130.0 {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

// TestHelperRun is ovid run for TestRunInterrupt; alone it does nothing.
func TestHelperRun(t *testing.T) {
	dir := os.Getenv("OVID_HELPER_RUN")
	if dir == "" {
		t.Skip("run by TestRunInterrupt")
	}
	os.Exit(Run(dir, nil, os.Stdout))
}
