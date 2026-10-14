set -e
ovid init squares --name squares
cat > squares/squares/main.ov <<'EOF'
package squares

import ovid/io

func Square(n i64) i64 {
  return n * n
}

func main(io *ovid/io.Cap) i64 {
  ovid/io.PrintInt(io, Square(12))
  ovid/io.Print(io, "\n")
  return 0
}
EOF
cat > squares/squares/main_test.ov <<'EOF'
package squares

import ovid/io

func TestSquare(io *ovid/io.Cap) i64 {
  if Square(7) != 49 {
    return 1
  }
  return 0
}
EOF
