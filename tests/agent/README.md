# The agent exercise

Tasks an agent is given with nothing but the `ovid` binary, each with a
goal a program checks. They exist to show whether agents can use the
toolchain, and to notice when a change to a command's output or defaults
makes that worse. Re-run the exercise (below) when one changes, and record
the results in `docs/AGENT_FEEDBACK.md`.

## A task

```
NN-name/
  task.md       what the agent is told (task.b.md: a second agent, run at the same time)
  start_from    optional: a directory of this repository (e.g. prog) to start from, less its bin/
  start/        the starting directory (absent: start empty), copied over start_from's
  goal.json     what must hold afterwards
  inject/       optional: more inject files (below), by module-relative path
  solution.sh   a reference solution, run with ovid on PATH
```

`goal.json` (see `Goal` in `goal.go`):

| key | meaning |
|---|---|
| `root` | the module, relative to the work directory (default: the work directory) |
| `check` | `ovid check` reports no errors |
| `tests` | `ovid test` passes and these tests ran and passed |
| `inject` | test files the grader adds to a copy of the module before `ovid test`, so behaviour is checked independently of the agent's own tests; the files under the task's `inject/` are added too |
| `runs` | the built program run with `args`, `stdin`, and `files` in its working directory, compared on `stdout` and `exit` (`files` paths may name subdirectories); `stderr`, when set, is a regexp standard error must match; `after` maps paths to the content the run must leave there, `null` for none |
| `contains`, `lacks` | regexps over the module's `.ov` files (`file` narrows to one, `count` wants an exact number) |
| `start_passes` | the start already meets the goal and the task is to leave it so |
| `xfail` | `issue`, the bug that makes the reference solution fail today, and `problems`, exactly what the grader reports because of it |

The grader works on a copy of the module and never trusts what the agent
says it did: `ok:true` from a rename is not a passed rename unless the
program still does what it did. It bounds what the agent's code can make
it do: each ovid command gets two minutes (`ovid test` runs the agent's
tests), each run of the program ten seconds, and each keeps at most 1 MiB
of output.

## Without a model

`go test ./tests/agent` runs in CI. For each task, the goal must fail on
`start/` (unless `start_passes`) and pass after `solution.sh`, which must
exit 0. A task marked `xfail` must still fail, with exactly its listed
problems, so that a different failure is not mistaken for the known one;
when its bug is fixed the test says so, and the mark is removed. That checks both the graders and the commands the
solutions use (rename, a stale edit, a replayed edit, concurrent writers).
`07-replay` and `11-lost-edit` tell the agent the same story, an
`ovid edit fix.json` whose output was lost, and differ only in whether the
edit ran: in 07 it did, so the start already meets the goal, and in 11 it
did not. `TestReplayRequest` checks that they fit together: `fix.json` sent
to 11's start leaves exactly 07's, and sent again it is refused as stale
and writes nothing.

## With a model

```sh
go run ./tests/agent/run -n 3              # every task, three runs each
go run ./tests/agent/run -task wc -n 1     # one task
go run ./tests/agent/run -summary /tmp/ovid-agent-XXXX/results.jsonl
```

Each run gets a fresh copy of `start/` in a temp directory and one Claude
Code process per prompt (`claude -p --bare`, tools Bash, Read, Write, and
Edit, no settings, hooks, or CLAUDE.md), with the prompt `preamble.md` +
`task.md` and the ovid built from this checkout first on `PATH` (built
into `-out`'s `bin/`, or into a temporary directory when `-out` holds
`:`, `PATH`'s separator, as a Bedrock model id does). The two
prompts of a two-writer task run at the same time in the same directory.
The run records, per task and run: whether the goal passed and why not,
the files changed, tool calls, ovid calls, failed calls (a tool error or a
nonzero exit of a whole Bash command), bytes read (tool results) and
written (tool inputs), tokens, cost, time, and the model. With them it
records the commit, `ovid version`, the Claude Code version, the tools, the
budget, and hashes of the prompts. Transcripts stay in the output
directory; they are too large to commit.

A run that looked outside its directory is listed as such, in the line
printed for it and in the summary, and left out of the medians: the agent
could have read the goals, or another run's work. A tool input looks
outside when it names the repository or `tests/agent`; when a Read,
Write, or Edit names a file elsewhere; when a Bash command lists, reads,
or `cd`s elsewhere (`find`, `ls`, `cat`, `grep`, `git -C`, `ovid -C`, a
`<` redirection, ... on an absolute path, `~`, or `..` out of the
directory, the shell's directory followed from call to call); when it
runs an ovid other than the one it was given; and when it runs `git` in a
work directory that sits in a checkout. Files in `/tmp` are the agent's
own scratch, but not `/tmp` itself, `-out`, or another run's
`ovid-agent-*`. The summary reports each task's pass rate and the medians of its
passing runs only, so a cheap failure does not count as progress.

`-model` picks the model (default: Claude Code's), `-budget` the spending
limit per agent (USD 2), `-timeout` the time limit per run (20 minutes).
`-preamble FILE` uses another preamble in place of `preamble.md`, to
compare two wordings on the same tasks; `preamble.guided.md` is one that
tells the agent to navigate and edit through ovid. An ovid call is one
whether the agent ran `ovid` by its name or by a path (`./bin/ovid`,
`/tmp/x/bin/ovid`). Each record's
`ovid_cmds` counts its ovid calls by subcommand, `diag_codes` the
diagnostics their output showed the agent, by code, and `static` what the
module's program text (its `_test.ov` files left out) spells when the
agent is done: the `_` that discard a result, the lines that call
`ErrText`, and the lines that write to standard error.

## Error handling

`17-cp` to `20-parse` measure a way of handling errors rather than the
toolchain: their goals only run the program (and, in `20-parse`, tests),
so the same goal grades any spelling of errors. `17-cp` ignores failures,
of which the user reports one; `18-calc` must carry a failure found three
calls deep to `main`, with the line; `19-conf` does not check
(`unused_result`), and silencing a result with `_` checks but fails the
runs; `20-parse` asks for a func returning a value or an error. A
prototype of another spelling swaps in its own `start/` and `solution.sh`
for the tasks whose start spells errors, and its own
`20-parse/inject/num/zz_goal_test.ov`, whose `GoalParse` is the only
place that calls `ParseI64`. `diag_codes` and `static` are what to
compare besides pass rate and cost.
