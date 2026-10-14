# The version that was read is stale: the edit must be refused with exit 2.
code=0
ovid replace fn:price.Price --expect 0094bd675479 <<'EOF' || code=$?
// Price is what n items cost, in cents.
func Price(n i64) i64 {
  if n <= 0 {
    return 0
  }
  return n * 250
}
EOF
[ "$code" = 2 ] || { echo "stale replace exited $code, want 2"; exit 1; }
set -e
# Merge with what is there now, guarded by its hash.
h=$(ovid show fn:price.Price | sed -n '1s/.*hash=//p')
ovid insert --before st:price.Price:1 --expect "$h" <<'EOF'
if n <= 0 {
  return 0
}
EOF
