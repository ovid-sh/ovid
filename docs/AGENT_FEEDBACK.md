# Agent feedback

Results of the agent exercise in `tests/agent/` (how to run it:
[tests/agent/README.md](../tests/agent/README.md)). Re-run it when a
command's output or defaults change, and add the run here, newest first.
Each run's raw records are in `docs/agent-runs/`.

## Current state (verified 2026-10-05)

The four harder tasks (12–15) separate the models where the first eleven
did not. `claude-haiku-4-5` passes 9 of 20: it never checks for i64
overflow in `12-sortn` (0/5), and in `13-stock` fixes only the bug the
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

In the small modules ovid's guard still does its job: in `14-rename-vs-call`
an edit that named the function the other agent had just renamed was
refused with `unknown_name` and nothing written, and the agent re-read and
used the new name (opus run 1).

The replay is exercised by agents, not only by the deterministic test:
told to retry a lost `ovid edit` first, every agent in `07-replay` got
`stale`, confirmed the change was already in, and made it no second time
(d8c0be0, below). The two problems the b4c231f run found are fixed by #100
(#98, #99), not yet re-run with a model.

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

- **`12-sortn`, haiku: no overflow check, and a report that says
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
- **Cost.** Sonnet costs 36–51% of what opus does on each task, 41% in all.
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
