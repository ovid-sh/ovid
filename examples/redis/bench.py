#!/usr/bin/env python3
"""Throughput of miniredis against a real server, with valkey-benchmark.

  bench.py [--ovid path/to/ovid] [--dir where/valkey/binaries/are] [--quick]

Both servers are single-threaded, on 127.0.0.1, with persistence off, and
pinned to one CPU; the load generator is pinned to others. Each case runs
without pipelining (one request per round trip, where system calls
dominate) and with 16 requests per round trip (where the server's own work
shows). Needs Linux x86-64, taskset, and valkey-server and valkey-benchmark
(or the redis- ones).
"""
import argparse, csv, io, os, shutil, socket, subprocess, sys, tempfile, time

HERE = os.path.dirname(os.path.abspath(__file__))
SERVER_CPU = "0"
CLIENT_CPUS = "2-15"


def find(d, *names):
    for n in names:
        p = os.path.join(d, n) if d else shutil.which(n)
        if p and os.path.exists(p):
            return p
    sys.exit("not found: " + " or ".join(names) + " (pass --dir)")


def wait_port(port):
    for _ in range(100):
        try:
            socket.create_connection(("127.0.0.1", port)).close()
            return
        except OSError:
            time.sleep(0.05)
    sys.exit("no server on port %d" % port)


def run(bench, port, tests, n, pipeline, keyspace):
    cmd = ["taskset", "-c", CLIENT_CPUS, bench, "-p", str(port), "-t", tests, "-n", str(n), "-c", "50",
           "-P", str(pipeline), "--threads", "8", "--csv"]
    if keyspace:
        cmd += ["-r", str(keyspace)]
    out = subprocess.run(cmd, check=True, capture_output=True, text=True).stdout
    rows = {}
    for r in csv.DictReader(io.StringIO(out)):
        rows[r["test"]] = (float(r["rps"]), float(r["p50_latency_ms"]), float(r["p99_latency_ms"]))
    return rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ovid", default=os.path.join(HERE, "..", "..", "bin", "ovid"))
    ap.add_argument("--dir", default="", help="directory holding valkey-server and valkey-benchmark")
    ap.add_argument("--quick", action="store_true", help="a tenth of the requests: too few for stable numbers")
    a = ap.parse_args()
    server = find(a.dir, "valkey-server", "redis-server")
    bench = find(a.dir, "valkey-benchmark", "redis-benchmark")
    tmp = tempfile.mkdtemp()
    mini = os.path.join(tmp, "miniredis")
    subprocess.run([a.ovid, "build", "-C", HERE, "-o", mini], check=True, stdout=subprocess.DEVNULL)
    targets = [("valkey", 7711, ["taskset", "-c", SERVER_CPU, server, "--port", "7711", "--save", "",
                                 "--appendonly", "no", "--dir", tmp]),
               ("miniredis", 7712, ["taskset", "-c", SERVER_CPU, mini, "7712"])]
    scale = 10 if a.quick else 1
    # Enough requests that a run lasts seconds: the tool's clock is coarse.
    cases = [("no pipeline", 1, 1000000 // scale, 0),
             ("pipeline 16", 16, 10000000 // scale, 0),
             ("pipeline 16, 1M random keys", 16, 10000000 // scale, 1000000)]
    tests = "ping_mbulk,set,get,incr"
    results = {}
    for name, port, cmd in targets:
        # One server at a time, so they do not share the CPU they are pinned to.
        p = subprocess.Popen(cmd, stdout=subprocess.DEVNULL)
        try:
            wait_port(port)
            for label, pipeline, n, keyspace in cases:
                results[(name, label)] = run(bench, port, tests, n, pipeline, keyspace)
        finally:
            p.kill()
            p.wait()
    shutil.rmtree(tmp, ignore_errors=True)
    print("requests per second (p50 / p99 latency in ms), 50 connections")
    for label, _, _, _ in cases:
        print()
        print(label)
        print("  %-12s %28s %28s %7s" % ("test", "valkey", "miniredis", "ratio"))
        for t in results[("valkey", label)]:
            v, m = results[("valkey", label)][t], results[("miniredis", label)][t]
            cell = lambda x: "%10.0f (%.3f / %.3f)" % x
            print("  %-12s %28s %28s %6.2fx" % (t, cell(v), cell(m), m[0] / v[0]))


if __name__ == "__main__":
    main()
