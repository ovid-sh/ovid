# POC A: async/await (stackless)

Branch `async-poc/A`. Native: `A/native` (`server <port> <upstream port>`).
Wasm: `A/wasm` (served by the default `wasm/worker.js`). `A/try` is a
no-I/O check of the lowering (prints 22).

## 1. Design

**Language.** `async func F(...) T` declares a func that only `await` or
`spawn` may call. `await f(...)` and `spawn f(...)` stand only as the whole
value of a `var`, an assignment, or an expression statement, inside an async
func. `await` has `f`'s type; `spawn` yields the task (an `i64`), which
`ovid/async.Join` awaits. An async func has one result (no `(T, i64)`); the
leaf awaitables return a negated error code instead. An entry
`async func main(io *ovid/io.Cap) i64` is allowed. New diagnostics:
`bad_async`, `bad_await`, `async_call`. `await`/`spawn` are keywords only
when a name follows, so existing locals with those names still parse.

Statement position only was chosen because it makes every suspension point
a statement boundary: the lowering never has to split an expression, and an
agent sees each wait on its own line (`refs`/`show` find them).

**Lowering** (`internal/async`, run after the checker, before either
backend). Each async func `F` becomes `F$resume(fr i64) i64` over a frame on
the heap: an `ovid/async.Frame` head (fn id, state, result, waiter, done,
queue link, scheduler, heap, wake value), then one 8-byte slot per param and
per local declaration. Block-scoped locals are hoisted and renamed
(`x$3`). The body is split at each `await` into numbered blocks run by
`while true { if $st == 0 {...} else { if $st == 1 ... } }`; `if` and `while`
that contain an await become block transitions, the ones that do not stay
structured. Suspending stores every local into its slot and returns 0;
`return v` stores the result and returns 1. Because Ovid has no function
values, the scheduler reaches resume functions through
`ovid/async.dispatch`, whose body the lowering writes: one branch per async
func, by the id stored in the frame. Two more intrinsics: `Self()` is the
frame, and `await Park()` returns to the scheduler unconditionally; the
runtime builds every leaf awaitable from those two.

`await G(a)` allocates G's frame, stores the arguments, and runs it at once
(`ovid/async.Start`); only if G suspends does the caller suspend too, with
itself as G's waiter. So an await that does not wait costs a frame
allocation and a call, not a trip through the scheduler. `spawn G(a)`
enqueues the frame. A callee whose first parameter is an `*ovid/io.Cap`
gets its frame from that heap; any other, from its caller's.

**Runtime** (`std/ovid/async`, 313 lines of code): a FIFO ready queue,
epoll (one-shot, re-armed per wait) natively, `HostSubmit`/`HostPoll` under
wasm (the default `worker.js` already runs the instance under JSPI, so
`HostPoll` can block). Leaf awaitables: `WaitFd`, `Sleep`, `Join`,
`Accept`, `Read`, `Write`, `Connect`. **Arenas** (`NewArena`, `ArenaCap`,
`Release`, backed by a new `ovid/io.SubCap`) give a connection a heap of its
own that is reset and pooled when it ends: without them a server that never
stops grows forever, since nothing is freed and `ResetHeap` assumes one
request at a time.

**A program without async funcs compiles exactly as before**: `Lower`
returns the program untouched (corpus, `TestSelfHost`, and
`TestCorpusWasm` pass). `prog/` was not changed.

## 2. How it feels

The W2 handler (native; the wasm one differs only in what `upstream` is):

```
async func handle(io *ovid/io.Cap, req *ovid/http.Request, res *ovid/http.Response, up i64) i64 {
  if ovid/http.PathIs(req, strptr("/w1"), 3) {
    var r i64 = await upstream(io, up)
    if r != 0 {
      return 1
    }
    ovid/http.Write(io, res, strptr("ok"), 2)
    return 0
  }
  if ovid/http.PathIs(req, strptr("/w2"), 3) {
    var tasks i64 = ovid/io.Alloc(io, FANOUT * 8)
    var i i64 = 0
    while i < FANOUT {
      var t i64 = spawn upstream(io, up)
      store64(tasks + (i * 8), t)
      i = i + 1
    }
    var failed i64 = 0
    i = 0
    while i < FANOUT {
      var r i64 = await ovid/async.Join(load64(tasks + (i * 8)))
      if r != 0 {
        failed = failed + 1
      }
      i = i + 1
    }
    if failed != 0 {
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

and `upstream` reads like the blocking baseline with `await` in front of
each I/O call:

```
async func upstream(io *ovid/io.Cap, port i64) i64 {
  var fd i64 = await ovid/async.Connect(io, port)
  ...
  var w i64 = await ovid/async.Write(fd, strptr("10\n"), 3)
  ...
    var n i64 = await ovid/async.Read(fd, buf + got, 8 - got)
```

Good:

- Porting the blocking baseline was mechanical: add `async`, put `await`
  before each I/O call and each call to an async func. The control flow,
  the `while` loops, and the early returns did not change. The native
  server is 134 lines of code against the baseline's 118.
- Every wait is visible on its own line, and every func that can wait says
  so in its signature: an agent can tell from `outline` which calls may
  suspend.
- The lowering worked the first time it ran (the `try` program and both
  servers), largely because statement-position awaits keep it simple.

Awkward:

- **Function colouring is real.** `handle` had to become async because it
  calls `upstream`; `serve` because it calls `handle`; the leaf
  `Read`/`Write` exist twice (`ovid/io` and `ovid/async`). A sync helper can
  never wait. With more `std/` this means a sync and an async copy of every
  I/O-touching package (`ovid/http`'s `ReadStdio` cannot be used by an async
  server; the native server reads the head itself).
- **Statement-only await** forces a temporary for every awaited value:
  `var t i64 = spawn upstream(io, up)` then `store64(..., t)`. Cheap to
  write, and arguably clearer, but it is noise.
- **No function values** shows through in the runtime: `Self()` and `Park()`
  are compiler intrinsics found by name, and `dispatch` is a stub whose body
  the compiler replaces. A program never sees this, but `std/` does.
- **Arenas are manual**: the accept loop does
  `var a *ovid/async.Arena = ovid/async.NewArena(ovid/async.Self())`, passes
  `ovid/async.ArenaCap(a)` as the task's first argument, and the task ends
  with `ovid/async.Release(ovid/async.Self(), a)`. Forgetting `Release` leaks
  an arena per connection; releasing early corrupts the next connection. The
  "first parameter `*Cap` picks the frame's heap" rule is implicit.
- **Debugging**: a crash in an async func is reported in `F$resume`, and
  the native code map marks no statement ids for lowered code, so the
  `st:` id of the faulting line is lost. Stack traces would show the
  scheduler, not the logical call chain (the waiter links hold it).
- `await` of a two-result func is impossible, so the `(T, i64)` convention
  breaks at the async boundary: results are "value or negated errno".

## 3. What it cost

Against `async-poc/base` (`git diff --stat`), lines added:

| area | files | lines (code, without comments/blank) |
|---|---|---|
| **compiler: lowering pass** | `internal/async/lower.go` | 617 (526) |
| **compiler: syntax, checker, IR** | `internal/syntax/parse.go`, `internal/check/check.go`, `internal/ir/ir.go` | +32, +56, +3 (the rest of `ir.go`'s 13 is gofmt realignment) |
| compiler: wiring | `internal/tool/lower.go`, `tool.go`, `test.go` | +27, ~9 changed |
| **compiler total** | | **≈ 735** |
| std: runtime | `std/ovid/async/async.ov` | 408 (313) |
| std: `ovid/io.SubCap` | `std/ovid/io/io.ov` | +12 |
| host | — | 0 (the base's `runAsync` sufficed) |
| program | `A/native` 150, `A/wasm` 64 | baseline native is 145 |
| tests | `internal/tool/async_test.go` | 107 |

Not counted, and what making A real would add: the same syntax, checker
rules, and lowering in `prog/` (the self-hosted compiler), which
`TestSelfHost` would then require byte for byte; `ovid help language`,
`docs/PROTOCOL.md` (three new diagnostic codes), and the outline/show/edit
commands' handling of `async` (they work, since `async` is part of the
func's span, but were not tested).

## 4. Smoke numbers (2 s runs on starship, NOT final)

Native (`bench-native.sh`, c = concurrent clients):

| path | c | rps | p50 ms | p99 ms | peak RSS |
|---|---|---|---|---|---|
| /w1 | 1 | 96 | 10.4 | 10.7 | 2 MB |
| /w1 | 100 | 8,417 | 11.8 | 14.1 | 10 MB |
| /w2 | 1 | 93 | 10.7 | 10.9 | 10 MB |
| /w2 | 100 | 1,394 | 41.6 | 473 | 10 MB |

For scale, the blocking baseline does ~96 rps on /w1 at any c, and /w2 takes
103 ms at c = 1. At /w2 c = 100 the server opens about 14,000 upstream
connections a second; the p99 there is probably the loopback connection
churn rather than the scheduler, but that was not investigated.

Wasm (`bench-wasm.sh`, workerd under `wrangler dev`):

| path | c | rps | p50 ms | p99 ms |
|---|---|---|---|---|
| /w1 | 1 | 79 | 12.7 | 13.9 |
| /w1 | 100 | 1,765 | 54.5 | 96.7 |
| /w2 | 1 | 44 | 22.5 | 33.8 |
| /w2 | 100 | 1,482 | 63.8 | 101 |

The wasm baseline's /w2 is 113 ms at c = 1. Under node (`try.mjs`) A's /w2
takes 11 ms; under workerd it takes ~22 ms, which was not investigated
(workerd's timers, or the JSPI round trips through `HostPoll`). Binary sizes:
20.9 KB native, 13.2 KB wasm (baseline 11.9 KB / 7.1 KB).

## 5. Many cores, and what is left open

- **Threads**: the scheduler is one queue with no locks. N threads each
  running their own `Rt` (one epoll, one queue, their own arenas) would be
  shared-nothing and needs no language change. Moving a task between
  threads would need the frames to be thread-safe, which nothing here is.
- **Isolated workers** (#75's proposal): fork N processes after `Listen`
  with `SO_REUSEPORT` (`ovid/io.Listen(..., true)` exists), each with its
  own `Rt`. Compatible as is; A only decides how one worker waits.
- A's frames are heap objects with an id, which is exactly what a message to
  another worker can't carry (they hold pointers). Task results cross
  workers only as bytes.
- Open: cancellation and timeouts (a parked frame has no way to be woken
  early), more than one joiner per task, and back-pressure on `spawn`
  (nothing bounds the number of tasks); errors as results rather than the
  two-result convention; and whether colouring is acceptable for an agent
  that must keep two versions of every I/O API in its head.
