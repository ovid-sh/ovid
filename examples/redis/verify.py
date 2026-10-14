#!/usr/bin/env python3
"""Sends the same commands to miniredis and to a real server and compares
the replies byte for byte.

  verify.py [--ovid path/to/ovid] [--server path/to/valkey-server]

Error replies are compared only as errors: the wording belongs to each
server. Needs Linux x86-64 and valkey-server or redis-server.
"""
import argparse, os, shutil, socket, subprocess, sys, tempfile, time

HERE = os.path.dirname(os.path.abspath(__file__))


def enc(*args):
    out = b"*%d\r\n" % len(args)
    for a in args:
        if not isinstance(a, bytes):
            a = str(a).encode()
        out += b"$%d\r\n%s\r\n" % (len(a), a)
    return out


class Client:
    def __init__(self, port):
        for _ in range(100):
            try:
                self.s = socket.create_connection(("127.0.0.1", port))
                break
            except OSError:
                time.sleep(0.05)
        else:
            sys.exit("no server on port %d" % port)
        self.buf = b""

    def line(self):
        while b"\r\n" not in self.buf:
            d = self.s.recv(65536)
            if not d:
                raise EOFError
            self.buf += d
        l, self.buf = self.buf.split(b"\r\n", 1)
        return l

    def take(self, n):
        while len(self.buf) < n:
            d = self.s.recv(65536)
            if not d:
                raise EOFError
            self.buf += d
        d, self.buf = self.buf[:n], self.buf[n:]
        return d

    def reply(self):
        """One reply, as its raw bytes; an error is reduced to b'-'."""
        l = self.line()
        t = l[:1]
        if t == b"-":
            return b"-"
        if t == b"$":
            n = int(l[1:])
            return l + b"\r\n" + (self.take(n + 2) if n >= 0 else b"")
        if t == b"*":
            return l + b"\r\n" + b"".join(self.reply() for _ in range(int(l[1:])))
        return l + b"\r\n"

    def raw(self, data, replies):
        self.s.sendall(data)
        return [self.reply() for _ in range(replies)]

    def cmd(self, *args):
        return self.raw(enc(*args), 1)[0]


def script():
    """Yields (label, bytes to send, replies expected)."""
    one = lambda *a: (" ".join(str(x)[:20] for x in a), enc(*a), 1)
    yield one("FLUSHALL")
    yield one("PING")
    yield one("PING", "hello")
    yield ("inline ping", b"PING\r\n", 1)
    yield ("inline set", b"SET  inline  value \r\n", 1)
    yield one("GET", "inline")
    yield one("ECHO", b"\x00\x01binary\r\n\xff")
    yield one("GET", "missing")
    yield one("SET", "k", "v")
    yield one("get", "k")
    yield one("SET", "k", "")
    yield one("GET", "k")
    yield one("STRLEN", "k")
    yield one("APPEND", "k", "abc")
    yield one("APPEND", "k", "x" * 5000)
    yield one("STRLEN", "k")
    yield one("APPEND", "new", "abc")
    yield one("GET", "new")
    yield one("EXISTS", "k", "new", "missing", "k")
    yield one("DEL", "k", "missing", "new")
    yield one("EXISTS", "k")
    yield one("INCR", "n")
    yield one("INCR", "n")
    yield one("DECR", "n")
    yield one("INCRBY", "n", 1000000000000)
    yield one("DECRBY", "n", 7)
    yield one("GET", "n")
    yield one("SET", "neg", "-5")
    yield one("INCR", "neg")
    yield one("INCRBY", "neg", -10)
    yield one("SET", "s", "abc")
    yield one("INCR", "s")
    yield one("INCRBY", "n", "x")
    yield one("MSET", "a", 1, "b", 2, "c", 3)
    yield one("MGET", "a", "missing", "c", "b")
    yield one("MSET", "a")
    yield one("DBSIZE")
    yield one("SET", "big", b"B" * 300000)
    yield one("GET", "big")
    yield one("STRLEN", "big")
    yield one("SET", "big", "small")
    yield one("GET", "big")
    yield one("SET", "e", "v", "PX", 10000000)
    yield one("TTL", "e")
    yield one("PERSIST", "e")
    yield one("TTL", "e")
    yield one("TTL", "missing")
    yield one("EXPIRE", "e", 10000)
    yield one("TTL", "e")
    yield one("EXPIRE", "missing", 10)
    yield one("SET", "e", "again")
    yield one("TTL", "e")
    yield one("SET", "gone", "v", "PX", 30)
    yield one("SET", "gone2", "v", "EX", 100)
    yield one("PEXPIRE", "gone2", 30)
    yield ("sleep", None, 0)
    yield one("GET", "gone")
    yield one("EXISTS", "gone2")
    yield one("SET", "x", "y", "EX", 0)
    yield one("SET", "x", "y", "ZZ", 5)
    yield one("SET", "x")
    yield one("GET")
    yield one("NOSUCHCOMMAND", "a")
    yield one("SELECT", 0)
    # A pipeline: many commands in one write, replies in order.
    n = 5000
    data = b"".join(enc("SET", "key:%d" % i, "value:%d" % i) for i in range(n))
    data += b"".join(enc("GET", "key:%d" % i) for i in range(0, n, 7))
    yield ("pipeline of %d" % n, data, n + len(range(0, n, 7)))
    yield one("DBSIZE")
    # Enough keys to grow the table several times, then delete most.
    n = 40000
    data = b"".join(enc("SET", "grow:%d" % i, i) for i in range(n))
    yield ("set %d keys" % n, data, n)
    data = b"".join(enc("DEL", "grow:%d" % i) for i in range(0, n, 2))
    yield ("delete half", data, n // 2)
    data = b"".join(enc("GET", "grow:%d" % i) for i in range(0, n, 97))
    yield ("read some back", data, len(range(0, n, 97)))
    yield one("DBSIZE")
    yield one("FLUSHALL")
    yield one("DBSIZE")
    yield one("GET", "key:1")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ovid", default=os.path.join(HERE, "..", "..", "bin", "ovid"))
    ap.add_argument("--server", default=shutil.which("valkey-server") or shutil.which("redis-server"))
    a = ap.parse_args()
    if not a.server:
        sys.exit("valkey-server or redis-server is needed: pass --server")
    tmp = tempfile.mkdtemp()
    mini = os.path.join(tmp, "miniredis")
    subprocess.run([a.ovid, "build", "-C", HERE, "-o", mini], check=True, stdout=subprocess.DEVNULL)
    procs = [subprocess.Popen([a.server, "--port", "7701", "--save", "", "--appendonly", "no", "--dir", tmp],
                              stdout=subprocess.DEVNULL),
             subprocess.Popen([mini, "7702"])]
    fails = 0
    try:
        real, ours = Client(7701), Client(7702)
        for label, data, replies in script():
            if data is None:
                time.sleep(0.1)
                continue
            want, got = real.raw(data, replies), ours.raw(data, replies)
            if want != got:
                fails += 1
                bad = next(i for i in range(replies) if want[i] != got[i])
                print("FAIL  %s: reply %d: want %r, got %r" % (label, bad, want[bad][:80], got[bad][:80]))
            else:
                print("ok    %s" % label)
        # QUIT answers and then closes.
        if ours.cmd("QUIT") != b"+OK\r\n":
            fails += 1
            print("FAIL  QUIT reply")
        try:
            ours.reply()
            fails += 1
            print("FAIL  the connection stayed open after QUIT")
        except EOFError:
            print("ok    QUIT")
        # A protocol error closes the connection and leaves the server up.
        junk = Client(7702)
        junk.s.sendall(b"*2\r\n$3\r\nGET\r\nnot-a-bulk\r\n")
        if junk.reply() != b"-":
            fails += 1
            print("FAIL  protocol error reply")
        print("ok    protocol error" if Client(7702).cmd("PING") == b"+PONG\r\n" else "FAIL  server died")
    finally:
        for p in procs:
            p.kill()
        shutil.rmtree(tmp, ignore_errors=True)
    print("%d failed" % fails if fails else "all passed")
    sys.exit(1 if fails else 0)


if __name__ == "__main__":
    main()
