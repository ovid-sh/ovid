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
	OvidCmds map[string]int `json:"ovid_cmds,omitempty"`
	// DiagCodes counts the diagnostics, by code, that the ovid calls'
	// output showed the agents (unused_result, type_mismatch, ...).
	DiagCodes map[string]int `json:"diag_codes,omitempty"`
	// Static is a scan of the module's program text the run left.
	Static    Static  `json:"static"`
	Failed    int     `json:"failed"`
	ReadB     int     `json:"bytes_read"`
	WriteB    int     `json:"bytes_written"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
	Seconds   float64 `json:"seconds"`
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
	DiagCodes map[string]int `json:"diag_codes,omitempty"`
	Failed    int            `json:"failed"`
	ReadB     int            `json:"bytes_read"`
	WriteB    int            `json:"bytes_written"`
	TokensIn  int            `json:"tokens_in"`
	TokensOut int            `json:"tokens_out"`
	CostUSD   float64        `json:"cost_usd"`
	Seconds   float64        `json:"seconds"`
	Outside   []string       `json:"outside,omitempty"`
}

// Static counts what error handling the module's .ov files (less its
// _test.ov files) spell, comments and string literals left out: each _
// that discards a result, the lines that call ErrText, and the lines that
// write to standard error (Eprint, Stderr, or a Write, WriteN, or WriteInt
// to fd 2).
type Static struct {
	Discards int `json:"discards"`
	ErrText  int `json:"errtext_lines"`
	Stderr   int `json:"stderr_lines"`
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
	r.Static = scan(filepath.Join(work, t.Goal.Root))
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
		for k, n := range a.DiagCodes {
			if r.DiagCodes == nil {
				r.DiagCodes = map[string]int{}
			}
			r.DiagCodes[k] += n
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

// ovidSub finds each ovid invocation's subcommand in a shell command,
// past any leading -C DIR (ovid -C mod check), DIR quoted or not.
var ovidSub = regexp.MustCompile(`(?:^|[\s;&|(])ovid\s+(?:-C\s+(?:"[^"]*"|'[^']*'|\S+)\s+)*([a-z]+)`)

// ovidSubs counts the ovid subcommands in a shell command.
func ovidSubs(command string, into map[string]int) {
	for _, m := range ovidSub.FindAllStringSubmatch(command, -1) {
		into[m[1]]++
	}
}

// diagCodes counts the diagnostics in an ovid command's output, one JSON
// record a line, {"fact":"error","code":...}; other lines are skipped, so
// output an agent filtered or cut short counts what is left of it.
func diagCodes(out string, into map[string]int) {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "{") {
			continue
		}
		var d struct{ Fact, Code string }
		if json.Unmarshal([]byte(ln), &d) == nil && d.Fact == "error" && d.Code != "" {
			into[d.Code]++
		}
	}
}

var (
	errText = regexp.MustCompile(`\bErrText\s*\(`)
	stderr  = regexp.MustCompile(`\b(Eprint|Stderr)\s*\(|\b(Write|WriteN)\s*\(\s*2\s*,|\bWriteInt\s*\([^,()]*,\s*2\s*,`)
)

// scan counts Static over the .ov files under dir.
func scan(dir string) Static {
	var s Static
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ov") || strings.HasSuffix(p, "_test.ov") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, ln := range strings.Split(string(b), "\n") {
			ln = code(ln)
			s.Discards += discards(ln)
			if errText.MatchString(ln) {
				s.ErrText++
			}
			if stderr.MatchString(ln) {
				s.Stderr++
			}
		}
		return nil
	})
	return s
}

// discards counts the _ that stand alone in ln, as a name, not in one.
func discards(ln string) int {
	word := func(i int) bool {
		if i < 0 || i >= len(ln) {
			return false
		}
		c := ln[i]
		return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
	}
	n := 0
	for i := range len(ln) {
		if ln[i] == '_' && !word(i-1) && !word(i+1) {
			n++
		}
	}
	return n
}

// code is a line of Ovid without its comment and with each string
// literal's contents blanked.
func code(ln string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(ln); i++ {
		c := ln[i]
		switch {
		case in && c == '\\':
			i++
		case in && c == '"':
			in = false
			b.WriteByte(c)
		case in:
		case c == '"':
			in = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(ln) && ln[i+1] == '/':
			return b.String()
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

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
	ovidUse := map[string]bool{} // tool_use ids of the Bash calls that ran ovid
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
					Type      string
					ID        string
					ToolUseID string `json:"tool_use_id"`
					Name      string
					Input     json.RawMessage
					Content   json.RawMessage
					IsError   bool `json:"is_error"`
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
					if a.OvidCmds == nil {
						a.OvidCmds = map[string]int{}
					}
					ovidSubs(in.Command, a.OvidCmds)
					ovidUse[c.ID] = true
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
				text := resultText(c.Content)
				a.ReadB += len(text)
				if ovidUse[c.ToolUseID] {
					if a.DiagCodes == nil {
						a.DiagCodes = map[string]int{}
					}
					diagCodes(text, a.DiagCodes)
					if len(a.DiagCodes) == 0 {
						a.DiagCodes = nil
					}
				}
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

// namesDir reports whether s names dir itself or something under it. dir
// must stand as a whole word of the shell command or JSON string: what
// comes before it and what follows it (unless that is a / into it) must be
// something that ends a token, not a byte that could be part of a longer
// path (/var/tmp/ovid, /tmp/ovid-out, /tmp/ovid+copy).
func namesDir(s, dir string) bool {
	delim := func(c byte) bool { return strings.IndexByte(" \t\r\n\"'`;&|()<>=:", c) >= 0 }
	for from := 0; ; {
		i := strings.Index(s[from:], dir)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(dir)
		if (i == 0 || delim(s[i-1])) && (end == len(s) || s[end] == '/' || delim(s[end])) {
			return true
		}
		from = i + 1
	}
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
	fmt.Println("Medians of the passing runs, except diag codes: the diagnostics the agents' ovid calls showed, summed over all runs.")
	fmt.Println()
	fmt.Println("| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds | diag codes | `_` | ErrText | stderr writes |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
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
		fmt.Printf("| %s | %d/%d | %s | %s | %s | %s | %s | %s | %s | $%s | %s | %s | %s | %s | %s |\n", k, len(ok), len(rs),
			med(func(r Result) float64 { return float64(r.Calls) }),
			med(func(r Result) float64 { return float64(r.OvidCalls) }),
			med(func(r Result) float64 { return float64(r.Failed) }),
			med(func(r Result) float64 { return float64(r.ReadB) }),
			med(func(r Result) float64 { return float64(r.WriteB) }),
			med(func(r Result) float64 { return float64(r.TokensIn) }),
			med(func(r Result) float64 { return float64(r.TokensOut) }),
			med(func(r Result) float64 { return r.CostUSD }),
			med(func(r Result) float64 { return float64(int64(r.Seconds)) }),
			codes(rs),
			med(func(r Result) float64 { return float64(r.Static.Discards) }),
			med(func(r Result) float64 { return float64(r.Static.ErrText) }),
			med(func(r Result) float64 { return float64(r.Static.Stderr) }))
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

// codes sums the runs' diag codes as "code n" pairs, most hit first.
func codes(rs []Result) string {
	sum := map[string]int{}
	for _, r := range rs {
		for k, n := range r.DiagCodes {
			sum[k] += n
		}
	}
	if len(sum) == 0 {
		return "–"
	}
	var ks []string
	for k := range sum {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if sum[ks[i]] != sum[ks[j]] {
			return sum[ks[i]] > sum[ks[j]]
		}
		return ks[i] < ks[j]
	})
	for i, k := range ks {
		ks[i] = fmt.Sprintf("%s %d", k, sum[k])
	}
	return strings.Join(ks, ", ")
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
