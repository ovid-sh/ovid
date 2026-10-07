# POC C: stackful tasks

## 1. Design

A **task** is an entry function, `func F(io *ovid/io.Cap, arg i64) i64`,
together with a Cap and a heap of its own. Code in a task looks like
blocking code: `ovid/task.Read`, `Write`, `Accept`, `Connect`, and `Sleep`
return when they are done. When the kernel says "not yet" (`E_AGAIN`),
they park the task on epoll and the scheduler runs another one.

Spawning is structured:

- `Spawn` returns a handle, and `Join` waits for it and returns its result.
- `Release` lets a task be freed when it ends, unjoined (for a server's
  connections).
- A `Group` (`Go`, `Wait`) handles a set of tasks.
- A task that returns waits for all its children before it ends.

`ovid/task.Run(io, taskfn(F), arg)` in `main` drives everything natively.

**Native.** Each task gets a 64 KiB mmap'd stack with a guard page below
it, and a 64 KiB first heap region. The `Task` record sits at the start of
that region, and its first six fields are a Cap's, so the `*Task` is the
`io` the entry function receives. The Go toolchain gains three builtins
(`prog/` is untouched):

- `swapstack(save, load)` compiles as a call to a stub. The stub pushes rbp
  and the callee-saved rbx and r12–r15, stores rsp at `save`, loads rsp
  from `load`, and pops the other task's registers. That set is exactly
  what compiled code keeps live across a call: `allocRegs` gives locals
  only callee-saved registers once a function calls anything, and the
  caller's temps are already spilled around a call.
- `taskinit(top, fn, cap, arg, exit)` lays out a fresh stack. The first
  switch into it pops cap/arg/fn/exit into r12–r15 and returns into a
  trampoline: `call fn(cap, arg)`, then `call exit(cap, result)`.
- `taskfn(F)` names the entry function (see "the choice" below).

The stubs are emitted only for programs that use them, so every other
binary stays byte-identical and `TestSelfHost` is unaffected. The
scheduler is ordinary Ovid in `std/ovid/task`: a ring of ready tasks, one
epoll instance, and one-shot registrations whose data is the task. Up to
1024 finished tasks keep their stack and region for reuse.

**Wasm.** No stack switching is needed. The backend generates a
`_task(fn, cap, arg)` export that dispatches on the `taskfn` index. The
host (`wasm/tasks.mjs`) answers two host ops:

- `HostSpawn` runs `WebAssembly.promising(_task)(…)` from the microtask
  queue, so the task starts once the spawner suspends.
- `HostJoin` returns that promise.

Several tasks are then suspended in one instance at once. That works
because Ovid's wasm keeps no stack in linear memory: every local is a wasm
local. `Spawn` picks the path at run time with `HostSpawn(0,0,0)`, which
returns 0 under a wasm host and `-ENOSYS` from a kernel. So the same
`ovid/task` source runs on both targets.

**Why a separate package.** The runtime first lived in `ovid/io`. That
broke `TestSelfHost` and `TestProgChecks` on Linux: the self-hosted
compiler parses all of `ovid/io` for every program, and it does not know
the new builtins. So the runtime lives in `ovid/task`, which only programs
that import it pull in, and the system calls it needs became plain
wrappers in `ovid/io` (`rt.ov`). The checker lets the shipped `ovid/task`
use `swapstack`/`taskinit`, and it treats `*ovid/task.*` as opaque
outside the runtime, the same way it treats `*ovid/io.*`.

**The choice: how a task names its entry.** I chose `taskfn(F)`, a
builtin resolved at compile time. The checker requires F to have the task
signature, records the reference as a use of F (so `refs` and `rename` see
it), and `reachable` follows it. The value is an opaque i64: a code
address natively, a dispatch index under wasm. The alternative was fork's
"returns twice" shape behind a helper, e.g. `if ovid/task.Fork(io) { …child…;
ovid/task.Exit(io, r) }`. That needs no compiler change, but the child's
code runs inline in the parent's function. The child would then share the
parent's locals, which is exactly the shared state tasks are meant to
avoid, and nothing would mark where it ends. `taskfn` makes every spawn
target visible to `refs`. An agent can list what may run concurrently by
asking for the references of a function.

**What it would mean for the language.** One builtin for programs
(`taskfn`) and two for the runtime only. No function colouring: any
function may call `ovid/task.Read`, and a function that can suspend is one
that takes the `io` of a task. `std` gains a second, task-aware set of
I/O calls (`ovid/task.Read` next to `ovid/io.Read`).

## 2. How it feels

The W2 handler, native (`native/server/server.ov`):

```ovid
// call is the task of one /w2 upstream call; arg is the upstream port.
func call(io *ovid/io.Cap, arg i64) i64 {
  return upstream(io, arg)
}

func handle(io *ovid/io.Cap, req *ovid/http.Request, res *ovid/http.Response, up i64) i64 {
  if ovid/http.PathIs(req, strptr("/w1"), 3) {
    if upstream(io, up) != 0 {
      return 1
    }
    ovid/http.Write(io, res, strptr("ok"), 2)
    return 0
  }
  if ovid/http.PathIs(req, strptr("/w2"), 3) {
    var g *ovid/task.Group = ovid/task.NewGroup(io)
    var i i64 = 0
    while i < FANOUT {
      ovid/task.Go(io, g, taskfn(call), up)
      i = i + 1
    }
    if ovid/task.Wait(io, g) != 0 {
      return 1
    }
    ovid/http.Write(io, res, strptr("ok "), 3)
    ovid/http.WriteInt(io, res, FANOUT)
    return 0
  }
  ovid/http.SetStatus(res, 404)
  return 0
}
```

`upstream` is baseline's, with `ovid/task.Connect/Write/Read` in place of
the `ovid/io` calls. The server as a whole differs from `baseline/` in 44
lines out of 150. The wasm handler is the same shape with
`ovid/task.Sleep(io, 10)` as the upstream call.

What was good:

- **The code reads top to bottom.** The connection handler, the parser
  call, and the upstream call are unchanged from the blocking server. No
  state machines, no callbacks, no `await`.
- **Concurrency is visible.** It shows up as a `Go`/`Wait` pair and a
  `taskfn(...)`, and every spawn target is a reference to a named function.
- **Freeing a task's memory is automatic and exact.** A task's heap is
  dropped when the task ends, so the per-connection `ResetHeap` dance from
  baseline disappears.
- **It worked the first time it ran.** The native server served
  `/w1`, `/w2`, and 404 correctly on its first run, and so did the wasm
  handler.

What was awkward:

- **One i64 argument.** With no closures and no struct values, the
  connection task gets its fd and the upstream port packed as
  `fd | (up << 32)`. Anything bigger means allocating a struct in the
  parent's heap and passing its address, with a lifetime the language
  cannot check (it works because tasks share an address space, which is
  the very thing isolation says not to rely on).
- **Handles cannot be stored.** `*ovid/task.Task` is opaque, which is
  right, since a handle cannot be forged. But with no arrays, a program
  cannot keep ten task handles of its own, so `Group` exists mainly to
  hold them. A language with typed arrays would not need it.
- **Two `Read`s.** Since there are no globals, a blocking call can find
  its scheduler only through the task's `io`, so the suspending calls are
  `ovid/task.Read(io, fd, …)` beside `ovid/io.Read(fd, …)`. Calling
  `ovid/io.Read` inside a task on a blocking fd stalls every task, and on
  a non-blocking fd it returns `E_AGAIN`. Nothing checks for either.
- **Stack size is a real limit.** With 64 KiB stacks, a recursion of 1,000
  small frames runs and 10,000 ends in SIGSEGV on the guard page. The
  guard turns overflow into a fault rather than corruption (verified), but
  recursive code such as the self-hosted compiler's would need a bigger
  stack or growable ones.
- **No preemption.** A task that computes without I/O starves the rest.
- **Debugging is untested.** A crash inside a task was not tested against
  `ovid run`'s fault report. The rbp chain ends at the trampoline (rbp 0),
  but nothing shows which task it was.
- **The host probe shows up in the receipt.** `Spawn` calls
  `HostSpawn(0,0,0)`, so a native build's `syscalls` lists 65552 and 65553,
  numbers no kernel has.

## 3. What it cost

`git diff --stat` against `async-poc/base`:

| area | files | lines |
|---|---|---|
| compiler: builtins (parse, check, native codegen + stubs, wasm `_task`, asm) | `internal/{syntax,check,compile,wasm,asm}`, `tool/rename.go` | +242 / −22 |
| std: the runtime | `std/ovid/task/task.ov` (335 non-comment lines), `std/ovid/io/rt.ov` | +502 |
| host (wasm) | `wasm/tasks.mjs`, `wasm/worker.js` | +59 |
| the programs | `native/server/server.ov` (127 code lines, baseline 118), `wasm/handler/handler.ov` (34, baseline 28) | +195 |
| tests | `internal/tool/task_test.go`, `tests/fail/unknown_package.ov` (the std package list) | +108 |

A real version would also need all of it in `prog/` (the stubs in
`prog/ovid/asm` and `cg`, the builtins in its parser and checker). That
would roughly double the compiler line count.

## 4. Smoke numbers (2 s runs on starship, not final)

Native (`bench-native.sh`, loopback; the upstream call is a fresh TCP
connection):

| path | c | rps | p50 ms | p99 ms | peak RSS |
|---|---|---|---|---|---|
| /w1 | 1 | 96 | 10.4 | 10.6 | 2.1 MB |
| /w1 | 100 | 7,395 | 13.4 | 16.7 | 3.3 MB |
| /w1 | 1000 | 12,687 | 78.4 | 104.5 | 14.1 MB |
| /w2 | 1 | 91 | 10.9 | 11.7 | 2.2 MB |
| /w2 | 100 | 1,350 | 60.2 | 253.9 | 11.3 MB |
| /w2 | 1000 | 814 | 1203 | 2126 | 94.1 MB |

For reference, baseline gives W1 96 rps and W2 9.7 rps at any
concurrency.

- **What bounds the server.** At W1/1000 and W2/100 it uses 0.7–0.8 of one
  core (its own user and kernel time), and the upstream simulator uses
  another 0.75.
- **Task reuse barely helps.** Keeping finished tasks' stacks and heaps
  for reuse moved W1/1000 from 12.7k to 13.6k rps and W2/100 from 1,350 to
  1,373. So mapping stacks is not the cost. One thread doing the kernel's
  connection churn is, at ~11 connections per W2 request.

Wasm (`bench-wasm.sh`, workerd under `wrangler dev`):

| path | c | rps | p50 ms | p99 ms | baseline-wasm rps / p50 |
|---|---|---|---|---|---|
| /w1 | 1 | 75 | 13.4 | 14.9 | 78 / 12.7 |
| /w1 | 100 | 1,768 | 54.3 | 107.5 | 1,829 / 53.9 |
| /w2 | 1 | 44 | 22.6 | 28.8 | 9 / 113.1 |
| /w2 | 100 | 1,291 | 74.8 | 123.2 | 712 / 129.8 |

Under node, `/w2` takes 11–12 ms. Under workerd it takes 22.6 ms, against
13.4 ms for a single sleep (W1). I did not find where the extra ~9 ms
goes; workerd's timers or ten 64 KiB `memory.grow`s per request are
candidates. workerd's RSS (0.25–2.3 GB) is dominated by each instance's
32 MiB first heap region, not by tasks.

**Memory per task, native.**

- **Chosen sizes.** Stack 64 KiB plus a 4 KiB guard, and a 64 KiB first
  heap region. Both are address space until touched.
- **Measured growth.** RSS grows ~12 KB per concurrent connection task,
  from 3.3 MB at W1/100 to 14.1 MB at W1/1000 (+10.8 MB per 900 tasks).
  At W2/1000, about 11,000 tasks are alive at once (1,000 connections plus
  ten calls each), and RSS is 94 MB, ~8.4 KB per task.
- **Under wasm,** each task costs one 64 KiB page of linear memory (reused
  after Join through the host's munmap free list), plus the JSPI stack V8
  keeps for a suspended call, which is not measured here.

## 5. Many cores, and what is left open

- **Scheduler per thread, tasks pinned.** A task already owns its heap
  and talks to others only through Join's result. So the next step is one
  scheduler per thread (clone with its own stack and epoll), with tasks
  never migrating. No allocator locking would be needed.
- **Isolation is by convention.** In one address space, nothing stops a
  task from passing an address into another task's heap (the i64 `arg`
  makes it easy). Only processes, or one wasm instance per task, isolate
  for real.
- **Processes work today.** Fork N processes after `Listen` with
  `SO_REUSEPORT`, each running its own `ovid/task.Run`. Not built here.

Unanswered:

- channels between tasks (only an i64 result passes today)
- cancellation and timeouts on a parked task
- preemption or a budget for CPU-bound tasks
- per-task stack size, or growable stacks
- which task a fault happened in
- how `Spawn` should tell native from wasm without a probe syscall
- the whole thing in `prog/`
