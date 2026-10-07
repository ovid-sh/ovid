# Async POCs: the final run (2026-10-07)

Four ways for an Ovid program to wait on more than one thing (#75), built
far enough to measure and to write code in. The design of each and how it
felt are in its branch's `experiments/async/<X>/NOTES.md` (`async-poc/A`
to `async-poc/D`). This page is the measured comparison and what it
suggests.

**Setup.** starship (Ryzen 7 8745HS, 16 threads, Linux 7.2), server, load,
and upstream on loopback, one configuration at a time under the bench
lock, load average 0.25 at the start. Every POC branch was rebased onto
`async-poc/base` at `a2aeb3e` (both wasm host fixes in). 10 s per cell after
a 1 s warm-up. Natively each run waited for TIME_WAIT to drain first. Raw
lines: `final-2026-10-07/*.jsonl`. Wasm runs under `wrangler dev` (one
local workerd isolate), which caps throughput near 1.7k rps whatever the
program does: read its wasm numbers as latency and relative cost, not
capacity.

## Results

Native, W1 (one 10 ms upstream call per request):

| | c=1 p50 | c=100 rps | c=100 p99 | c=1000 rps | c=1000 p99 | RSS at c=1000 |
|---|---|---|---|---|---|---|
| baseline (blocking) | 10.4 ms | 96 | 1,052 ms | 46 (999 errors) | 9.9 s | 2 MB |
| **A** async/await | 10.4 ms | **8,555** | 13.6 ms | 13,490 | 208 ms | 92 MB |
| **B** event loop | 10.4 ms | 6,726 | 17.6 ms | 13,518 | 217 ms | 28 MB |
| **C** stackful tasks | 10.4 ms | 7,995 | 15.0 ms | 13,378 | 220 ms | **18 MB** |
| **D** pre-fork, N=64 | 10.5 ms | 5,934 | 22.1 ms | 5,924 | 176 ms | 132 MB |
| **D** pre-fork, N=256 | 10.4 ms | 9,110 | 12.4 ms | **15,812** | 174 ms | 522 MB |
| **D** fork per connection | 10.5 ms | 9,260 | 12.0 ms | 15,013 | 229 ms | 2,042 MB |

Native, W2 (ten 10 ms upstream calls per request):

| | c=1 p50 | c=10 rps | c=100 rps | c=100 p50 / p99 | c=1000 rps | RSS at c=1000 |
|---|---|---|---|---|---|---|
| baseline | 104 ms | 10 | 5 (100 errors) | 5.4 s / 10 s | 0 (all errors) | 2 MB |
| **A** | **10.7 ms** | 893 | 1,378 | 43 / 313 ms | 837 | 98 MB |
| **B** | **10.7 ms** | 864 | 1,373 | 42 / 348 ms | 844 | 46 MB |
| **C** | **10.7 ms** | 878 | 1,370 | 43 / 365 ms | 784 | 95 MB |
| **D** N=64 | 104 ms | 95 | 597 | 190 / 219 ms | 601 | 132 MB |
| **D** N=256 | 104 ms | 96 | 925 | 107 / 111 ms | 1,597 | 522 MB |
| **D** per connection | 103 ms | 95 | 921 | 108 / 112 ms | 1,765 | 2,208 MB |

Wasm under workerd (`wrangler dev`), W2:

| | c=1 p50 | c=10 rps | c=100 rps | c=1000 rps | c=1000 p99 |
|---|---|---|---|---|---|
| baseline (= D: an instance per request) | 115 ms | 86 | 728 | 1,278 | 1,069 ms |
| **A** | 13.0 ms | 677 | 1,715 | 1,377 | 1,253 ms |
| **B** | 13.0 ms | 667 | 1,724 | 1,325 | 1,282 ms |
| **C** | 13.0 ms | 669 | 1,528 | 1,103 | 1,994 ms |

W1 under wasm is the same for every POC (76 rps at c=1, about 1.7k at
c=100): one wait per request leaves nothing for the models to differ on.
workerd's RSS (1–3.7 GB) measures one 32 MiB heap and an 8 MiB request
buffer per live instance (D's NOTES), not the models.

Binary sizes: native baseline 11.9 KB, A 20.9, B 12.3, C 16.2, D 13.5;
wasm baseline 7.1 KB, A 13.2, B 7.3, C 9.0.

## What the numbers say

1. **A, B, and C perform the same.** Within one process they reach the same
   ceilings: about 13.5k rps on W1 and about 1.37k on W2 at c=100. W2 is 10.7
   ms at c=1 for all three, against the blocking 104 ms. The ceiling is not
   the model. It is one thread doing the kernel's work for every connection
   (W2 makes 11 TCP connections per request), and the W2 tails at c=100
   (p99 over 300 ms for all three) come from that connection churn. **Speed
   does not decide between A, B, and C. Cost and feel do.**
2. **Memory differs, and C is lowest**: 18 MB at 1,000 open connections, about
   12 KB per task with 64 KiB stacks of which only touched pages count. B's
   28 MB is its free lists. A's 92 MB is its per-connection arenas, which
   were not tuned, so it is not inherent to stackless code.
3. **D is the only one that uses more than one core**, and the only one with
   no concurrency inside a request. Pre-fork with N=256 beats every single
   process on W1 at c=1000 (15.8k rps) and on W2 at c=1000 (1.6k against
   about 0.8k). But it pays about 2 MB per process, and W2 stays at 104 ms a
   request, so at c=100 its latency is 2.5 times A, B, and C's. Throughput
   follows N divided by the request time exactly (N=64: 5.9k on W1, 600 on
   W2), so N must be sized for the peak.
4. **Under wasm, JSPI makes A, B, and C equivalent too**: 13 ms for W2
   against 115 ms. C falls behind at c=1000 (1,103 rps against about 1,350),
   since each request runs ten JSPI stacks plus a spawn round trip each.
   The two host fixes found along the way mattered more than the model.
   Without coalescing timers, every POC paid about 9 ms per W2 under
   workerd.

## What each cost, and how it felt

| | compiler | std | program (baseline 145 lines) | feel, in a line |
|---|---|---|---|---|
| A | **≈735** (lowering 617) | 420 | 150 | mechanical to port, but colouring spreads up the call chain and the I/O API doubles |
| B | 0 | 0 | **376** (15 funcs vs 6) | inverted control flow, manual lifetimes, three invariants checked only by reasoning; does not compose |
| C | 242 | 502 | 152 | reads like the blocking code; one i64 argument per task, two `Read`s, no preemption |
| D | 0 | 9 | unchanged | the program is untouched; fork "returns twice" stands in for spawn, and nothing checks the child exits |

The W2 handler in each (abridged):

```
// A
var t i64 = spawn upstream(io, up)        // ×10, handles kept in memory
var r i64 = await ovid/async.Join(t)      // ×10

// B: no single handler; onRead starts 10 Up structs, onUp steps each
// through CONNECTING → SENDING → RECEIVING, upDone counts them down
// and the last one formats the response.

// C
var g *ovid/task.Group = ovid/task.NewGroup(io)
ovid/task.Go(io, g, taskfn(call), up)     // ×10
if ovid/task.Wait(io, g) != 0 { return 1 }

// D: the baseline's loop, unchanged: ten calls one after another.
```

## Suggestion

**C for concurrency within a process, D's isolated workers (#75) for cores,
with B's loop as the runtime underneath C.**

- C performs like A and B, uses the least memory, and keeps code in the
  blocking style an agent reads top to bottom. It costs a third of A's
  compiler change and no colouring. Its scheduler *is* B's loop, so B is not
  thrown away: it becomes the implementation, not the programming model.
- D's numbers are the case for workers. A single process stops near 13.5k
  rps here; more processes go past it. Isolated workers each running C's
  scheduler would combine both. That combination was not measured.
- A is ruled out on cost, not speed: about 735 compiler lines (before
  `prog/`), a second I/O API, and lost `st:` ids in crashes, for no
  measured gain over C.

What C still has to answer before it is more than a POC:

- `prog/`: the stack switch and `taskfn` in the self-hosted compiler, and its
  assembler.
- A plain `ovid/io.Read` on a blocking fd inside a task stalls every task.
  Either the checker forbids it in task code, or `ovid/io`'s calls become
  task-aware.
- Task entry: one `i64` argument (the server packs `fd | up << 32`).
- Crash reports from inside a task (`ovid run`'s fault report) are not
  verified.
- No preemption, channels, cancellation, or timeouts yet.
- Under wasm, the spawn round trip makes C about 18% slower than A and B at
  c=1000.
