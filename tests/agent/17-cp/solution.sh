set -e
cat > cp/main.ov <<'OV'
package cp

import ovid/io

// Fail prints "cp: NAME: WHY" to standard error and returns 1, the exit
// code.
func Fail(name bytes, why bytes) i64 {
  ovid/io.Eprint("cp: ")
  ovid/io.Eprint(name)
  ovid/io.Eprint(": ")
  ovid/io.Eprint(why)
  ovid/io.Eprint("\n")
  return 1
}

// main copies the file SRC to DST: cp SRC DST. DST is created, or
// replaced if it is a file, and gets permission 0644.
func main(io *ovid/io.Cap) i64 {
  if ovid/io.Argc(io) != 3 {
    ovid/io.Eprint("usage: cp SRC DST\n")
    return 1
  }
  var src bytes = ovid/io.Arg(io, 1)
  var dst bytes = ovid/io.Arg(io, 2)
  var dir bool, de i64 = ovid/io.IsDir(io, src)
  if de != 0 {
    return Fail(src, ovid/io.ErrText(de))
  }
  if dir {
    return Fail(src, "is a directory")
  }
  var data bytes, e i64 = ovid/io.ReadFile(io, src)
  if e != 0 {
    return Fail(src, ovid/io.ErrText(e))
  }
  e = ovid/io.WriteFile(io, dst, data, 420)
  if e != 0 {
    return Fail(dst, ovid/io.ErrText(e))
  }
  return 0
}
OV
