#!/usr/bin/env bash
# bench-native.sh <label> <module dir> [server args...]
#
# Run on Linux x86-64 from the repository root. Builds bin/ovid, the
# upstream simulator, and loadgen; builds the module; starts upstream and
# the server; runs loadgen over /w1 and /w2 at each concurrency; and appends
# one JSON line per run to experiments/async/results/<label>-native.jsonl.
#
# The server is started as: <binary> <port> <upstream port> [server args...]
# Every run of this script, from any checkout, holds one lock, so two
# benchmarks never share the machine.
#
# Env: PORT (8080), UPSTREAM_PORT (9100), DURATION (10s), CONCURRENCY
# ("1 10 100 1000"), PATHS ("/w1 /w2").
set -euo pipefail

label=${1:?label}
mod=${2:?module dir}
shift 2
PORT=${PORT:-8080}
UPSTREAM_PORT=${UPSTREAM_PORT:-9100}
DURATION=${DURATION:-10s}
CONCURRENCY=${CONCURRENCY:-"1 10 100 1000"}
PATHS=${PATHS:-"/w1 /w2"}

root=$(pwd)
out="$root/experiments/async/results/$label-native.jsonl"
mkdir -p "$(dirname "$out")"
tmp=$(mktemp -d)
pids=()
# Each process runs in a session of its own (setsid), so whatever it
# forks goes with it.
cleanup() {
	for p in "${pids[@]}"; do
		kill -KILL -- "-$p" 2>/dev/null || true
	done
	rm -rf "$tmp"
}
trap cleanup EXIT

exec 9>/tmp/ovid-async-bench.lock
echo "waiting for the bench lock..." >&2
flock 9

ulimit -n 65536
go build -buildvcs=false -o "$tmp/ovid" ./cmd/ovid
go build -buildvcs=false -o "$tmp/upstream" ./experiments/async/cmd/upstream
go build -buildvcs=false -o "$tmp/loadgen" ./experiments/async/cmd/loadgen
"$tmp/ovid" build -C "$mod" -o "$tmp/server" >&2

setsid "$tmp/upstream" -port "$UPSTREAM_PORT" &
pids+=($!)
setsid "$tmp/server" "$PORT" "$UPSTREAM_PORT" "$@" &
server=$!
pids+=($server)
sleep 0.5
kill -0 "$server" || { echo "server exited at start" >&2; exit 1; }

# rss_kb: the resident memory of the server's session: it and every
# process it forked.
rss_kb() {
	ps -o rss= -s "$server" 2>/dev/null | awk '{s+=$1} END {print s+0}'
}

host=$(hostname)
commit=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
bytes=$(stat -c %s "$tmp/server")
for path in $PATHS; do
	for c in $CONCURRENCY; do
		peak=0
		"$tmp/loadgen" -addr "127.0.0.1:$PORT" -path "$path" -c "$c" -d "$DURATION" -label "$label" >"$tmp/line" &
		lg=$!
		while kill -0 "$lg" 2>/dev/null; do
			r=$(rss_kb)
			[ "$r" -gt "$peak" ] && peak=$r
			sleep 0.25
		done
		wait "$lg"
		alive=true
		kill -0 "$server" 2>/dev/null || alive=false
		# Add what loadgen cannot see: the server's memory, binary size,
		# whether it survived, and where it ran.
		sed -e "s/}\$/,\"peak_rss_kb\":$peak,\"binary_bytes\":$bytes,\"server_alive\":$alive,\"platform\":\"native\",\"host\":\"$host\",\"commit\":\"$commit\"}/" "$tmp/line" | tee -a "$out"
		$alive || { echo "server died" >&2; exit 1; }
	done
done
