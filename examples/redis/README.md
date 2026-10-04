# miniredis: a Redis-protocol server in Ovid

A proof of concept: a single-threaded key-value server that speaks a subset
of the Redis protocol, written in Ovid with no libc. It exists to find out
what a long-running network server needs from the language and `std/`, and
how the result compares with a real server.

```sh
ovid build -C examples/redis -o miniredis
./miniredis 6380 &            # 127.0.0.1 only
valkey-cli -p 6380 set greeting hello
valkey-cli -p 6380 get greeting
```

`./verify.py` sends the same commands to miniredis and to `valkey-server`
(or `redis-server`) and compares the replies byte for byte. `./bench.py`
measures both with `valkey-benchmark`.

## Packages

- `miniredis/store`: a heap that reuses freed blocks (`ovid/io.Alloc` never
  frees), and a hash table of string keys and values with expiry times.
- `miniredis/server`: connections and their buffers, the RESP parser, the
  commands, and the `epoll` loop.
- `miniredis/cli`: `main`.

It uses the socket, `epoll`, and clock wrappers this change adds to
`ovid/io`.

## Commands

`GET` `SET` (with `EX` and `PX`) `DEL` `EXISTS` `INCR` `DECR` `INCRBY`
`DECRBY` `APPEND` `STRLEN` `MSET` `MGET` `EXPIRE` `PEXPIRE` `TTL` `PTTL`
`PERSIST` `DBSIZE` `FLUSHALL` `FLUSHDB` `PING` `ECHO` `INFO` `QUIT`, and
`SELECT` `CLIENT` `COMMAND` `CONFIG`, which are accepted and do nothing.
Pipelining and inline commands work.

## What it does not do

- Any type but strings: no lists, hashes, sets, sorted sets, or streams.
- Persistence, replication, cluster mode, pub/sub, transactions, scripts,
  ACLs, RESP3, or TLS.
- Active expiry: an expired key is deleted when it is next asked for, and
  until then it takes memory and counts in `DBSIZE`.
- Incremental rehashing: the table doubles in one step, which pauses the
  server for as long as that takes.
- Limits on memory, on a client's output buffer, or on the number of
  clients; eviction; overflow checks in `INCR`.
- More than one thread.
- Listening on anything but 127.0.0.1.

So a comparison with a real server is between a program that does the
minimum and one that does a great deal more per request. It shows what the
language and its I/O allow, not that this is a replacement.
