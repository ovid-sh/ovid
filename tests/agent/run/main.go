// Command run gives each task of the agent exercise to a model through
// Claude Code, grades what it leaves, and records the cost. See
// tests/agent/README.md.
//
//	go run ./tests/agent/run [-n 3] [-task wc] [-model M] [-out DIR]
//	go run ./tests/agent/run -summary DIR/results.jsonl
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ovid/tests/agent"
)

// Result is one line of results.jsonl: one run of one task.
type Result struct {
	Task      string   `json:"task"`
	Run       int      `json:"run"`
	Pass      bool     `json:"pass"`
	Problems  []string `json:"problems,omitempty"`
	Changed   []string `json:"changed"`
	Agents    []Agent  `json:"agents"`
	Calls     int      `json:"calls"`
	OvidCalls int      `json:"ovid_calls"`
	// OvidCmds counts those calls by subcommand (check, show, edit, ...).
	OvidCmds  map[string]int `json:"ovid_cmds,omitempty"`
	Failed    int            `json:"failed"`
	ReadB     int            `json:"bytes_read"`
	WriteB    int            `json:"bytes_written"`
	TokensIn  int            `json:"tokens_in"`
	TokensOut int            `json:"tokens_out"`
	CostUSD   float64        `json:"cost_usd"`
	Seconds   float64        `json:"seconds"`
	// Outside lists tool inputs that named the repository or the exercise:
	// a run that looked there is not a fair one.
	Outside []string `json:"outside,omitempty"`
	Env     Env      `json:"env"`
}

// Agent is what one Claude Code process reported.
type Agent struct {
	Prompt    string         `json:"prompt_sha256"` // of preamble.md and the task's prompt, before {{dir}} is filled in
	Model     string         `json:"model"`
	Stop      string         `json:"stop"` // the result's subtype: success, error_max_turns, ...
	Turns     int            `json:"turns"`
	Calls     int            `json:"calls"`
	OvidCalls int            `json:"ovid_calls"`
	OvidCmds  map[string]int `json:"ovid_cmds,omitempty"`
	Failed    int            `json:"failed"`
	ReadB     int            `json:"bytes_read"`
	WriteB    int            `json:"bytes_written"`
	TokensIn  int            `json:"tokens_in"`
	TokensOut int            `json:"tokens_out"`
	CostUSD   float64        `json:"cost_usd"`
	Seconds   float64        `json:"seconds"`
	Outside   []string       `json:"outside,omitempty"`
}

// Env is what the run depends on besides the task.
type Env struct {
	Date     string  `json:"date"`
	Commit   string  `json:"commit"`
	Ovid     string  `json:"ovid_version"`
	Claude   string  `json:"claude_version"`
	Tools    string  `json:"tools"`
	Budget   float64 `json:"budget_usd"`
	Preamble string  `json:"preamble_sha256"`
}

func main() {
	n := flag.Int("n", 1, "runs per task")
	only := flag.String("task", "", "only tasks whose name contains this")
	model := flag.String("model", "", "model for claude --model (default: its own)")
	out := flag.String("out", "", "directory for results and transcripts (default: a new temp dir)")
	budget := flag.Float64("budget", 2, "spending limit per agent, USD")
	timeout := flag.Duration("timeout", 20*time.Minute, "time limit per run")
	summary := flag.String("summary", "", "print a Markdown table of a results.jsonl and exit")
	preamble := flag.String("preamble", "", "the preamble file (default: tests/agent/preamble.md), to compare wordings")
	flag.Parse()
	if *summary != "" {
		if err := summarize(*summary); err != nil {
			fatal(err)
		}
		return
	}

	repo, err := gitOut("rev-parse", "--show-toplevel")
	if err != nil {
		fatal(err)
	}
	tasks, err := agent.Load(filepath.Join(repo, "tests", "agent"))
	if err != nil {
		fatal(err)
	}
	if *out == "" {
		if *out, err = os.MkdirTemp("", "ovid-agent-"); err != nil {
			fatal(err)
		}
	}
	bin := filepath.Join(*out, "bin")
	ovid := filepath.Join(bin, "ovid")
	if b, err := exec.Command("go", "build", "-o", ovid, "ovid/cmd/ovid").CombinedOutput(); err != nil {
		fatal(fmt.Errorf("go build: %v\n%s", err, b))
	}
	if *preamble == "" {
		*preamble = filepath.Join(repo, "tests", "agent", "preamble.md")
	}
	pre, err := os.ReadFile(*preamble)
	if err != nil {
		fatal(err)
	}
	const tools = "Bash,Read,Write,Edit"
	env := Env{
		Date:     time.Now().UTC().Format(time.RFC3339),
		Tools:    tools,
		Budget:   *budget,
		Preamble: sha(string(pre)),
	}
	env.Commit, _ = gitOut("rev-parse", "HEAD")
	if s, _ := gitOut("status", "--porcelain"); s != "" {
		env.Commit += "+dirty"
	}
	if b, err := exec.Command(ovid, "version").Output(); err == nil {
		env.Ovid = strings.TrimSpace(string(b))
	}
	if b, err := exec.Command("claude", "--version").Output(); err == nil {
		env.Claude = strings.TrimSpace(string(b))
	}

	rf, err := os.OpenFile(filepath.Join(*out, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fatal(err)
	}
	defer rf.Close()
	fmt.Fprintf(os.Stderr, "results in %s\n", *out)
	for _, t := range tasks {
		if !strings.Contains(t.Name, *only) {
			continue
		}
		for i := 1; i <= *n; i++ {
			r := runTask(t, i, repo, bin, ovid, string(pre), *model, *budget, *timeout, *out)
			r.Env = env
			b, _ := json.Marshal(r)
			rf.Write(append(b, '\n'))
			fmt.Fprintf(os.Stderr, "%s #%d pass=%v calls=%d failed=%d cost=$%.2f %.0fs %s\n",
				t.Name, i, r.Pass, r.Calls, r.Failed, r.CostUSD, r.Seconds, strings.Join(r.Problems, "; "))
		}
	}
}

func runTask(t agent.Task, i int, repo, bin, ovid, pre, model string, budget float64, timeout time.Duration, out string) Result {
	r := Result{Task: t.Name, Run: i}
	name := fmt.Sprintf("%s-%d", t.Name, i)
	work := filepath.Join(out, "work", name)
	if err := os.RemoveAll(work); err != nil { // a reused -out must not leak an earlier run's edits
		fatal(err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		fatal(err)
	}
	if err := t.Setup(work); err != nil {
		fatal(err)
	}
	before := agent.Snapshot(work)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	r.Agents = make([]Agent, len(t.Prompts))
	var wg sync.WaitGroup
	for k, p := range t.Prompts {
		wg.Add(1)
		go func(k int, p string) {
			defer wg.Done()
			prompt := strings.ReplaceAll(pre, "{{dir}}", work) + p
			tr := filepath.Join(out, "transcripts", fmt.Sprintf("%s-%c.jsonl", name, 'a'+k))
			r.Agents[k] = runAgent(ctx, work, bin, prompt, model, budget, tr, repo)
			r.Agents[k].Prompt = sha(pre + p)
		}(k, p)
	}
	wg.Wait()
	r.Seconds = time.Since(start).Seconds()
	r.Changed = agent.Changed(before, agent.Snapshot(work))
	r.Problems = agent.Grade(ovid, work, t.Goal)
	r.Pass = len(r.Problems) == 0
	for _, a := range r.Agents {
		r.Calls += a.Calls
		r.OvidCalls += a.OvidCalls
		for k, n := range a.OvidCmds {
			if r.OvidCmds == nil {
				r.OvidCmds = map[string]int{}
			}
			r.OvidCmds[k] += n
		}
		r.Failed += a.Failed
		r.ReadB += a.ReadB
		r.WriteB += a.WriteB
		r.TokensIn += a.TokensIn
		r.TokensOut += a.TokensOut
		r.CostUSD += a.CostUSD
		r.Outside = append(r.Outside, a.Outside...)
	}
	return r
}

var ovidCmd = regexp.MustCompile(`(^|[\s;&|(])ovid\s`)

// ovidSub finds each ovid invocation's subcommand in a shell command.
var ovidSub = regexp.MustCompile(`(?:^|[\s;&|(])ovid\s+([a-z]+)`)

// runAgent runs one Claude Code process in work and reads its stream.
func runAgent(ctx context.Context, work, bin, prompt, model string, budget float64, transcript, repo string) Agent {
	var a Agent
	args := []string{"-p", "--bare", "--output-format", "stream-json", "--verbose",
		"--tools", "Bash,Read,Write,Edit", "--permission-mode", "bypassPermissions",
		"--no-session-persistence", "--max-budget-usd", fmt.Sprint(budget)}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = work
	cmd.Env = childEnv(bin)
	os.MkdirAll(filepath.Dir(transcript), 0o755)
	tf, err := os.Create(transcript)
	if err != nil {
		fatal(err)
	}
	defer tf.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		a.Stop = "start: " + err.Error()
		return a
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		ln := sc.Bytes()
		tf.Write(append(ln, '\n'))
		var m struct {
			Type    string
			Subtype string
			Model   string
			Message struct {
				Content []struct {
					Type    string
					Name    string
					Input   json.RawMessage
					Content json.RawMessage
					IsError bool `json:"is_error"`
				}
			}
			NumTurns int     `json:"num_turns"`
			Cost     float64 `json:"total_cost_usd"`
			Usage    struct {
				In      int `json:"input_tokens"`
				Created int `json:"cache_creation_input_tokens"`
				Read    int `json:"cache_read_input_tokens"`
				Out     int `json:"output_tokens"`
			}
		}
		if json.Unmarshal(ln, &m) != nil {
			continue
		}
		switch m.Type {
		case "system":
			if m.Subtype == "init" {
				a.Model = m.Model
			}
		case "assistant":
			for _, c := range m.Message.Content {
				if c.Type != "tool_use" {
					continue
				}
				a.Calls++
				a.WriteB += len(c.Input)
				var in struct {
					Command string
				}
				json.Unmarshal(c.Input, &in)
				if c.Name == "Bash" && ovidCmd.MatchString(in.Command) {
					a.OvidCalls++
					for _, m := range ovidSub.FindAllStringSubmatch(in.Command, -1) {
						if a.OvidCmds == nil {
							a.OvidCmds = map[string]int{}
						}
						a.OvidCmds[m[1]]++
					}
				}
				s := string(c.Input)
				if namesDir(s, repo) || strings.Contains(s, "tests/agent") || strings.Contains(s, "ovid-sh") {
					a.Outside = append(a.Outside, cut(s, 200))
				}
			}
		case "user":
			for _, c := range m.Message.Content {
				if c.Type != "tool_result" {
					continue
				}
				a.ReadB += len(resultText(c.Content))
				if c.IsError {
					a.Failed++
				}
			}
		case "result":
			a.Stop = m.Subtype
			a.Turns = m.NumTurns
			a.CostUSD = m.Cost
			a.TokensIn = m.Usage.In + m.Usage.Created + m.Usage.Read
			a.TokensOut = m.Usage.Out
		}
	}
	err = cmd.Wait()
	a.Seconds = time.Since(start).Seconds()
	if a.Stop == "" {
		a.Stop = fmt.Sprintf("no result (%v): %s", err, cut(stderr.String(), 300))
	}
	return a
}

// resultText is a tool result's text, which is a string or a list of
// blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []struct{ Text string }
	json.Unmarshal(raw, &bs)
	var b strings.Builder
	for _, x := range bs {
		b.WriteString(x.Text)
	}
	return b.String()
}

// namesDir reports whether s names dir itself or something under it. A
// sibling whose name merely starts the same way (dir-out next to dir) is
// not a match.
func namesDir(s, dir string) bool {
	for i := strings.Index(s, dir); i >= 0; {
		rest := s[i+len(dir):]
		if rest == "" || strings.ContainsRune("/\"' \t\n\\;)", rune(rest[0])) {
			return true
		}
		j := strings.Index(rest, dir)
		if j < 0 {
			break
		}
		i += len(dir) + j
	}
	return false
}

// childEnv is this process's environment with ovid first on PATH and
// without the variables that would make claude think it is nested in the
// session that started this.
func childEnv(bin string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k := kv[:strings.IndexByte(kv, '=')]
		if k == "CLAUDECODE" || k == "CLAUDE_PID" || strings.HasPrefix(k, "CLAUDE_CODE_SESSION") ||
			strings.HasPrefix(k, "CLAUDE_CODE_MESSAGING") || k == "CLAUDE_CODE_CHILD_SESSION" || k == "CLAUDE_CODE_ENTRYPOINT" {
			continue
		}
		if k == "PATH" {
			kv = "PATH=" + bin + string(os.PathListSeparator) + kv[5:]
		}
		env = append(env, kv)
	}
	return env
}

// summarize prints one row per task: pass rate, and the medians of the
// passing runs' costs, so a cheap failure does not look like progress.
func summarize(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	by := map[string][]Result{}
	var env Env
	models := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r Result
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			return err
		}
		by[r.Task] = append(by[r.Task], r)
		env = r.Env
		for _, a := range r.Agents {
			models[a.Model] = true
		}
	}
	var names []string
	for k := range by {
		names = append(names, k)
	}
	sort.Strings(names)
	var ms []string
	for m := range models {
		ms = append(ms, m)
	}
	sort.Strings(ms)
	fmt.Printf("Commit %s, %s, model %s, tools %s, budget $%g per agent, run %s.\n\n",
		env.Commit, env.Claude, strings.Join(ms, ", "), env.Tools, env.Budget, env.Date[:10])
	fmt.Println("| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|")
	for _, k := range names {
		rs := by[k]
		var ok []Result
		for _, r := range rs {
			if r.Pass && len(r.Outside) == 0 {
				ok = append(ok, r)
			}
		}
		med := func(f func(Result) float64) string {
			if len(ok) == 0 {
				return "–"
			}
			v := make([]float64, len(ok))
			for i, r := range ok {
				v[i] = f(r)
			}
			sort.Float64s(v)
			x := v[len(v)/2]
			if len(v)%2 == 0 {
				x = (v[len(v)/2-1] + v[len(v)/2]) / 2
			}
			if x != float64(int64(x)) {
				return fmt.Sprintf("%.2f", x)
			}
			return fmt.Sprint(int64(x))
		}
		fmt.Printf("| %s | %d/%d | %s | %s | %s | %s | %s | %s | %s | $%s | %s |\n", k, len(ok), len(rs),
			med(func(r Result) float64 { return float64(r.Calls) }),
			med(func(r Result) float64 { return float64(r.OvidCalls) }),
			med(func(r Result) float64 { return float64(r.Failed) }),
			med(func(r Result) float64 { return float64(r.ReadB) }),
			med(func(r Result) float64 { return float64(r.WriteB) }),
			med(func(r Result) float64 { return float64(r.TokensIn) }),
			med(func(r Result) float64 { return float64(r.TokensOut) }),
			med(func(r Result) float64 { return r.CostUSD }),
			med(func(r Result) float64 { return float64(int64(r.Seconds)) }))
	}
	fmt.Println()
	for _, k := range names {
		for _, r := range by[k] {
			switch {
			case len(r.Outside) > 0:
				fmt.Printf("- %s #%d looked outside its directory: %s\n", k, r.Run, strings.Join(r.Outside, " | "))
			case !r.Pass:
				fmt.Printf("- %s #%d failed: %s\n", k, r.Run, strings.Join(r.Problems, "; "))
			}
		}
	}
	return nil
}

func gitOut(args ...string) (string, error) {
	b, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(b)), err
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "run:", err)
	os.Exit(1)
}
