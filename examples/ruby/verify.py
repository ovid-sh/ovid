#!/usr/bin/env python3
"""Runs every tests/*.rb with the interpreter and with Ruby, and compares
standard output byte for byte, and the exit code.

  verify.py [--ovid path/to/ovid] [--ruby path/to/ruby]

Standard error is not compared: the wording of an error belongs to each
implementation. Needs Linux x86-64 and a ruby.
"""
import argparse, glob, os, shutil, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))


def run(argv):
    p = subprocess.run(argv, capture_output=True, timeout=60)
    return p.stdout, p.returncode, p.stderr


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ovid", default=shutil.which("ovid") or os.path.join(HERE, "../../bin/ovid"))
    ap.add_argument("--ruby", default=shutil.which("ruby"))
    a = ap.parse_args()
    if not a.ruby:
        sys.exit("no ruby on PATH; pass --ruby")
    with tempfile.TemporaryDirectory() as tmp:
        rb = os.path.join(tmp, "rb")
        subprocess.run([a.ovid, "build", "-C", HERE, "-o", rb], check=True, stdout=subprocess.DEVNULL)
        failed = 0
        cases = sorted(glob.glob(os.path.join(HERE, "tests", "*.rb")))
        for path in cases:
            name = os.path.relpath(path, HERE)
            got, gcode, gerr = run([rb, path])
            want, wcode, _ = run([a.ruby, path])
            if got == want and gcode == wcode:
                print("ok   %s" % name)
                continue
            failed += 1
            print("FAIL %s: exit %d, ruby %d" % (name, gcode, wcode))
            gl, wl = got.decode(errors="replace").splitlines(), want.decode(errors="replace").splitlines()
            for i in range(max(len(gl), len(wl))):
                g = gl[i] if i < len(gl) else "<missing>"
                w = wl[i] if i < len(wl) else "<missing>"
                if g != w:
                    print("     line %d: got %r, ruby %r" % (i + 1, g, w))
                    break
            if gerr:
                print("     stderr: %s" % gerr.decode(errors="replace").strip())
        print("%d of %d agree" % (len(cases) - failed, len(cases)))
        sys.exit(1 if failed else 0)


main()
