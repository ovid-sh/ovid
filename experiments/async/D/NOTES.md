# D: the host owns concurrency

## 1. Design

The program is a blocking handler for one request and knows nothing of
concurrency. The host runs many copies of it at once (#127):

- **Native** (`native/`): `app` is `baseline/server`, unchanged but for
  exporting `Serve`, `Atoi`, and `Loop` (its old `main`). `host` forks:
  - `shared`: N pre-forked workers accepting on one listener the parent opened;
  - `reuseport`: N workers, each with its own `SO_REUSEPORT` listener;
  - `perconn`: the parent accepts and forks a child per connection, which
    serves it and exits; the parent reaps with the new `ovid/io.Reap`.

  `host <port> <upstream port> [N] [shared|reuseport|perconn]`, N 16 by default.
- **Wasm** (`wasm/`): the default Worker model already is D, with a fresh
  instance per request under `runAsync`. `wasm/handler` is
  `baseline-wasm`, and `wasm/worker.js` adds an `x-ovid-memory` header.
  What D costs here is the heap each instance maps. `internal/wasm`
  now reads `OVID_WASM_HEAP` to override the first region's size, which is
  an experiment-only change.

For the language and `std/` this needs nothing but `Reap`, a
non-blocking `wait4`. Isolation comes from the OS (a process) or from the
host (an instance), never from the type system.

**W2 stays sequential under D, as expected.** One request is one blocking
handler, so its ten upstream calls take about 100 ms whatever the host
does. To do better, D needs the host to run the sub-requests: for
example a host op "make these N calls and wake me when all are done", or
the handler emitting sub-requests that the host fans out and feeds back.
Either way, concurrency inside a request moves into the host's API rather
than the language.

## 2. How it feels

The W2 handler is the baseline's, word for word:

```
func handle(io *ovid/io.Cap, req *ovid/http.Request, res *ovid/http.Response, up i64) i64 {
  ...
  if ovid/http.PathIs(req, strptr("/w2"), 3) {
    var i i64 = 0
    while i < FANOUT {
      if upstream(io, up) != 0 {
        return 1
      }
      i = i + 1
    }
    ovid/http.Write(io, res, strptr("ok "), 3)
    ovid/http.WriteInt(io, res, FANOUT)
    return 0
  }
  ...
}
```

And the whole concurrent part of the native host:

```
func perconn(io *ovid/io.Cap, ln i64, up i64) i64 {
  var m *ovid/io.HeapMark = ovid/io.MarkHeap(io)
  while true {
    var fd i64, ae i64 = ovid/io.Accept(ln, 0)
    if ae == 0 {
      var pid i64 = ovid/io.Fork()
      if pid == 0 {
        ovid/io.Close(ln)
        app.Serve(io, fd, up)
        ovid/io.Exit(0)
      }
      ovid/io.Close(fd)
    }
    while ovid/io.Reap(io) > 0 {
    }
    ovid/io.ResetHeap(io, m)
  }
  return 0
}
```

Good:
- The program is plain sequential code that an agent reads top to bottom.
- Nothing about the handler changed between the baseline and here.
- A crash takes down one connection's process, not the server.
- Each request runs on its own heap, so `ResetHeap` keeps working as before.

Awkward:
- **No function values.** fork's "returns twice" stands in for "run this
  function in a worker". A child that forgets `Exit` runs on into the
  parent's loop. The host works around that with `worker` calling `Exit`
  itself, but nothing checks it.
- **Reaping.** It needed a new std function, and without it zombies pile up
  quietly.
- **Ports in the ephemeral range.** The first smoke runs used 48080/49100. Those
  sit inside Linux's ephemeral range (32768–60999), so `bind` failed with
  `EADDRINUSE` while ports were held as the local end of outgoing
  connections or in TIME_WAIT. The bench's own upstream then never came up,
  and every request answered 500. Keep test ports below 32768.
- **Wasm memory.** The cost is invisible in the program. `NewStdio`
  allocates `MAX_REQUEST + 1` (8 MiB) up front. With a first region smaller
  than that, `Alloc` maps a whole 128 MiB `HEAPCHUNK`. A 1 MiB region on its
  own therefore makes each instance **129 MiB**, four times worse than the
  default 32 MiB. Only a 1 MiB region together with a 64 KiB `MAX_REQUEST`
  brings it to **1.1 MiB** (`wasm/memsize.mjs`, under node).

## 3. What it cost

Against `async-poc/base`:

| area | lines |
|---|---|
| compiler | 0 |
| `std/` (`ovid/io.Reap`) | +9 |
| toolchain (`OVID_WASM_HEAP`, experiment only) | +12 |
| wasm host (`memoryBytes` in run's result) | +4 |
| program: `app` vs `baseline/server` | 39 lines differ (exports, `Loop` lifted out of `main`) |
| program: the native host | 120 |
| program: the wasm Worker | 18 (plus 16 for `memsize.mjs`) |

## 4. Smoke numbers (2 s runs on starship, loopback; NOT final)

Native, W1 = one 10 ms call per request, W2 = ten:

| host | N | C | W1 rps | W1 p50/p99 ms | W2 rps | W2 p50/p99 ms | peak RSS |
|---|---|---|---|---|---|---|---|
| baseline | 1 | 10 | 97 | 103 / 105 | 9.7 | 1029 / 1030 | 2 MB |
| shared | 16 | 100 | 1,514 | 63 / 75 | 149 | 625 / 730 | 34 MB |
| shared | 64 | 100 | 5,932 | 17 / 23 | 577 | 207 / 221 | 131 MB |
| shared | 256 | 100 | 9,024 | 11 / 13 | 931 | 107 / 111 | 521 MB |
| shared | 256 | 1000 | 15,398 | 46 / 175 | 1,445 | 652 / 782 | 521 MB |
| reuseport | 16 | 100 | 1,308 | 54 / 211 | 111 | 625 / 1560 | 34 MB |
| reuseport | 64 | 100 | 4,021 | 21 / 88 | 292 | 215 / 957 | 131 MB |
| reuseport | 256 | 1000 | 14,114 | 43 / 337 | 1,096 | 509 / 1739 | 521 MB |
| perconn | – | 100 | 9,164 | 11 / 12 | 917 | 108 / 113 | 34 MB / 204 MB |
| perconn | – | 1000 | 14,730 | 57 / 183 | 1,583 | 577 / 937 | 478 MB / 2.1 GB |

- **A pool's throughput is N divided by the request time.** W1 with
  N=16 gives 1,514 rps, which is 16 / 10.5 ms. Throughput scales linearly
  with N until about 15k rps, where the box itself (loadgen, upstream,
  connects) binds.
- **Memory is about 2 MB per process**, a summed RSS that counts shared
  pages once per process. So N decides memory and throughput together.
- **reuseport has much worse tails than shared** (p99 211 against 75 ms at
  N=16). The kernel hashes connections to listeners without knowing which
  worker is busy. A shared accept queue hands each connection to an idle
  worker.
- **perconn needs no N.** A child per connection matches concurrency
  exactly (C=100 W1: 9,164 rps at p50 10.9 ms, near ideal). Its memory is
  then about 2 MB times the requests in flight, which reached 2.1 GB at
  C=1000 for W2.

Wasm (`wrangler dev`, workerd, one isolate; RSS is workerd's and
carries history from earlier cells in the same session):

| first region | MAX_REQUEST | memory per instance | W1 C=100 rps | p50/p99 ms | workerd RSS | W2 C=100 rps |
|---|---|---|---|---|---|---|
| 32 MiB (default) | 8 MiB | 32.1 MiB | 1,714 | 57 / 88 | 2,275 MB | 695 |
| 1 MiB | 8 MiB | 129.1 MiB | 1,556 | 62 / 102 | 2,277 MB | 535 |
| 1 MiB | 64 KiB | 1.1 MiB | 1,955 | 49 / 91 | 733 MB | 726 |

- W1 and W2 answer correctly in all three.
- Shrinking the per-instance heap cut workerd's RSS at C=100 to about a
  third and raised throughput by 14%.
- Under workerd dev, throughput is bounded near 2k rps by the single
  isolate's per-request work (instantiate, JS glue, JSPI), not by memory.
  Production Workers spread requests over isolates, so this is a floor
  for one isolate, not a Worker's limit.
- W2 is about 110–125 ms at low load everywhere, as expected for D.

## 5. Many cores, and what is left open

- **D already uses many cores natively.** Processes are the isolated
  workers of #75, and the pool and per-connection hosts are its proposal
  with the program left untouched.
- **What D cannot do:**
  - **Concurrency inside a request.** W2 is that case, and it needs a host API.
  - **Cheap concurrency.** Each in-flight request costs a process (about
    2 MB here) or a wasm instance (its heap). 10k idle connections would
    mean 10k processes.
- **Open questions:**
  1. How the host offers sub-request fan-out without the program growing
     its own concurrency.
  2. Whether `ovid/http`'s up-front 8 MiB request buffer should shrink, or
     grow lazily, since under wasm it sets the per-request memory floor.
  3. A spawn that names its entry function, instead of fork's "returns
     twice".
  4. Replacing a pool worker that dies. The host here only reports it.
