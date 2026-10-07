#!/usr/bin/env bash
# try-native.sh: build POC C's native server and the upstream simulator,
# start both on 30080/30100, and hit /w1, /w2, and a missing path once
# each. Run on Linux from the repository root.
set -euo pipefail
tmp=$(mktemp -d)
pids=()
trap 'for p in "${pids[@]}"; do kill -KILL -- "-$p" 2>/dev/null || true; done; rm -rf "$tmp"' EXIT
go build -buildvcs=false -o "$tmp/ovid" ./cmd/ovid
go build -buildvcs=false -o "$tmp/upstream" ./experiments/async/cmd/upstream
"$tmp/ovid" build -C experiments/async/C/native -o "$tmp/server" >/dev/null
setsid "$tmp/upstream" -port 30100 &
pids+=($!)
setsid "$tmp/server" 30080 30100 &
pids+=($!)
sleep 0.3
for p in /w1 /w2 /nope; do
	/usr/bin/time -f "$p %es" curl -s -m 5 -o "$tmp/body" -w "%{http_code} " "http://127.0.0.1:30080$p" || echo "curl failed"
	cat "$tmp/body"
	echo
done
kill -0 "${pids[1]}" && echo "server alive"
