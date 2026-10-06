# Agent feedback

Results of the agent exercise in `tests/agent/` (how to run it:
[tests/agent/README.md](../tests/agent/README.md)). Re-run it when a
command's output or defaults change, and add the run here, newest first.
Each run's raw records are in `docs/agent-runs/`.

## Current state (verified 2026-10-06)

The four harder tasks (12–15) separate the models where the first eleven
did not. `claude-haiku-4-5` passes 9 of 20: it accepts integers that do
not fit in i64 in `12-sortn` (0/5: four runs have no overflow check, one
an incomplete one), and in `13-stock` fixes only the bug the
task reports (0/5). `claude-opus-5-5` and `claude-sonnet-5-5` pass 20 of
20 each; sonnet costs less than half of what opus does ($1.51 against
$3.65) in fewer calls. Sonnet remains the cheapest model that passes
everything.

`ovid`'s agent commands do not pay off on a large module, because agents
do not reach for them: given a copy of prog/ (7.6k lines) in
`15-selfhost-messages`, none of the 15 runs called `ovid outline`, `refs`,
or `grep`, one called `ovid show`, and none edited through ovid. They
navigated with `grep`, `sed -n`, and `cat` and edited with python, `sed`,
or Edit, reading 29–93 KB of the 180 KB.

That was the cheaper choice, not a habit to correct: told to navigate
through ovid (`preamble.guided.md`), sonnet did, and the task cost a third
more, because `show`, `grep`, and `outline` printed 1.7 to 6.2 times what
the shell tools print (611c7e2, below). With the read commands printing
text by default (#125 at 68194b7, below), the guided runs cost no more
than the plain ones ($0.14 against $0.15 median, 28 KB read against
30 KB), while using `show` 38 times, `grep` 23, and `outline` 13 in five
runs. Whether agents reach for them unprompted, and whether that beats
the shell on a task that needs the ids, is not yet measured.

Editing through ovid is a separate matter: in both rounds the guided
agents changed code with python and `sed` (`ovid insert` in one run of
ten; no `replace` or `edit`), even when told to. The hashes `show` prints
are paid for and not used on this task, where the edits are a dozen
message strings in one file.

In the small modules ovid's guard still does its job: in `14-rename-vs-call`
an edit that named the function the other agent had just renamed was
refused with `unknown_name` and nothing written, and the agent re-read and
used the new name (opus run 1).

The replay is exercised by agents, not only by the deterministic test:
told to retry a lost `ovid edit` first, every agent in `07-replay` got
`stale`, confirmed the change was already in, and made it no second time
(d8c0be0, below). The two problems the b4c231f run found are fixed by #100
(#98, #99), not yet re-run with a model.

## 2026-10-06, 68194b7: the same two preambles, with the read commands printing text

The round of 611c7e2 (below) repeated on the branch of #125, where
`outline`, `refs`, `grep`, and `show` print text by default: `show` of a
230-line func is 1.01 times its text, `grep` 1.18 times `grep -rn`,
`outline` of a package 1.23 times `grep -n '^func\|^type\|^const'`. The
guided preamble is the corrected one (it says `show --ids` for statement
ids and that the declaration's hash is the guard). Same task, model,
budget, and host; $1.57 in all. Records:
`2026-10-06-68194b7-nav-{plain,guided}-sonnet.jsonl`.

Medians over the five runs of each:

| preamble | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| plain | 5/5 | 17 | 9 | 1 | 30312 | 6977 | 216090 | 5728 | $0.15 | 52 |
| guided | 5/5 | 16 | 14 | 0 | 28062 | 6131 | 188282 | 5525 | $0.14 | 51 |

ovid calls by subcommand and shell tools, summed over the five runs:

| | check | build | test | help | outline | show | grep | refs | insert | shell grep | sed | cat | python3 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| plain | 34 | 7 | 8 | 10 | 2 | 0 | 1 | 0 | 0 | 47 | 32 | 28 | 16 |
| guided | 28 | 5 | 6 | 9 | 13 | 38 | 23 | 0 | 0 | 15 | 17 | 31 | 5 |

What it shows:

- **The cost gap is gone.** Guided runs now read slightly less and cost
  slightly less than plain ones (the difference is within the spread of
  five runs; the previous round's third more is not). The guided agents
  used ovid's read commands as much as before (`show` 38 against 39,
  `grep` 23 against 24) and shell `grep` as little (15 against 16).
- **Reading is where the commands can compete; editing is not, yet.** No
  guided run used `replace`, `insert`, `append`, `delete`, or `edit`;
  python fell from 13 uses to 5 and `sed` from 23 to 17, so the agents
  edited less by hand but not through ovid. The task's edits are string
  literals inside one file's functions; an id-addressed edit has nothing
  to offer over `sed` there, so this task cannot show whether the edit
  commands pay. A task whose edits are spread across files, or race
  another writer, can (14-rename-vs-call does, in the small).
- **Plain runs did not change.** $0.15 against $0.15, 30 KB against
  30 KB: the agents that read with `grep` and `sed` were not affected,
  as expected.

## 2026-10-05, 611c7e2: does telling agents to use ovid's read commands pay?

`15-selfhost-messages` only, five runs with each of two preambles, model
`us.anthropic.claude-sonnet-5-5` (Bedrock), both at the same time on
starship, Claude Code 2.1.286, $2 budget per agent, $1.84 in all. The
plain preamble is `preamble.md`. The guided one, `preamble.guided.md`, adds
one paragraph: find your way with `ovid outline`, `show`, `refs`, and
`grep` rather than by reading files, and change code with `ovid replace`,
`insert`, `append`, `delete`, or `edit`. Records:
`2026-10-05-611c7e2-nav-{plain,guided}-sonnet.jsonl`.

Medians over all five runs of each (the plain column includes its one
failed run):

| preamble | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| plain | 4/5 | 13 | 10 | 1 | 30056 | 7504 | 166222 | 6471 | $0.15 | 56 |
| guided | 5/5 | 16 | 15 | 0 | 52036 | 8640 | 321401 | 7212 | $0.20 | 61 |

ovid calls by subcommand, summed over the five runs, and the shell tools,
all counted from the transcripts. (The records' `ovid_cmds` field has one
`check` fewer for guided run 4: it was written as `ovid -C DIR check`,
which the counter did not see past until the fix in this PR.)

| | check | build | test | help | outline | show | grep | refs | insert | shell grep | sed | cat | python3 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| plain | 28 | 10 | 8 | 12 | 2 | 0 | 0 | 0 | 0 | 40 | 23 | 18 | 11 |
| guided | 32 | 13 | 8 | 8 | 10 | 39 | 24 | 1 | 4 | 16 | 23 | 22 | 13 |

What it shows:

- **The guidance was followed for reading.** Every guided run used
  `show` (3–11 times), `grep` (3–6), and `outline` (1–3). Shell `grep`
  fell from 40 uses to 16.
- **Reading through ovid cost more.** Each guided run read more than each
  plain one (40–70 KB against 25–31 KB) and the median cost rose by a
  third. Five runs a side is too few to say anything about the pass rate
  (5/5 against 4/5).
- **The reason is the size of the output.** Measured on prog/ at this
  commit:

  | read | ovid | shell | ratio |
  |---|---|---|---|
  | one func, `fn:ovid/check.CheckExpr` (211 lines) | `show`: 11,937 B (`--plain`: 7,033 B) | the text: 7,033 B | 1.7 |
  | every line with `type_mismatch` | `grep`: 4,917 B | `grep -rn`: 1,316 B | 3.7 |
  | the decls of `ovid/check` | `outline --pkg`: 16,951 B | `grep -n '^func\|^type\|^const'`: 2,729 B | 6.2 |

  `show` appends an id comment to every line where a statement starts,
  `grep` wraps each match in a JSON object with its file, decl, and
  source line, and `outline` prints a JSON object per decl with its doc
  comment and hash. #125 proposes compact forms.
- **The guidance was not followed for editing.** `ovid insert` was called
  4 times, all in run 2; no run used `replace` or `edit`. Edits were made
  with python (13 uses) and `sed`, as without the guidance. The ids that
  `show` printed were paid for and not used.
- **The guided preamble misdescribed `show`, and is corrected since.** The
  wording these runs used (sha256 `153196d9…`) said `show` prints "the id
  and hash of each statement". It prints each statement's id and the
  declaration's hash, which is the guard an edit to those statements
  takes. An agent that looked for per-statement hashes would not have
  found them, so this run cannot say how much of the missing `ovid edit`
  use is the wording's doing. The corrected file names the declaration's
  hash and `--expect`; it has not been run.
- **A fault in the runner, fixed in this commit's PR.** All ten runs were
  flagged as looking outside their directory, because the output
  directories (`/tmp/ovid-exp-plain`, `-guided`) began with the
  checkout's path (`/tmp/ovid-exp`) and the test was a substring match.
  No tool input named the checkout itself, `tests/agent`, or `ovid-sh`.
  The table above therefore comes from the records directly, not from
  `-summary`, which leaves flagged runs out.

## 2026-10-05, 5d9f9d9 / 3d0c0f9: four harder tasks, opus, sonnet, and haiku

Tasks 12–15 (#110), five runs each, models `us.anthropic.claude-opus-5-5`,
`us.anthropic.claude-sonnet-5-5`, and
`us.anthropic.claude-haiku-4-5-20251001-v1:0` (Bedrock), the three at the
same time on starship, Claude Code 2.1.286, $5 budget per agent. #110 was
merged while they ran, and the runner builds ovid from the checkout for each
task, so some tasks ran at 3d0c0f9, which also has #107 and #109 (the
syscall list in the build receipt, and less indexing in check, build, run,
and test; both change prog/, the start of 15). Each task ran at one commit
per model: opus 12–13 and sonnet 12–15 and haiku 12 at 5d9f9d9, the rest
at 3d0c0f9. `go test ./tests/agent` passes at both. Records:
`2026-10-05-hard-{opus,sonnet,haiku}.jsonl`.

opus-5-5 ($3.65 in all):

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 12-sortn | 5/5 | 6 | 6 | 0 | 16070 | 6026 | 52023 | 4229 | $0.15 | 56 |
| 13-stock | 5/5 | 5 | 4 | 0 | 14547 | 1218 | 40456 | 1558 | $0.08 | 28 |
| 14-rename-vs-call | 5/5 | 11 | 10 | 0 | 25920 | 1663 | 69391 | 1738 | $0.11 | 71 |
| 15-selfhost-messages | 5/5 | 17 | 11 | 4 | 48223 | 8853 | 356326 | 6803 | $0.35 | 105 |

sonnet-5-5 ($1.51 in all):

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 12-sortn | 5/5 | 3 | 3 | 0 | 15458 | 3682 | 26069 | 3099 | $0.06 | 25 |
| 13-stock | 5/5 | 4 | 3 | 0 | 9855 | 905 | 18043 | 1009 | $0.03 | 10 |
| 14-rename-vs-call | 5/5 | 6 | 6 | 0 | 12787 | 1111 | 34685 | 1435 | $0.04 | 11 |
| 15-selfhost-messages | 5/5 | 17 | 11 | 1 | 29111 | 9156 | 245982 | 7700 | $0.18 | 61 |

haiku-4-5 ($5.56 in all, $2.28 of it on the 11 failed runs):

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 12-sortn | 0/5 | – | – | – | – | – | – | – | – | – |
| 13-stock | 0/5 | – | – | – | – | – | – | – | – | – |
| 14-rename-vs-call | 5/5 | 28 | 13 | 2 | 11372 | 2780 | 122369 | 3773 | $0.08 | 26 |
| 15-selfhost-messages | 4/5 | 94 | 24 | 6.5 | 92932 | 52504 | 4559703 | 31509 | $0.70 | 260 |

What the transcripts show:

- **`12-sortn`, haiku: no working overflow check, and a report that says
  otherwise.** All five runs accept `9223372036854775808`,
  `-9223372036854775809` (runs 7 and 8 of the goal), and in four of them
  `99999999999999999999` (run 9), printing the wrapped value with exit 0.
  Only run 1 wrote a check (`value > 922337203685477580` before the
  multiply), which misses the last digit. Run 2 ended with "correctly
  handles edge cases including ... boundary values for i64". Every other
  edge case (`-0`, `+1`, spaces, CR, a missing final newline) passed, and
  so did the 2M-value sort. Runs took 36–90 calls, many of them tests of
  speed after early versions timed out.
- **`13-stock` tests reading, not debugging.** All ten opus and sonnet runs
  read the whole 156-line file with `cat` before running anything and fixed
  the three bugs in their first edit (one python rewrite; opus run 3 used
  three Edits). Haiku read it too (with Read) but reproduced the reported failure,
  fixed that one (the dropped last byte), and stopped; in all five runs the
  prefix compare and the short copy on growth remain. A run-time bug
  that separates opus from sonnet needs a module too large to read.
- **`14-rename-vs-call`**: every opus and sonnet run renamed with
  `ovid rename`, and haiku did in four of five runs. In opus run 1, agent
  b's `ovid edit` appended `Bulk` calling `Calc` after a's rename had
  landed; it was refused with `unknown_name` and nothing written, and b
  re-read and sent it with `WithTax`. Sonnet run 1's agent b wrote the call
  with python instead, `ovid check` failed, and it fixed the name with
  `sed`. All of haiku's b agents edited with Edit.
- **`15-selfhost-messages`: no agent used ovid to find its way.** Across
  15 runs the agents called `ovid outline`, `refs`, and `grep` zero times
  and `ovid show` once; they used `grep -n` over the `.ov` files (3–65
  times per run), `sed -n` ranges, and `cat`, and edited with python,
  `sed`, or Edit, never `ovid edit`. `ovid check`, `build`, and `test` were
  the only ovid commands they relied on. Nobody read all 180 KB (29 KB for
  sonnet, 48 KB for opus, 93 KB for haiku, medians). Haiku's failed run 5
  built its messages in the checker's output buffer `c.b`, so each message
  also appeared raw in stdout ahead of its JSON line.
- **Cost.** Sonnet costs 31–47% of what opus does on each task (per-task
  totals), 41% in all.
  Haiku is not cheap where it struggles: its `15-selfhost-messages` runs
  cost $3.15 against opus's $1.90, with 13 times the median input tokens.

## 2026-10-05, d8c0be0: the replay tasks, opus-5-5 and sonnet-5-5

`07-replay` was redesigned because no agent ever sent its lost request
again. The lost request is now `ovid edit fix.json`, three ops in a file in
the directory; in `07-replay` it ran before the reply was lost, and in the
new `11-lost-edit`, told the same story, it did not. Only these two tasks
were run, three times each per model, with the setup of the b4c231f run
below.

opus-5-5:

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 07-replay | 3/3 | 3 | 3 | 0 | 5025 | 269 | 12347 | 492 | $0.03 | 13 |
| 11-lost-edit | 3/3 | 2 | 2 | 0 | 3697 | 227 | 8082 | 439 | $0.02 | 11 |

sonnet-5-5:

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 07-replay | 3/3 | 2 | 2 | 1 | 1736 | 147 | 7056 | 304 | $0.01 | 5 |
| 11-lost-edit | 3/3 | 2 | 2 | 0 | 874 | 75 | 6636 | 252 | $0.01 | 5 |

What the transcripts show:

- **Left to choose, agents read before they retry.** In a run of the first
  version (e618a52, records not kept), which only said to make sure the
  change was made once, all six `07-replay` agents read the module, saw
  the change, and stopped without sending `fix.json`; all six
  `11-lost-edit` agents read it and then sent it. So the tasks now say to
  retry first, as a harness retrying a lost reply would, and measure what
  the agent does with the answer.
- **The stale refusal does not mislead.** In all six `07-replay` runs the
  first call was `ovid edit fix.json`, refused with `stale` (exit 2; the
  one failed call of each sonnet run). Each then read the module (`ovid show`,
  with `cat`, `ovid outline`, or `ovid grep` in some runs) and concluded
  the first edit had landed. None
  followed the hint's "use the ids and hashes it prints now" into a second
  edit, and none used `--force`.
- **`11-lost-edit`**: the retry applied the edit in every run, and every
  agent ran `ovid check` on the result.

## 2026-10-04, b4c231f: opus-5-5 and sonnet-5-5

Commit b4c231f, Claude Code 2.1.286, models `us.anthropic.claude-opus-5-5`
and `us.anthropic.claude-sonnet-5-5` (Bedrock; the `sonnet` alias maps to
sonnet-4-5 there, so the id was given), tools Bash, Read, Write, and Edit,
$2 budget per agent, three runs per task, both models at the same time on
starship. Medians over passing runs.

opus-5-5 ($1.34 in all):

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 01-create | 3/3 | 4 | 4 | 0 | 7772 | 1082 | 21981 | 753 | $0.04 | 12 |
| 02-fix-errors | 3/3 | 4 | 3 | 1 | 5813 | 443 | 18519 | 850 | $0.03 | 17 |
| 03-add-func | 3/3 | 3 | 2 | 0 | 7095 | 1035 | 16499 | 789 | $0.04 | 12 |
| 04-rename-func | 3/3 | 4 | 4 | 0 | 10104 | 414 | 24889 | 531 | $0.04 | 13 |
| 05-rename-type | 3/3 | 3 | 3 | 0 | 8710 | 210 | 17023 | 377 | $0.03 | 10 |
| 06-stale-hash | 3/3 | 3 | 3 | 0 | 4868 | 459 | 11999 | 622 | $0.03 | 14 |
| 07-replay | 3/3 | 2 | 2 | 0 | 2852 | 175 | 7948 | 320 | $0.02 | 8 |
| 08-two-writers | 3/3 | 8 | 8 | 0 | 11171 | 470 | 34956 | 722 | $0.05 | 14 |
| 09-same-func | 3/3 | 9 | 9 | 0 | 22642 | 691 | 56612 | 903 | $0.08 | 17 |
| 10-wc | 3/3 | 4 | 4 | 0 | 15512 | 2152 | 34165 | 1639 | $0.08 | 23 |

sonnet-5-5 ($0.56 in all):

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 01-create | 3/3 | 4 | 3 | 1 | 7525 | 626 | 21171 | 513 | $0.02 | 7 |
| 02-fix-errors | 3/3 | 3 | 2 | 1 | 3952 | 192 | 11980 | 402 | $0.01 | 6 |
| 03-add-func | 3/3 | 4 | 3 | 0 | 7057 | 940 | 21206 | 917 | $0.02 | 11 |
| 04-rename-func | 3/3 | 3 | 2 | 0 | 4567 | 185 | 12309 | 475 | $0.01 | 6 |
| 05-rename-type | 3/3 | 2 | 2 | 0 | 2901 | 102 | 7387 | 228 | $0.01 | 4 |
| 06-stale-hash | 3/3 | 3 | 3 | 0 | 4454 | 381 | 11690 | 528 | $0.01 | 7 |
| 07-replay | 3/3 | 1 | 1 | 0 | 402 | 47 | 3949 | 168 | $0.00 | 4 |
| 08-two-writers | 3/3 | 8 | 8 | 0 | 7659 | 528 | 28521 | 714 | $0.02 | 7 |
| 09-same-func | 3/3 | 9 | 9 | 0 | 13229 | 848 | 42220 | 1093 | $0.04 | 10 |
| 10-wc | 3/3 | 3 | 3 | 0 | 15070 | 1284 | 23415 | 1002 | $0.03 | 8 |

What the transcripts show:

- **`05-rename-type`**: all six runs used `ovid rename` once (sonnet with the
  bare name `Node`) and then ran `ovid check` and `ovid run`; none edited
  the file afterwards. In the d28570f run every agent had to repair the
  rename by hand.
- **`07-replay`**: as before, no agent sent the delete again; each ran
  `ovid show` and `ovid check` and stopped. The replay is covered only by
  the deterministic test.
- **The guard on statement edits**: in `08-two-writers` (runs 2 and 3, both
  agents) and `09-same-func` (three agents), sonnet's first edit of an
  `st:` id had no `--expect` and was refused with `expect_required`. All
  seven re-sent it with the hash from `ovid show`; none used `--force`.
  Opus always passed a hash.
- **Stale between agents**: in `09-same-func`, three opus agents and two
  sonnet agents got `stale`, re-read, and kept the other's change.
- **`replace` ignores `--before`**: sonnet's `09-same-func` run 3, agent a,
  sent `ovid replace st:team.Scale:1 --before st:team.Scale:1` meaning an
  insert. ovid replaced the `return` with the `if` and refused the result
  with `missing_return`; the agent then used `ovid insert`. Reproduced by
  hand: `ovid replace st:team.Scale:1 --after fn:team.main --dry-run`
  exits 0 and plans a plain replace.
- **`sed` instead of ovid**: in sonnet's `09-same-func` run 2, agent b
  changed `3` to `4` with `sed`. Agent a's insert, guarded by the hash it
  had read, was refused as stale, and its retry kept b's change.
- **The failed calls** are mostly the last command of a chain exiting
  nonzero by design (`ovid check` on the broken start of `02-fix-errors`,
  `cat *.ov` with no `.ov` in the module root), plus `ovid help replace`
  (exit 64) and one `stale` that the agent recovered from.

## 2026-10-04, d28570f

Commit d28570f, Claude Code 2.1.286, model `us.anthropic.claude-opus-5-5`
(Bedrock), tools Bash, Read, Write, and Edit, $2 budget per agent, three
runs per task, run on starship. Medians over passing runs:

| task | passed | calls | ovid calls | failed calls | bytes read | bytes written | tokens in | tokens out | cost | seconds |
|---|---|---|---|---|---|---|---|---|---|---|
| 01-create | 3/3 | 5 | 5 | 0 | 7704 | 1530 | 27494 | 1021 | $0.05 | 20 |
| 02-fix-errors | 3/3 | 4 | 3 | 1 | 5047 | 586 | 17753 | 869 | $0.03 | 19 |
| 03-add-func | 3/3 | 3 | 2 | 0 | 6546 | 898 | 15707 | 710 | $0.04 | 14 |
| 04-rename-func | 3/3 | 4 | 4 | 0 | 8681 | 450 | 22897 | 572 | $0.04 | 15 |
| 05-rename-type | 3/3 | 4 | 4 | 0 | 4742 | 328 | 16477 | 535 | $0.03 | 14 |
| 06-stale-hash | 3/3 | 3 | 3 | 0 | 3132 | 486 | 10837 | 646 | $0.03 | 13 |
| 07-replay | 3/3 | 1 | 1 | 0 | 306 | 47 | 3900 | 228 | $0.01 | 7 |
| 08-two-writers | 3/3 | 8 | 8 | 0 | 9154 | 799 | 34346 | 912 | $0.05 | 13 |
| 09-same-func | 3/3 | 10 | 10 | 0 | 20390 | 825 | 54552 | 1058 | $0.08 | 20 |
| 10-wc | 3/3 | 4 | 4 | 1 | 15423 | 2155 | 34178 | 1839 | $0.08 | 27 |

What the transcripts show:

- **The stale guard works between agents.** In `09-same-func` both agents
  edited `Scale` at once with `expect`. In two runs the second got
  `"error":"stale"`; its hint named the node the hash now belongs to, and the
  agent re-sent the edit to that id and kept the other's change. In the
  third, the second agent saw the change on re-reading before it wrote.
  Agents chose small edits (insert a statement, replace one literal) to
  stay out of each other's way.
- **`06-stale-hash` tested the guard once in three runs.** Run 2 sent the
  given hash, was refused as stale, and re-sent with the current one. Run 1
  ran `ovid show` first and used the current hash. Run 3 sent
  `ovid replace fn:price.Price` with no `--expect`, which a decl id allows
  (#22); it kept the bulk price only because it had read the function just
  before.
- **The failed calls are shell mistakes, not ovid's**: a `cd calc` from
  inside `calc`, and test files written to `/tmp/t1`, which already existed
  on the machine as a directory. All three `10-wc` agents put scratch files
  in `/tmp`, outside their directory.
- **`10-wc`**: all three runs read the file with `Open` and `Read`, not
  `ReadFile`, and wrote `main.ov` once; every later call was testing.

Limits of this run: one model; three runs per task, so a 3/3 says little
about rare failures; "failed calls" counts only tool errors and Bash
commands whose last step exited nonzero; nothing counts a tool that
answers `ok` and is wrong, which is what #21 did. Its `prompt_sha256`
fields hash the prompt with the run's directory filled in, so they differ
between runs; later runs hash the template.

What would make the exercise measure more: a smaller model, tasks on a
module too large to read whole (where `outline`, `show`, and `refs` should
pay off), a bug visible only at run time, and a check that the agent's
own commands did what their receipts claimed.
