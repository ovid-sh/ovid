set -e
h=$(ovid show fn:calc.Mid | sed -n '1s/.*hash=//p')
ovid replace fn:calc.Mid --expect "$h" <<'EOF'
func Mid(lo i64, hi i64) i64 {
  return lo + (hi - lo) / 2
}
EOF
sed -i.bak 's/hi - lo + one/hi - lo + 1/' calc/span.ov && rm calc/span.ov.bak
