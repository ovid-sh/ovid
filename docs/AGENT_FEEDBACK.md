# Agent feedback

Results of the agent exercise in `tests/agent/` (how to run it:
[tests/agent/README.md](../tests/agent/README.md)). Re-run it when a
command's output or defaults change, and add the run here, newest first.
Each run's raw records are in `docs/agent-runs/`.

## Current state (verified 2026-10-04)

One model, `claude-opus-5-5`, passes all ten tasks, 30 of 30 runs, at a
median of 1 to 10 tool calls and $0.01 to $0.08 per task. The tasks are a
floor, not a measure: at this level they do not tell a better toolchain
from a worse one, and only a weaker model or harder tasks would. Two of
them pass because the agent was careful, not because the toolchain was
right:

- **#21**: in all three runs of `05-rename-type`, `ovid rename` reported
  `ok:true, check_ok:true` and changed what the program prints (7 to 9). Each
  agent noticed only because it ran the program afterwards, and fixed the
  line by hand.
- **#22**: in `07-replay` no agent sent the lost delete again; each looked
  first. Sending it again would have deleted the surviving statement too,
  as the deterministic test shows. And in `06-stale-hash` one agent edited
  a function with no hash at all, which a decl id permits.

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
