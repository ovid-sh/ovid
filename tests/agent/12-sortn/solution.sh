set -e
cat > sortn/main.ov <<'EOF'
package sortn

import ovid/io
import ovid/mem

const MinI64 i64 = -9223372036854775807 - 1

// Sort sorts the n values at p ascending, in place: a bottom-up merge sort
// through a scratch array.
func Sort(io *ovid/io.Cap, p i64, n i64) i64 {
  if n < 2 {
    return 0
  }
  var src i64 = p
  var dst i64 = ovid/io.Alloc(io, n * 8)
  var w i64 = 1
  while w < n {
    var lo i64 = 0
    while lo < n {
      var mid i64 = lo + w
      if mid > n {
        mid = n
      }
      var hi i64 = mid + w
      if hi > n {
        hi = n
      }
      var i i64 = lo
      var j i64 = mid
      var k i64 = lo
      while k < hi {
        if i < mid && (j >= hi || load64(src + i * 8) <= load64(src + j * 8)) {
          store64(dst + k * 8, load64(src + i * 8))
          i = i + 1
        } else {
          store64(dst + k * 8, load64(src + j * 8))
          j = j + 1
        }
        k = k + 1
      }
      lo = hi
    }
    var t i64 = src
    src = dst
    dst = t
    w = w * 2
  }
  if src != p {
    ovid/mem.CopyN(p, src, n * 8)
  }
  return 0
}

// Parse stores at out the integer the n bytes at p spell (an optional "-"
// and decimal digits) and returns 0, or returns 1 if they spell none or
// one outside i64. It counts down from 0, so MinI64 parses.
func Parse(p i64, n i64, out i64) i64 {
  var i i64 = 0
  var neg bool = false
  if n > 0 && load8(p) == 45 {
    neg = true
    i = 1
  }
  if i == n {
    return 1
  }
  var acc i64 = 0
  var bad bool = false
  while i < n && !bad {
    var d i64 = load8(p + i) - 48
    if d < 0 || d > 9 || acc < MinI64 / 10 {
      bad = true
    } else {
      acc = acc * 10
      if acc < MinI64 + d {
        bad = true
      }
      acc = acc - d
    }
    i = i + 1
  }
  if bad || (!neg && acc == MinI64) {
    return 1
  }
  if !neg {
    acc = 0 - acc
  }
  store64(out, acc)
  return 0
}

func main(io *ovid/io.Cap) i64 {
  var size i64 = 65536
  var buf i64 = ovid/io.Alloc(io, size)
  var n i64 = 0
  var r i64 = 1
  var e i64 = 0
  while r > 0 && e == 0 {
    if n == size {
      var bigger i64 = ovid/io.Alloc(io, size * 2)
      ovid/mem.CopyN(bigger, buf, n)
      buf = bigger
      size = size * 2
    }
    r, e = ovid/io.Read(0, bytes(buf + n, size - n))
    n = n + r
  }
  if e != 0 {
    ovid/io.Eprint("sortn: cannot read standard input\n")
    return 1
  }
  // At most one value per newline, plus a last line without one.
  var most i64 = 1
  var i i64 = 0
  while i < n {
    if load8(buf + i) == 10 {
      most = most + 1
    }
    i = i + 1
  }
  var vals i64 = ovid/io.Alloc(io, most * 8)
  var count i64 = 0
  var start i64 = 0
  i = 0
  while i <= n {
    if i == n || load8(buf + i) == 10 {
      if i > start {
        if Parse(buf + start, i - start, vals + count * 8) != 0 {
          ovid/io.Eprint("sortn: a line is not an i64\n")
          return 1
        }
        count = count + 1
      }
      start = i + 1
    }
    i = i + 1
  }
  Sort(io, vals, count)
  var out *ovid/mem.Buf = ovid/mem.New(io, count * 21 + 64)
  var tmp i64 = ovid/io.Alloc(io, 64)
  i = 0
  while i < count {
    var m i64 = ovid/mem.FormatI64(tmp, load64(vals + i * 8))
    ovid/mem.WBytes(io, out, bytes(tmp, m))
    ovid/mem.WByte(io, out, 10)
    i = i + 1
  }
  ovid/io.Stdout(bytes(out.data, out.len))
  return 0
}
EOF
