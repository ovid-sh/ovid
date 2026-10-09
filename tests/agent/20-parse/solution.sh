set -e
cat >> num/num.ov <<'OV'

// ParseI64 returns the integer s spells, an optional "-" and one or more
// decimal digits, and 0; or 0 and E_SYNTAX if s is not that, or 0 and
// E_RANGE if its value is outside i64. It counts down from 0, so the most
// negative i64 parses.
func ParseI64(s bytes) (i64, i64) {
  var min i64 = -9223372036854775807 - 1
  var i i64 = 0
  var neg bool = len(s) > 0 && s[0] == 45
  if neg {
    i = 1
  }
  if i == len(s) {
    return 0, E_SYNTAX
  }
  var acc i64 = 0
  var e i64 = 0
  while i < len(s) {
    if !IsDigit(s[i]) {
      return 0, E_SYNTAX
    }
    var d i64 = s[i] - 48
    if acc < min / 10 || acc * 10 < min + d {
      e = E_RANGE
    } else {
      acc = acc * 10 - d
    }
    i = i + 1
  }
  if e != 0 || (!neg && acc == min) {
    return 0, E_RANGE
  }
  if neg {
    return acc, 0
  }
  return 0 - acc, 0
}
OV
cat > sum/main.ov <<'OV'
package sum

import ovid/io
import num

// Fail prints "sum: line N: WHY" and a newline to standard error and
// returns 1, the exit code.
func Fail(io *ovid/io.Cap, n i64, why bytes) i64 {
  ovid/io.Eprint("sum: line ")
  ovid/io.WriteInt(io, 2, n)
  ovid/io.Eprint(": ")
  ovid/io.Eprint(why)
  ovid/io.Eprint("\n")
  return 1
}

// ReadAll reads standard input to its end: the data and 0, or an empty
// bytes and an error code.
func ReadAll(io *ovid/io.Cap) (bytes, i64) {
  var size i64 = 65536
  var buf i64 = ovid/io.Alloc(io, size)
  var n i64 = 0
  var r i64 = 1
  var e i64 = 0
  while r > 0 {
    if n == size {
      var bigger i64 = ovid/io.Alloc(io, size * 2)
      var i i64 = 0
      while i < n {
        store8(bigger + i, load8(buf + i))
        i = i + 1
      }
      buf = bigger
      size = size * 2
    }
    r, e = ovid/io.Read(0, bytes(buf + n, size - n))
    if e != 0 {
      return "", e
    }
    n = n + r
  }
  return bytes(buf, n), 0
}

// main prints the sum of the numbers on standard input, one a line.
func main(io *ovid/io.Cap) i64 {
  var data bytes, e i64 = ReadAll(io)
  if e != 0 {
    ovid/io.Eprint("sum: ")
    ovid/io.Eprint(ovid/io.ErrText(e))
    ovid/io.Eprint("\n")
    return 1
  }
  var max i64 = 9223372036854775807
  var min i64 = -9223372036854775807 - 1
  var sum i64 = 0
  var line i64 = 1
  var start i64 = 0
  var i i64 = 0
  while i <= len(data) {
    if i == len(data) || data[i] == 10 {
      if i > start {
        var v i64, pe i64 = num.ParseI64(data[start:i])
        if pe == num.E_SYNTAX {
          return Fail(io, line, "not a number")
        } else if pe != 0 {
          return Fail(io, line, "out of range")
        }
        if (v > 0 && sum > max - v) || (v < 0 && sum < min - v) {
          return Fail(io, line, "sum overflows")
        }
        sum = sum + v
      }
      start = i + 1
      line = line + 1
    }
    i = i + 1
  }
  ovid/io.PrintInt(io, sum)
  ovid/io.Print("\n")
  return 0
}
OV
