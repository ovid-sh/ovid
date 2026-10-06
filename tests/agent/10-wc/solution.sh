set -e
cat > wc/main.ov <<'EOF'
package wc

import ovid/io

// IsSpace reports whether c separates words.
func IsSpace(c i64) bool {
  return c == 32 || c == 9 || c == 10 || c == 13
}

func main(io *ovid/io.Cap) i64 {
  if ovid/io.Argc(io) < 2 {
    ovid/io.Eprint(strptr("usage: wc FILE\n"))
    return 1
  }
  var name i64 = ovid/io.Arg(io, 1)
  var nn i64 = ovid/io.Alloc(io, 8)
  var data i64, e i64 = ovid/io.ReadFile(io, name, ovid/io.CLen(name), nn)
  if e != 0 {
    ovid/io.Eprint(strptr("wc: cannot read the file\n"))
    return 1
  }
  var p i64 = data
  var n i64 = load64(nn)
  var lines i64 = 0
  var words i64 = 0
  var inword bool = false
  var i i64 = 0
  while i < n {
    var c i64 = load8(p + i)
    if c == 10 {
      lines = lines + 1
    }
    if IsSpace(c) {
      inword = false
    } else if !inword {
      inword = true
      words = words + 1
    }
    i = i + 1
  }
  ovid/io.PrintInt(io, lines)
  ovid/io.Print(strptr(" "))
  ovid/io.PrintInt(io, words)
  ovid/io.Print(strptr(" "))
  ovid/io.PrintInt(io, n)
  ovid/io.Print(strptr("\n"))
  return 0
}
EOF
