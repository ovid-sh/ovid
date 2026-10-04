# Two writers at once, each on its own func of the same file.
ovid replace fn:team.Alpha > a.out <<'EOF' &
func Alpha(x i64) i64 {
  return x + 10
}
EOF
ovid replace fn:team.Beta > b.out <<'EOF' &
func Beta(x i64) i64 {
  return x * 3
}
EOF
wait
cat a.out b.out
rm a.out b.out
