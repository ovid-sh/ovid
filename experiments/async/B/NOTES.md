# POC B: an event loop, explicit state machines

## 1. Design

There is no language change and no std change. The program does what C
programs do without libuv: one process, one `epoll` instance, every socket
non-blocking, level-triggered. Everything being waited on is a struct, and
its address is the epoll `data`. The struct's **first field is a kind tag**
(listener, connection, or upstream call), so the loop can tell which struct
an event is for. Its **state field** says where it is:

```
Conn:  READING ──► UPSTREAM ──► WRITING ──► closed, back on the free list
                (/w1: 1 call, /w2: 10 calls; the Conn counts `pending`)
Up:    CONNECTING ──► SENDING ──► RECEIVING ──► closed, `upDone(conn)`
```

That is three states for a connection and three for an upstream call. The
loop dispatches on the kind, and the handler on the state. A finished
upstream call decrements its connection's `pending`. The last one to finish
formats the response and starts writing it, trying the write at once and
waiting for `EPOLLOUT` only for what is left.

Under wasm the host gives each request its own instance, so the loop only
covers the calls within one request. It submits every timer
(`HostSubmit`), keeps a state byte per call, and `HostPoll`s until none is
pending. That poll is the only point where the instance suspends (JSPI).

What it means for the language and std: nothing is needed beyond the
syscall wrappers already in `async-poc/base`. That is B's appeal, and also
its limit: the program carries the whole concurrency model itself.

## 2. How it feels

The W2 path, native. There is no single "W2 handler": W2 is spread across
four functions and two structs (excerpted from `native/server/server.ov`).

```
func onRead(io *ovid/io.Cap, s *Server, c *Conn) i64 {
  ... read until the head is in, Parse ...
  var calls i64 = 0
  if ovid/http.PathIs(c.req, strptr("/w1"), 3) {
    calls = 1
  } else if ovid/http.PathIs(c.req, strptr("/w2"), 3) {
    calls = FANOUT
  } else {
    return respond(io, s, c, 404, 0, 0)
  }
  c.state = UPSTREAM
  c.calls = calls
  c.pending = calls
  ovid/io.EpollCtl(io, s.ep, ovid/io.EPOLL_CTL_MOD, c.fd, 0, c as i64)
  var i i64 = 0
  while i < calls {
    startUpstream(io, s, c)
    i = i + 1
  }
  return 0
}

func onUp(io *ovid/io.Cap, s *Server, u *Up, mask i64) i64 {
  if u.state == CONNECTING {
    if (mask & (ovid/io.EPOLLERR | ovid/io.EPOLLHUP)) != 0 {
      return finishUp(io, s, u, true)
    }
    u.state = SENDING
  }
  if u.state == SENDING {
    while u.sent < 3 {
      var w i64, we i64 = ovid/io.WriteSome(u.fd, strptr("10\n") + u.sent, 3 - u.sent)
      if we == ovid/io.E_AGAIN {
        return 0
      }
      if we != 0 {
        return finishUp(io, s, u, true)
      }
      u.sent = u.sent + w
    }
    u.state = RECEIVING
    return ovid/io.EpollCtl(io, s.ep, ovid/io.EPOLL_CTL_MOD, u.fd, ovid/io.EPOLLIN, u as i64)
  }
  // RECEIVING
  while u.got < 3 {
    var n i64, re i64 = ovid/io.Read(u.fd, u.buf + u.got, 8 - u.got)
    if re == ovid/io.E_AGAIN {
      return 0
    }
    if re != 0 || n == 0 {
      return finishUp(io, s, u, true)
    }
    u.got = u.got + n
  }
  return finishUp(io, s, u, false)
}

func upDone(io *ovid/io.Cap, s *Server, c *Conn, failed bool) i64 {
  c.pending = c.pending - 1
  if failed {
    c.failed = true
  }
  if c.pending > 0 {
    return 0
  }
  if c.failed {
    return respond(io, s, c, 500, 0, 0)
  }
  if c.calls == 1 {
    return respond(io, s, c, 200, strptr("ok"), 2)
  }
  return respond(io, s, c, 200, strptr("ok 10"), 5)
}
```

Compare the baseline, where the same work is a five-line `while` around
`upstream(io, up)`.

The W2 handler under wasm (`wasm/handler/handler.ov`) is much smaller,
because each request owns its instance and nothing but timers is waited on:

```
func fanout(io *ovid/io.Cap, n i64) i64 {
  var state i64 = ovid/io.Alloc(io, n)
  var i i64 = 0
  while i < n {
    var e i64 = ovid/io.HostSubmit(UPSTREAM_MS, i)
    if e != 0 {
      return e
    }
    store8(state + i, PENDING)
    i = i + 1
  }
  var pending i64 = n
  var tags i64 = ovid/io.Alloc(io, 8 * n)
  while pending > 0 {
    var got i64, pe i64 = ovid/io.HostPoll(tags, n)
    if pe != 0 {
      return pe
    }
    var j i64 = 0
    while j < got {
      var tag i64 = load64(tags + (8 * j))
      if tag < 0 || tag >= n || load8(state + tag) != PENDING {
        return ovid/io.E_IO
      }
      store8(state + tag, DONE)
      pending = pending - 1
      j = j + 1
    }
  }
  return 0
}
```

What was awkward:

- **The control flow is inverted and scattered.** "Read the request, call
  upstream ten times, answer" becomes 15 functions instead of the
  baseline's 6, and 376 lines instead of 145. To follow one request you
  jump between `onRead`, `startUpstream`, `onUp`, `finishUp`, `upDone`,
  `respond`, and `onWrite`, and every local that must survive a wait
  becomes a struct field (`sent`, `got`, `pending`, `calls`, `failed`).
  `outline` and `refs` show the functions, not the order they run in.
- **No function values means a kind tag and a hand-written dispatch.** The
  epoll `data` is a raw address read with `load64(data)` and cast with
  `as *Conn` or `as *Up`. Nothing checks that the cast matches the tag. A
  new kind of thing to wait on means another tag, another struct, and
  another branch in the loop.
- **Lifetimes are manual, and `ResetHeap` is useless here.** Requests
  interleave, so no point in the heap's life belongs to one request. Conn
  and Up structs (and their buffers) go on hand-written free lists. That
  in turn means `ovid/http.Format` cannot be used, because it allocates a
  new buffer per response, which would leak in a loop that never resets.
  The response is formatted by hand into the Conn's pooled `ovid/mem.Buf`.
  A struct used after it went back on a free list would be silent memory
  corruption; nothing catches it.
- **The std wrappers allocate on every call.** `EpollCtl` and `Connect`
  each take 16 bytes from the heap for their argument structs (there is no
  scratch memory, #82). In a loop that never resets, that is a slow leak
  of about 100 bytes per W2 request, about 6 MB per minute at the smoke
  rate. It does not show in a 2 s run, but a long-running B server would
  need these wrappers to take caller-owned scratch.
- **Where the bugs could come from.** The code worked on the first build,
  but only because three questions were each settled by reasoning, not by
  any check:
  - Can an event in the same `epoll_wait` batch refer to a struct already
    freed and reused? (No: each struct is freed only while handling its
    own event, and an fd appears at most once per batch.)
  - Can a synchronous connect failure free the connection while the
    loop is still starting its other calls? (Only on the last call, after
    which the loop ends.)
  - Is `pending` decremented exactly once per call on every path?

  Each of these is the kind of bug a race-free single-threaded model
  still allows, and an agent changing one handler must re-check all
  three.

What felt good: the wasm version. When a request owns its instance and the
host has a poll call, the state machine shrinks to a table of states and a
counter. Natively, there is no hidden machinery: every syscall is visible,
and the receipt lists exactly what the loop needs (`epoll_*`, `accept4`,
`connect`).

## 3. What it cost

Against `async-poc/base`:

| area | lines |
|---|---|
| compiler | 0 |
| std | 0 |
| host (`host.mjs`, `worker.js`) | 0 |
| harness | 1 (`bench-wasm.sh`: the Worker name must be lowercase; a fix for every POC) |
| program, native | 376 (`native/server/server.ov`; baseline 145) |
| program, wasm | 69 (`wasm/handler/handler.ov`; baseline 35) |

## 4. Smoke numbers (not final)

starship, 2–5 s runs, while other POCs may have been building. **These are
not the final numbers.**

Native (binary 12,322 bytes):

| path | c | rps | p50 ms | p99 ms | peak RSS |
|---|---|---|---|---|---|
| /w1 | 1 | 96 | 10.4 | 10.6 | 2.0 MB |
| /w1 | 100 | 7,157 | 14.0 | 17.1 | 4.0 MB |
| /w2 | 1 | 93 | 10.7 | 11.0 | 4.0 MB |
| /w2 | 10 | 873 | 11.3 | 13.2 | 4.0 MB |
| /w2 | 100 | 1,386 | 40.3 | 462.6 | 6.0 MB |

For comparison, the baseline at /w1 c=10 does 97 rps with a p50 of 103 ms,
and at /w2 c=1 does 9.7 rps with a p50 of 103 ms.

The /w2 c=100 tail is the workload, not B. Every upstream call is a new TCP
connection, about 14k connects a second, and `ss -s` showed 43.9k sockets
in TIME_WAIT during the run (port range 32768–60999). At /w2 c=10 the tail
is tight (p99 13 ms). Any POC that really runs ten calls at once will hit
the same wall. Keep-alive upstream connections would remove it, but the
workload defines a call as its own connection.

Wasm (workerd under `wrangler dev`, binary 7,341 bytes):

| path | c | rps | p50 ms | p99 ms |
|---|---|---|---|---|
| /w1 | 1 | 79 | 12.7 | 14.0 |
| /w1 | 100 | 1,733 | 57.4 | 89.7 |
| /w2 | 1 | 45 | 22.4 | 24.4 |
| /w2 | 100 | 1,479 | 65.2 | 107.3 |

/w2 is concurrent (22 ms, against the baseline's 113 ms), but at c=1 it
takes about twice as long as /w1. Under node it takes 11 ms. A likely
cause, not verified: each timer fires as its own task, `HostPoll` wakes on
the first one and returns one tag, and the loop polls again. That is up to
ten suspend/resume round trips per request, which workerd seems to charge
for more than node does. A poll that waits one more tick to collect
several completions would test this. The peak RSS of workerd (0.7–1.8 GB)
mostly measures `wrangler dev` and one 32 MiB heap per live instance, not
B.

## 5. Many cores, and what is left open

- **More cores** follow the RFC's worker model without changing the loop:
  N processes, each with its own loop, listening with `SO_REUSEPORT`
  (`Listen(..., reuseport=true)` is already in base). Each process owns
  its free lists and heap, so nothing is shared. Threads would also work,
  one loop per thread with its own `Cap`, for the same reason. B carries
  no state that wants sharing, because all of it lives in per-connection
  structs.
- **What B leaves unanswered**:
  - Composition. A handler that needs a database call followed by an
    HTTP call, each async, needs a state per step, and nesting (a call
    that itself fans out) multiplies the states. B does not give
    abstraction over waiting: every library that waits would have to
    expose its own state machine and kind tags. With no function values,
    the program's loop must know every kind of thing any library waits
    on.
  - Timeouts. There are none here. Adding them means a timer wheel or
    `timerfd` plus another kind and another state per struct.
  - Memory for a long-running loop: free lists, plus std wrappers that do
    not allocate (#82).

B is the floor: no language cost, the best visibility of syscalls, and
the clearest numbers. But the code an agent has to write and keep correct
grows with every kind of thing waited on.
