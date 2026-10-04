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
  start/        the starting directory (absent: start empty)
  goal.json     what must hold afterwards
  solution.sh   a reference solution, run with ovid on PATH
```

`goal.json` (see `Goal` in `goal.go`):

| key | meaning |
|---|---|
| `root` | the module, relative to the work directory (default: the work directory) |
| `check` | `ovid check` reports no errors |
| `tests` | `ovid test` passes and these tests ran and passed |
| `inject` | test files the grader adds to a copy of the module before `ovid test`, so behaviour is checked independently of the agent's own tests |
| `runs` | the built program run with `args`, `stdin`, and `files` in its working directory, compared on `stdout` and `exit` |
| `contains`, `lacks` | regexps over the module's `.ov` files (`file` narrows to one, `count` wants an exact number) |
| `start_passes` | the start already meets the goal and the task is to leave it so |
| `xfail` | the issue that makes the reference solution fail today |

The grader works on a copy of the module and never trusts what the agent
says it did: `ok:true` from a rename is not a passed rename unless the
program still does what it did.

## Without a model

`go test ./tests/agent` runs in CI. For each task, the goal must fail on
`start/` (unless `start_passes`) and pass after `solution.sh`. A task
marked `xfail` must still fail; when its bug is fixed the test says so, and
the mark is removed. That checks both the graders and the commands the
solutions use (rename, a stale edit, a replayed edit, concurrent writers).

## With a model

```sh
go run ./tests/agent/run -n 3              # every task, three runs each
go run ./tests/agent/run -task wc -n 1     # one task
go run ./tests/agent/run -summary /tmp/ovid-agent-XXXX/results.jsonl
```

Each run gets a fresh copy of `start/` in a temp directory and one Claude
Code process per prompt (`claude -p --bare`, tools Bash, Read, Write, and
Edit, no settings, hooks, or CLAUDE.md), with the prompt `preamble.md` +
`task.md` and the ovid built from this checkout first on `PATH`. The two
prompts of a two-writer task run at the same time in the same directory.
The run records, per task and run: whether the goal passed and why not,
the files changed, tool calls, ovid calls, failed calls (a tool error or a
nonzero exit of a whole Bash command), bytes read (tool results) and
written (tool inputs), tokens, cost, time, and the model. With them it
records the commit, `ovid version`, the Claude Code version, the tools, the
budget, and hashes of the prompts. Transcripts stay in the output
directory; they are too large to commit.

A run whose tool inputs name the repository is listed as looking outside
its directory and left out of the medians: the agent could have read the
goals. The summary reports each task's pass rate and the medians of its
passing runs only, so a cheap failure does not count as progress.

`-model` picks the model (default: Claude Code's), `-budget` the spending
limit per agent (USD 2), `-timeout` the time limit per run (20 minutes).
