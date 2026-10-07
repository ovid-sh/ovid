# Async experiments (#75)

Four proofs of concept for how an Ovid program waits on more than one
thing, each built far enough to measure and to write code in:

| | model | language change |
|---|---|---|
| **A** | `async`/`await`: functions lowered to state machines (stackless) | yes: syntax, checker, a lowering pass |
| **B** | an event loop in `std/`; the program is an explicit state machine | no |
| **C** | stackful tasks: blocking-style code, suspended at the syscall | a stack switch, restricted to `ovid/io` |
| **D** | the host owns concurrency (#127): one handler per request | no |

Each runs natively (Linux x86-64) and as wasm (workerd, JSPI). These are
experiments: compiler changes go in the Go toolchain (`internal/`) only,
and `prog/` is not touched. `go test ./...` must still pass on the branch.

## The workloads

Every POC serves the same two paths, with the same answers, as the
reference servers in `baseline/` (native) and `baseline-wasm/` (wasm):

- **W1, `GET /w1`**: one upstream call of 10 ms, then `ok`. Under C
  concurrent clients this measures concurrency *between* requests.
- **W2, `GET /w2`**: ten upstream calls of 10 ms, then `ok 10`. Done one
  after another it takes 100 ms; concurrently, about 10. This measures
  concurrency *within* a request.

An upstream call, natively, is a TCP connection to `127.0.0.1:<upstream
port>` (`cmd/upstream`): write `10\n`, read `ok\n`, close. Under wasm it is
a host timer: `ovid/io.HostSleep(10)` blocks; `HostSubmit(10, tag)` and
`HostPoll` are the non-blocking pair (`std/ovid/io/host.ov`,
`internal/wasm/host.mjs`).

Inbound, each request is its own connection (`Connection: close`): read
the head up to the blank line, answer with `ovid/http.Format`, close.

## Running

Benchmarks run on starship (Linux, 16 threads), server and load on
loopback. `bench-native.sh` and `bench-wasm.sh` hold one lock
(`/tmp/ovid-async-bench.lock`), so two never overlap.

```sh
rsync -a --delete --exclude node_modules --exclude .wrangler --exclude bin ./ starship:ovid-async/<name>/
ssh starship 'cd ovid-async/<name> && ./experiments/async/bench-native.sh <label> <module dir> [server args]'
ssh starship 'cd ovid-async/<name>/experiments/async/wasm && npm install'   # once
ssh starship 'cd ovid-async/<name> && ./experiments/async/bench-wasm.sh <label> <module dir>'
```

The native server is started as `<binary> <port> <upstream port> [args]`.
A wasm module is served by `wasm/worker.js` (fresh instance per request,
under `runAsync`), unless the module directory brings its own `worker.js`
and `*.mjs`. `wasm/try.mjs` runs a module once per path under node
(`node --experimental-wasm-jspi`). Results append to
`results/<label>-{native,wasm}.jsonl`: rps, p50/p90/p99/max, errors, the
server's peak RSS, binary size.

The final numbers are one sequential run of every POC on a quiet machine;
while developing, use short runs (`DURATION=2s CONCURRENCY="1 100"`).

## What each POC delivers

In `experiments/async/<A|B|C|D>/`:

- `native/` and `wasm/`: Ovid modules serving W1 and W2 (wasm may be the
  same source as native, or not).
- `NOTES.md`:
  1. The design, in a paragraph or two, and what it would mean for the
     language and `std/`.
  2. **How it feels**: the W2 handler as written in this model, quoted
     whole, and what was awkward to write, debug, or read.
  3. **What it cost**: lines changed by area (compiler, std, host, the
     program), from `git diff --stat` against `async-poc/base`.
  4. Smoke numbers (short runs), clearly marked as not final.
  5. How it would extend to many cores (threads, isolated workers) and
     what it leaves unanswered.
