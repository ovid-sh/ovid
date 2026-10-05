# The edit ran before the connection dropped. Sent again exactly as it was,
# the request must be refused as stale (exit 2), writing nothing.
code=0
ovid edit fix.json || code=$?
[ "$code" = 2 ] || { echo "the replay exited $code, want 2"; exit 1; }
