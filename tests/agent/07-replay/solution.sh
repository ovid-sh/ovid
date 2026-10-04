# The lost request, sent again exactly as it was: the guard must refuse it
# as stale (exit 2), since the statement it named is gone.
code=0
ovid delete st:tally.Count:2 --expect adb929245855 || code=$?
[ "$code" = 2 ] || { echo "the replay exited $code, want 2"; exit 1; }
