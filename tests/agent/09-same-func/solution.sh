set -e
# Both writers read Scale at hash 589bb36e35d9. The first one lands.
ovid replace fn:team.Scale --expect 589bb36e35d9 <<'EOF'
func Scale(x i64) i64 {
  if x < 0 {
    return 0
  }
  return x * 3
}
EOF
# The second is refused as stale (exit 2) rather than overwriting it...
code=0
ovid replace fn:team.Scale --expect 589bb36e35d9 <<'EOF' || code=$?
func Scale(x i64) i64 {
  return x * 4
}
EOF
[ "$code" = 2 ] || { echo "second writer exited $code, want 2"; exit 1; }
# ...and changes what is there now.
h=$(ovid show fn:team.Scale | sed -n '1s/.*hash=//p')
ovid replace fn:team.Scale --expect "$h" <<'EOF'
func Scale(x i64) i64 {
  if x < 0 {
    return 0
  }
  return x * 4
}
EOF
