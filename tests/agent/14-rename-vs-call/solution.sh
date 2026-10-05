# The rename lands first. The other writer's Bulk, written against the
# module it read, still calls Calc: ovid must refuse it (it adds a check
# error) and write nothing; sent again with the new name, it lands.
set -e
ovid rename Calc WithTax
bulk() {
  ovid append shop <<EOF2
// Bulk is the price of n items that each cost each before tax; an order
// of 10 or more takes 5 off each item's price before tax.
func Bulk(n i64, each i64) i64 {
  if n >= 10 {
    return n * $1(each - 5)
  }
  return n * $1(each)
}
EOF2
}
code=0
bulk Calc || code=$?
[ "$code" = 1 ] || { echo "Bulk calling Calc after the rename exited $code, want 1"; exit 1; }
bulk WithTax
h=$(ovid show fn:shop.main | sed -n '1s/.*hash=//p')
ovid insert --before st:shop.main:3 --expect "$h" <<'EOF2'
ovid/io.PrintInt(io, Bulk(12, 105))
ovid/io.Print(strptr("\n"))
EOF2
