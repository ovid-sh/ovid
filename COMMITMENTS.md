# Commitments after v0

v0 reads local files only. This file records what comes next. It is not implemented.

Network is part of the language, not a later maybe. After `ovid` can compile itself, the first standard library is HTTP: the best HTTP client, one a func reaches only through a capability it was handed, with the kernel bounding what the program may do (what a capability enforces, below).

That same capability does three jobs:

- programs call it to speak HTTP
- the compiler fetches packages when an import path is a URL
- it serves user programs

The import syntax stays. The import path is still the package name. Only the lookup changes: a path that is a URL is fetched with the HTTP capability instead of read from the module directory. v0 resolves every import on the local filesystem.

## The self-hosted compiler is the language (decided 2026-10-07)

`prog/`, the compiler written in Ovid, is where the language lives. The Go
toolchain keeps what it alone has, the agent commands (`edit`, `show`,
`outline`, `grep`, `move`) and the bootstrap, and its compiler follows the
Ovid one rather than leading it. While `TestSelfHost` holds the two
byte-identical, a language change lands in `prog/` first, with `internal/`
following in the same pull request; the parity test stays until the Go
compiler is retired.

What is ported to Ovid is what agents are seen to rely on (#12,
`docs/AGENT_FEEDBACK.md`): `check`, `build`, and `refs` are done; `test`
(fork, exec, wait, and pipes in `ovid/io`) and `rename` (the multi-file write
under the module lock) come next. The rest waits for evidence that agents
reach for it.

Why: the self-hosted compiler is a small static binary (about 155 KB as of
2026-10-07; the README keeps the current figure) that checked `prog/` in
7 MB with nine system calls on one thread when measured in #12 (main at
6d4fcd8), and it builds itself under the seccomp list of its own receipt;
the Go runtime cannot be made to fit that. The cost is that every language
feature is written twice until the Go compiler goes.

## What a capability enforces (decided 2026-10-13)

A func reaches only what its parameters carry, transitively through struct
fields; the kernel enforces what the program may do. That is the whole
promise, and the shape of `ovid/io` makes it, not a checker rule:

- Files sit behind opaque handles, done in #214: `Open(io, path, …)`
  returns a `*File` and an error, every stream and path func takes `io` or
  a `*File`, nothing in `ovid/io` takes or returns a bare descriptor, and
  `opaque_type`, in both checkers, keeps a handle from being cast or taken
  apart outside `ovid/io`. The network follows the same shape (#124 is the
  prototype): listening and dialing take a `*Net`, a connection is a
  `*Conn`, and results are `(T, error)` as #216 spells them. Handle funcs
  live in `ovid/io`, beside the syscall.
- `Cap` splits next: `io` is argv, the heap, and stdio; `fs *ovid/io.Dir`
  (openat-relative) and `net *ovid/io.Net` are asked for on `main`'s
  signature, and `main(io)` alone keeps everything, so the cheap default
  stays cheap. An HTTP handler `(io, req, res)` holds no `Dir`, so it
  cannot open a file the request names.
- The build receipt names the grant (`"capabilities": ["fs", "net"]`)
  beside its syscalls, and the sandbox (#107, #155) is derived from it: a
  Landlock read root when `fs` is absent, no address families when `net`
  is.

Not enforced, by design: code in the same process that wants to misbehave.
Handles live in addressable memory and `load64`/`store64` are unrestricted,
so a handle laundered through a struct field is three lines (#123). A
check-time flow rule ("use only the handles your parameters carry") cannot
close that, caught nothing in `prog/` and `std/` that typing does not, and
would cost 160–220 lines in two checkers; it is not built. The boundary for
distrusted code is a process (the receipt sandbox, #75 workers), not the
type system, until #8.

Why: measured on main at fb595e3 (2026-10-07, on starship, before #214).
Then 18 of `ovid/io`'s 40 funcs reached the kernel with no capability
parameter, and #123's `Sneaky` still checked, built, and read
`/etc/hostname`. The handle shape costs five user instructions per I/O
call (37 to 42 per 4 KB read, wall time within noise; a Redis-protocol
server +0.08–0.77% instructions per request), the network PoC +818 B of
self-hosted compiler and +7 B per program, and the file reshape about 66
mechanical call sites, which #214 then paid. In ten agent runs, five per
API shape, no agent misused a descriptor under either; the handle API took
a median 6 turns to the raw one's 11, which traced to its doc idiom, not to
enforcement. Order: the network shape, the `Cap` split, the receipt and
sandbox, then an agent experiment in which misuse is possible before any
further checker rule.
