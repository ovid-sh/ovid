#!/bin/sh
# Builds sqlread and diffs its output against the sqlite3 shell on databases
# sqlite3 itself creates. Needs sqlite3 on PATH and a Linux x86-64 host.
#   ./verify.sh [path/to/ovid]
set -eu
cd "$(dirname "$0")"
OVID=${1:-../../bin/ovid}
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
"$OVID" build -o "$T/sqlread" >/dev/null
fails=0

mk() { # mk <page_size>: a database with every case the reader handles
  db="$T/p$1.db"
  sqlite3 "$db" <<EOF
PRAGMA page_size=$1;
CREATE TABLE ints(id INTEGER PRIMARY KEY, v INTEGER);
INSERT INTO ints(v) VALUES (0),(1),(-1),(127),(128),(-128),(-129),(32767),(32768),
  (8388607),(8388608),(2147483647),(2147483648),(-2147483649),(140737488355327),
  (140737488355328),(9223372036854775807),(-9223372036854775808),(NULL);
CREATE TABLE mixed(a, b TEXT, c BLOB, d);
INSERT INTO mixed VALUES (1,'one',x'414243',NULL),(NULL,'',x'',2),('text',NULL,'x',-3.5);
CREATE TABLE big("row id" INTEGER PRIMARY KEY, n INT, s TEXT);
WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM g WHERE i<30000)
  INSERT INTO big SELECT i*3, i*i, substr('abcdefghijklmnopqrstuvwxyz', 1, i%27) FROM g;
DELETE FROM big WHERE n%7=0;
CREATE TABLE spill(k INTEGER PRIMARY KEY, body TEXT);
WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM g WHERE i<40)
  INSERT INTO spill SELECT i, printf('%d:', i) || hex(zeroblob(i*i*37)) FROM g;
CREATE TABLE reals(r REAL);
INSERT INTO reals VALUES (0.0),(-0.0),(1.0),(-1.5),(0.1),(1e15),(1e16),(123456789012345.6),
  (1e-4),(1e-5),(1.7976931348623157e308),(5e-324),(2.2250738585072014e-308),(1e100),(1e-100),
  (0.3),(2.5),(1000000000000002.5),(999999999999999.9),(9.9999999999999995e22),(1e999),(-1e999),
  (3.141592653589793),(1.0/3.0),(2.0/3.0);
WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM g WHERE i<4000)
  INSERT INTO reals SELECT (random()/7.0) * pow(10.0, random()%300) FROM g;
INSERT INTO reals SELECT random()/1e6 FROM reals LIMIT 2000;
CREATE TABLE empty(x);
EOF
  echo "$db"
}

same() { # same <label> <expected file> <actual file>
  if cmp -s "$2" "$3"; then
    echo "ok    $1"
  else
    echo "FAIL  $1"
    diff "$2" "$3" | head -5
    fails=$((fails + 1))
  fi
}

for ps in 512 4096 65536; do
  db=$(mk $ps)
  sqlite3 "$db" "SELECT name FROM sqlite_schema WHERE type='table'" >"$T/want"
  "$T/sqlread" "$db" >"$T/got"
  same "page $ps: tables" "$T/want" "$T/got"
  for t in ints mixed big spill reals empty; do
    sqlite3 "$db" "SELECT * FROM $t" >"$T/want"
    "$T/sqlread" "$db" "$t" >"$T/got"
    same "page $ps: $t ($(wc -l <"$T/want") rows)" "$T/want" "$T/got"
  done
  for id in 3 6 45000 89997 90000 21 -5; do
    sqlite3 "$db" "SELECT * FROM big WHERE rowid=$id" >"$T/want"
    "$T/sqlread" "$db" big "$id" >"$T/got"
    same "page $ps: big rowid $id" "$T/want" "$T/got"
  done
done

if [ "$fails" -ne 0 ]; then
  echo "$fails failed"
  exit 1
fi
echo "all passed"
