package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShellCommands(t *testing.T) {
	for s, want := range map[string][][]string{
		"ls -la /home":                                      {{"ls", "-la", "/home"}},
		"cd m && cat 'a b.ov' | head -3":                    {{"cd", "m"}, {"cat", "a b.ov"}, {"head", "-3"}},
		`X=1 git -C "/x y" log; (find ~)`:                   {{"X=1", "git", "-C", "/x y", "log"}, {"find", "~"}},
		"echo $(ls /etc) `cat /x`":                          {{"echo"}, {"ls", "/etc"}, {"cat", "/x"}},
		"wc -l < /etc/passwd 2>/dev/null":                   {{"wc", "-l", "<", "/etc/passwd", "2", ">", "/dev/null"}},
		"ovid check 2>&1 >> log":                            {{"ovid", "check", "2", "1", ">", "log"}},
		"a\\ b c":                                           {{"a b", "c"}},
		"cat > f.ov <<'EOF'\nls /home\ncat /etc/x\nEOF\nls": {{"cat", ">", "f.ov"}, {"ls"}},
		"cat <<-END > f\n\tfind /\n\tEND\nfind .":           {{"cat", ">", "f"}, {"find", "."}},
		"grep x <<< /home":                                  {{"grep", "x", ">", "/home"}},
	} {
		if got := shellCommands(s); !reflect.DeepEqual(got, want) {
			t.Errorf("shellCommands(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestOutside(t *testing.T) {
	sc := func() *scope {
		return &scope{repo: "/src/ovid", work: "/tmp/out/work/01-create-1", home: "/home/u",
			allow: []string{"/tmp/out:0/bin"}, scratch: []string{"/tmp", "/var/tmp/"}, deny: []string{"/tmp/out"}}
	}
	bash := func(c string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"command": c})
		return b
	}
	file := func(p string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"file_path": p})
		return b
	}
	for _, c := range []struct {
		tool  string
		input json.RawMessage
		want  bool
	}{
		{"Bash", bash("ovid check"), false},
		{"Bash", bash("ls; ls -la .; cat m/main.ov; find . -name '*.ov'"), false},
		{"Bash", bash("ls /tmp/out/work/01-create-1/m"), false},
		{"Bash", bash("/tmp/out:0/bin/ovid check 2>/dev/null; ls /tmp/out:0/bin"), false},
		{"Bash", bash("cd m && ls .. && cat ../m/main.ov"), false},
		{"Bash", bash("git status"), false}, // no repository above work
		{"Bash", bash("echo /home > notes.txt"), false},
		{"Bash", bash("cat > m/main.ov <<'EOF'\n// see /usr/share\nls /home\nEOF"), false},
		{"Bash", bash("grep -rn 'func' . | sort"), false},
		{"Bash", bash("cd m; cd -; ls"), false},
		{"Bash", bash("cd /tmp 2>/dev/null; cd - >/dev/null; ls"), false},
		{"Bash", bash("ovid run -- a /nonexist/b; ovid run conf /nope"), false},
		// Scratch files in /tmp are the agent's own.
		{"Bash", bash("echo 7 > /tmp/in.txt && ovid run -- /tmp/in.txt < /tmp/in.txt"), false},
		{"Bash", bash("cd /tmp && rm -f a && cat a.txt; wc -l /var/tmp/x"), false},
		{"Write", file("/tmp/fix.json"), false},
		{"Read", file("/tmp/out/work/01-create-1/m/main.ov"), false},
		{"Write", file("m/new.ov"), false},
		// Outside.
		{"Bash", bash("ls /home"), true},
		{"Bash", bash("find ~ -name ovid"), true},
		{"Bash", bash("find / -name ovid 2>/dev/null | head"), true},
		{"Bash", bash("ls $HOME/ovid-eval"), true},
		{"Bash", bash("cat /etc/passwd"), true},
		{"Bash", bash("ls ../"), true},
		{"Bash", bash("ls /tmp/out/work"), true}, // the other runs
		{"Bash", bash("cat ../../results.jsonl"), true},
		{"Bash", bash("cd /tmp && ls"), true},
		{"Bash", bash("ls /tmp"), true},
		{"Bash", bash("find /var/tmp -name ovid"), true},
		{"Bash", bash("cat /tmp/ovid-agent-123/results.jsonl"), true}, // another run's
		{"Bash", bash("cat /tmp/out/results.jsonl"), true},
		{"Bash", bash("cd /tmp/out; ls"), true},
		{"Bash", bash("cd"), true},
		{"Bash", bash("git -C /src/other log"), true},
		{"Bash", bash("wc -l < /etc/hosts"), true},
		{"Bash", bash("/home/u/ovid-eval/e-try/bin/ovid check"), true}, // another variant's ovid
		{"Bash", bash("ovid -C /home/u/prog outline"), true},
		{"Bash", bash("ovid run -C /home/u/prog -- x"), true},
		{"Bash", bash("ls /src/ovid"), true}, // the repository
		{"Bash", bash("echo tests/agent"), true},
		{"Read", file("/home/u/.claude/CLAUDE.md"), true},
		{"Read", file("../../transcripts/x.jsonl"), true},
		{"Edit", file("/etc/hosts"), true},
	} {
		if got := sc().outside(c.tool, c.input); got != c.want {
			t.Errorf("outside(%s %s) = %v, want %v", c.tool, c.input, got, c.want)
		}
	}

	// The shell's directory persists from call to call, and comes back
	// to work when it left.
	s := sc()
	if s.outside("Bash", bash("cd m")) || s.outside("Bash", bash("cat ../m/main.ov")) || s.cwd != s.work+"/m" {
		t.Errorf("cd m, then cat ../m/main.ov: outside, or cwd %q", s.cwd)
	}
	if !s.outside("Bash", bash("cd /")) || s.outside("Bash", bash("ls m")) || s.cwd != s.work {
		t.Errorf("cd /: not outside, or cwd %q not put back", s.cwd)
	}

	// A git command where work sits in a checkout reads that checkout.
	s = sc()
	s.gitUp = true
	if !s.outside("Bash", bash("git log --oneline")) {
		t.Errorf("git log with a repository above work: not outside")
	}
}

func TestGitAbove(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "out", "work", "a")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if gitAbove(work) && !strings.HasPrefix(work, os.TempDir()) {
		t.Fatalf("gitAbove(%q) with no .git", work)
	}
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	if !gitAbove(work) {
		t.Errorf("gitAbove(%q) = false with %s/.git", work, dir)
	}
	os.Mkdir(filepath.Join(work, ".git"), 0o755)
	if gitAbove(work) {
		t.Errorf("gitAbove(%q) = true with its own .git", work)
	}
}
