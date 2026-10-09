package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The jail mounts the run's directories after the private /tmp and home,
// which would hide them otherwise, and runs argv last.
func TestJailArgs(t *testing.T) {
	j := &jail{bwrap: "bwrap", binds: []string{"--bind", "/cfg", "/cfg"}}
	a := j.args("/tmp/out/work/x", "/tmp/out/bin", []string{"claude", "-p"})
	at := func(s ...string) int {
		for i := range a {
			if slices.Equal(a[i:min(i+len(s), len(a))], s) {
				return i
			}
		}
		t.Fatalf("args lack %q: %q", s, a)
		return -1
	}
	tmp := at("--tmpfs", "/tmp")
	for _, b := range [][]string{{"--bind", "/tmp/out/work/x", "/tmp/out/work/x"}, {"--ro-bind", "/tmp/out/bin", "/tmp/out/bin"}, {"--bind", "/cfg", "/cfg"}} {
		if at(b...) < tmp {
			t.Errorf("%q comes before the tmpfs on /tmp, which hides it", b)
		}
	}
	at("--unshare-pid")
	at("--chdir", "/tmp/out/work/x")
	if !slices.Equal(a[len(a)-3:], []string{"--", "claude", "-p"}) {
		t.Errorf("args end %q, want -- claude -p", a[len(a)-3:])
	}
}

func TestJailEnv(t *testing.T) {
	env := jailEnv([]string{"HOME=/home/u", "PATH=/home/u/bin:/usr/bin", "PWD=/src/ovid", "OLDPWD=/src", "TMPDIR=/home/u/tmp", "AWS_PROFILE=p"}, "/b:/usr/bin", "/w")
	want := []string{"HOME=/home/u", "AWS_PROFILE=p", "PATH=/b:/usr/bin", "TMPDIR=/tmp", "PWD=/w"}
	for _, kv := range want {
		if !slices.Contains(env, kv) {
			t.Errorf("jailEnv lacks %s: %q", kv, env)
		}
	}
	for _, kv := range env {
		if strings.Contains(kv, "/src") || strings.HasPrefix(kv, "TMPDIR=/home") {
			t.Errorf("jailEnv keeps %s", kv)
		}
	}
}

// In the jail a shell sees its work directory and ovid, and nothing of
// the output directory, the other runs, or the home directory.
func TestJail(t *testing.T) {
	if runtime.GOOS != "linux" {
		if _, err := newJail("claude", t.TempDir()); err == nil || !strings.Contains(err.Error(), "Linux") {
			t.Errorf("newJail on %s = %v, want an error that says it needs Linux", runtime.GOOS, err)
		}
		t.Skip("bubblewrap needs Linux")
	}
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("no bwrap")
	}
	if err := exec.Command(bw, "--ro-bind", "/", "/", "--unshare-pid", "--", "true").Run(); err != nil {
		t.Skipf("bwrap cannot make a namespace here: %v", err)
	}
	home := t.TempDir()
	out := filepath.Join(home, "out:v1:0")
	work := filepath.Join(out, "work", "01-create-1")
	other := filepath.Join(out, "work", "01-create-2")
	bin, tmp, err := binDir(out) // out holds a ':'
	if err != nil || !tmp {
		t.Fatalf("binDir(%q) = %q, %v, %v", out, bin, tmp, err)
	}
	defer os.RemoveAll(bin)
	for _, d := range []string{work, other, bin, filepath.Join(home, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "ovid"), "#!/bin/sh\necho ovid ran\n", 0o755)
	write(filepath.Join(out, "results.jsonl"), "{}\n", 0o644)
	write(filepath.Join(other, "goal.json"), "{}\n", 0o644)
	write(filepath.Join(home, "src", "secret"), "x\n", 0o644)
	write(filepath.Join(work, "main.ov"), "package m\n", 0o644)

	j := &jail{bwrap: bw, exe: "/bin/sh"}
	script := `
ovid
cat main.ov
echo made > new.txt && echo wrote work
touch ` + bin + `/x 2>/dev/null || echo bin read-only
for p in ` + out + `/results.jsonl ` + other + ` ` + home + `/src/secret; do test -e "$p" && echo "SEES $p"; done
ls ` + out + ` | tr '\n' ' '; echo
echo pid1=$(cat /proc/1/comm)
`
	cmd := j.command(context.Background(), work, bin, []string{"/bin/sh", "-c", script})
	cmd.Env = jailEnv(childEnv(bin), j.pathEnv(bin), work)
	b, err := cmd.CombinedOutput()
	got := string(b)
	if err != nil {
		t.Fatalf("jail: %v\n%s", err, got)
	}
	for _, want := range []string{"ovid ran\n", "package m\n", "wrote work\n", "bin read-only\n", "\nwork \n"} {
		if !strings.Contains(got, want) {
			t.Errorf("jail output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SEES") || strings.Contains(got, "pid1=systemd") || strings.Contains(got, "pid1=init") {
		t.Errorf("jail sees outside:\n%s", got)
	}
	if b, err := os.ReadFile(filepath.Join(work, "new.txt")); err != nil || string(b) != "made\n" {
		t.Errorf("work/new.txt = %q, %v; want what the jail wrote", b, err)
	}
}
