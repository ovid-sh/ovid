#!/usr/bin/env python3
"""Times sqlread (Ovid) against the same program in C and against libsqlite3.

  bench/bench.py [--ovid path/to/ovid] [--runs N] [--small]

Programs:
  ovid     ../sqlite/*.ov, built by ovid
  c-O0     sqlread.c, the same algorithms function for function, gcc -O0
  c-O2     the same, gcc -O2: what the Ovid source could run at
  c-O1, c-noinl   with --levels: gcc -O1, and gcc -O2 -fno-inline, to show
           what register allocation and inlining are each worth
  api      api.c: libsqlite3 through its C API, gcc -O2
  sqlite3  the sqlite3 shell (print and seek only)

Every program's output is compared with the api program's before anything is
timed. Times are the best of N runs, output to /dev/null, file in the page
cache; user and sys are the CPU time of that best run. Needs Linux x86-64,
gcc, libsqlite3 and its header, and the sqlite3 shell.
"""
import argparse, math, os, random, sqlite3, struct, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "out")


def sh(*cmd):
    subprocess.run(cmd, check=True, stdout=subprocess.DEVNULL)


def build(ovid):
    """Builds every program into out/; returns the seconds each took."""
    os.makedirs(OUT, exist_ok=True)
    times = {}
    def timed(name, *cmd):
        t = time.perf_counter()
        sh(*cmd)
        times[name] = time.perf_counter() - t
    c = os.path.join(HERE, "sqlread.c")
    timed("ovid", ovid, "build", "-C", os.path.dirname(HERE), "-o", os.path.join(OUT, "ovid"))
    for name, flags in (("c-O0", ["-O0"]), ("c-O1", ["-O1"]), ("c-noinl", ["-O2", "-fno-inline"]), ("c-O2", ["-O2"])):
        timed(name, "gcc", *flags, "-fwrapv", "-fno-builtin-pow10", "-o", os.path.join(OUT, name), c)
    timed("api", "gcc", "-O2", "-o", os.path.join(OUT, "api"), os.path.join(HERE, "api.c"), "-lsqlite3")
    return times


def makedb(path, scale):
    if os.path.exists(path):
        return
    random.seed(1)
    c = sqlite3.connect(path)
    c.execute("create table ints(id integer primary key, a int, b int, c int)")
    c.execute("create table texts(id integer primary key, s text, t text)")
    c.execute("create table reals(r)")
    c.execute("create table bits(r)")
    c.executemany("insert into ints(a,b,c) values (?,?,?)",
                  ((i, random.getrandbits(40), -i * 7) for i in range(2000000 // scale)))
    c.executemany("insert into texts(s,t) values (?,?)",
                  (("name-%d" % i, "x" * (i % 200)) for i in range(1000000 // scale)))
    c.executemany("insert into reals values (?)",
                  ((random.random() * 1e6,) for i in range(1000000 // scale)))
    def anydouble():
        while True:
            x = struct.unpack("<d", struct.pack("<Q", random.getrandbits(64)))[0]
            if not math.isnan(x):
                return x
    c.executemany("insert into bits values (?)", ((anydouble(),) for i in range(400000 // scale)))
    c.commit()
    c.close()


def run(cmd, capture=False):
    """Runs cmd; returns (wall, user, sys, stdout or None)."""
    out = subprocess.PIPE if capture else subprocess.DEVNULL
    t = time.perf_counter()
    p = subprocess.Popen(cmd, stdout=out)
    data = p.stdout.read() if capture else None
    _, status, ru = os.wait4(p.pid, 0)
    wall = time.perf_counter() - t
    p.returncode = os.waitstatus_to_exitcode(status)
    if p.returncode != 0:
        sys.exit("failed (%d): %s" % (p.returncode, " ".join(cmd)))
    return wall, ru.ru_utime, ru.ru_stime, data


def best(cmd, runs):
    return min((run(cmd) for _ in range(runs)), key=lambda r: r[0])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ovid", default=os.path.join(HERE, "..", "..", "..", "bin", "ovid"))
    ap.add_argument("--runs", type=int, default=5)
    ap.add_argument("--small", action="store_true", help="a tenth of the rows")
    ap.add_argument("--levels", action="store_true", help="also time c-O1 and c-noinl")
    a = ap.parse_args()

    builds = build(a.ovid)
    db = os.path.join(OUT, "small.db" if a.small else "bench.db")
    makedb(db, 10 if a.small else 1)
    seek = str((2000000 // (10 if a.small else 1)) - 1)

    def cmd(prog, table, mode):
        if prog == "sqlite3":
            q = "select * from " + table
            if mode == "seek":
                q += " where rowid=" + seek
            return ["sqlite3", db, q]
        argv = [os.path.join(OUT, prog), db, table]
        if mode == "count":
            argv.append("--count")
        if mode == "seek":
            argv.append(seek)
        return argv

    native = ["ovid", "c-O0"] + (["c-O1", "c-noinl"] if a.levels else []) + ["c-O2", "api"]
    cases = [(t, m) for t in ("ints", "texts", "reals", "bits") for m in ("print", "count")]
    cases.append(("ints", "seek"))

    for table, mode in cases:
        want = run(cmd("api", table, mode), capture=True)[3]
        progs = native + ([] if mode == "count" else ["sqlite3"])
        for prog in progs:
            if run(cmd(prog, table, mode), capture=True)[3] != want:
                sys.exit("output differs from api: %s %s %s" % (prog, table, mode))

    print("database: %d MB; best of %d runs" % (os.path.getsize(db) >> 20, a.runs))
    print()
    print("build      seconds   bytes")
    for prog in ["ovid", "c-O0", "c-O1", "c-noinl", "c-O2", "api"]:
        print("%-8s %9.3f %7d" % (prog, builds[prog], os.path.getsize(os.path.join(OUT, prog))))
    print()
    print("%-14s %-8s %8s %8s %8s %7s" % ("case", "program", "wall ms", "user ms", "sys ms", "x c-O2"))
    for table, mode in cases:
        progs = native + ([] if mode == "count" else ["sqlite3"])
        res = {p: best(cmd(p, table, mode), a.runs if mode != "seek" else a.runs * 4) for p in progs}
        for p in progs:
            w, u, s, _ = res[p]
            print("%-14s %-8s %8.1f %8.1f %8.1f %7.2f" %
                  (table + " " + mode, p, w * 1e3, u * 1e3, s * 1e3, w / res["c-O2"][0]))
        print()


if __name__ == "__main__":
    main()
