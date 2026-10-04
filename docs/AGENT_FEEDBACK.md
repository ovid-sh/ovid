# Agent feedback

Results of the agent exercise in `tests/agent/` (how to run it:
[tests/agent/README.md](../tests/agent/README.md)). Re-run it when a
command's output or defaults change, and add the run here, newest first.
Each run's raw records are in `docs/agent-runs/`.

## Current state (verified 2026-10-04)

Two models, `claude-opus-5-5` and `claude-sonnet-5-5`, pass all ten tasks
against b4c231f, 30 of 30 runs each. Sonnet is cheaper ($0.56 for all 30,
against opus's $1.34) and as reliable here. The tasks remain a floor: they
do not separate these models, and harder tasks would.

The two bugs the first run hit are gone in practice, not only in the
deterministic test: every `05-rename-type` run got the rename right in one
`ovid rename` and none touched the file by hand (#21), and a statement
edit with no hash is now refused (#22), which sonnet hit and recovered from
seven times. What the runs still show:

- `ovid replace` accepts `--before` and `--after` and ignores them: an agent
  that meant to insert replaced instead. The check guard caught it this
  time only because the result lacked a `return`.
- `ovid help replace` is a usage error (exit 64): agents guess that each
  command is a help topic.
- Agents may skip ovid's edits and use `sed`; the hash guard then still
  protects the other writer, as in `09-same-func`.

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
