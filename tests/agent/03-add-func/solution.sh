set -e
ovid insert --after fn:shop.Total <<'EOF'
// Discount takes 10% off a total of 100 or more.
func Discount(total i64) i64 {
  if total >= 100 {
    return total * 9 / 10
  }
  return total
}
EOF
ovid replace st:shop.main:1 --force <<'EOF'
ovid/io.PrintInt(io, Discount(Total()))
EOF
cat > shop/main_test.ov <<'EOF'
package shop

import ovid/io

func TestDiscount(io *ovid/io.Cap) i64 {
  if Discount(50) != 50 || Discount(200) != 180 {
    return 1
  }
  return 0
}
EOF
