# Two writers at once, each on its own func of the same file, each guarded
# by the hash it read before either wrote.
a=$(ovid show fn:team.Alpha | sed -n '1s/.*hash=//p')
b=$(ovid show fn:team.Beta | sed -n '1s/.*hash=//p')
ovid replace fn:team.Alpha --expect "$a" > a.out <<'EOF' &
func Alpha(x i64) i64 {
  return x + 10
}
EOF
ovid replace fn:team.Beta --expect "$b" > b.out <<'EOF' &
func Beta(x i64) i64 {
  return x * 3
}
EOF
wait
cat a.out b.out
rm a.out b.out
