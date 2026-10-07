#!/usr/bin/env bash
# bench-wasm.sh <label> <module dir>
#
# Run from the repository root, after `npm install` in experiments/async/wasm.
# Builds the module with --target wasm, serves it from `wrangler dev`
# (workerd, local) with experiments/async/wasm/worker.js, or the module's own
# worker.js and *.mjs when it has them, then runs loadgen over /w1 and /w2
# at each concurrency and appends JSON lines to
# experiments/async/results/<label>-wasm.jsonl. Shares bench-native.sh's lock.
#
# Env: PORT (8787), DURATION (10s), CONCURRENCY ("1 10 100 1000"),
# PATHS ("/w1 /w2").
set -euo pipefail

label=${1:?label}
mod=${2:?module dir}
PORT=${PORT:-8787}
DURATION=${DURATION:-10s}
CONCURRENCY=${CONCURRENCY:-"1 10 100 1000"}
PATHS=${PATHS:-"/w1 /w2"}

root=$(pwd)
wasmdir="$root/experiments/async/wasm"
out="$root/experiments/async/results/$label-wasm.jsonl"
mkdir -p "$(dirname "$out")"
tmp=$(mktemp -d)
dev=""
# wrangler runs in a process group of its own (setsid), so its node,
# esbuild, and workerd processes all go with it.
cleanup() {
	[ -n "$dev" ] && kill -KILL -- "-$dev" 2>/dev/null || true
	rm -rf "$tmp"
}
trap cleanup EXIT

exec 9>/tmp/ovid-async-bench.lock
echo "waiting for the bench lock..." >&2
flock 9

ulimit -n 65536
go build -buildvcs=false -o "$tmp/ovid" ./cmd/ovid
go build -buildvcs=false -o "$tmp/loadgen" ./experiments/async/cmd/loadgen
mkdir "$tmp/w"
"$tmp/ovid" build -C "$mod" --target wasm -o "$tmp/w/app.wasm" >&2
cp internal/wasm/host.mjs "$tmp/w/"
if [ -f "$mod/worker.js" ]; then
	cp "$mod/worker.js" "$tmp/w/"
	cp "$mod"/*.mjs "$tmp/w/" 2>/dev/null || true
else
	cp "$wasmdir/worker.js" "$tmp/w/"
fi
cat >"$tmp/w/wrangler.jsonc" <<EOF
{ "name": "ovid-async-$(echo "$label" | tr 'A-Z_' 'a-z-')", "main": "worker.js", "compatibility_date": "2026-10-01" }
EOF

(cd "$wasmdir" && exec setsid "$wasmdir/node_modules/.bin/wrangler" dev -c "$tmp/w/wrangler.jsonc" --ip 127.0.0.1 --port "$PORT" \
	--inspector-port $((PORT + 1)) --log-level warn >"$tmp/dev.log" 2>&1) &
dev=$!
# A listener left from a run that was killed would answer for us.
sleep 0.5
if ! kill -0 "$dev" 2>/dev/null; then cat "$tmp/dev.log" >&2; exit 1; fi
for _ in $(seq 1 60); do
	curl -s -o /dev/null "http://127.0.0.1:$PORT/w1" && break
	sleep 1
done
curl -sf -o /dev/null "http://127.0.0.1:$PORT/w1" || { cat "$tmp/dev.log" >&2; exit 1; }

# tree_rss_kb: resident memory of the workerd processes under wrangler,
# leaving out wrangler's own node and esbuild.
tree_rss_kb() {
	local all=$dev frontier=$dev next
	while [ -n "$frontier" ]; do
		next=$(for p in $frontier; do pgrep -P "$p" || true; done | paste -sd' ' -)
		[ -n "$next" ] && all="$all $next"
		frontier=$next
	done
	ps -o rss=,comm= -p "$(echo $all | tr ' ' ,)" 2>/dev/null | awk '$2 ~ /workerd/ {s+=$1} END {print s+0}'
}

host=$(hostname)
commit=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
bytes=$(wc -c <"$tmp/w/app.wasm" | tr -d ' ')
for path in $PATHS; do
	for c in $CONCURRENCY; do
		peak=0
		"$tmp/loadgen" -addr "http://127.0.0.1:$PORT" -path "$path" -c "$c" -d "$DURATION" -label "$label" >"$tmp/line" &
		lg=$!
		while kill -0 "$lg" 2>/dev/null; do
			r=$(tree_rss_kb)
			[ "$r" -gt "$peak" ] && peak=$r
			sleep 0.25
		done
		wait "$lg"
		sed -e "s/}\$/,\"peak_rss_kb\":$peak,\"binary_bytes\":$bytes,\"platform\":\"wasm-workerd-dev\",\"host\":\"$host\",\"commit\":\"$commit\"}/" "$tmp/line" | tee -a "$out"
	done
done
