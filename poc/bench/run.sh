#!/bin/sh
# Runs each bench binary 5 times under perf stat; prints output, time,
# instructions, branches, page faults. Run on starship in the bench dir.
cd "$(dirname "$0")"
for d in sum_a sum_e sum_x sum_d sum_c sum_b sum_t sum_u tok_a tok_e tok_x tok_d tok_b; do
  chmod +x $d/bin
  out=$(./$d/bin)
  echo "== $d out=$out"
  perf stat -r 5 -e task-clock,instructions,branches,branch-misses,page-faults ./$d/bin 2>&1 >/dev/null | grep -E 'task-clock|instructions|branches|misses|faults|elapsed' | sed 's/  */ /g'
done
