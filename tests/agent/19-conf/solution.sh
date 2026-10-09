set -e
ovid replace fn:conf.Get --force <<'OV'
func Get(io *ovid/io.Cap, path bytes, key bytes) (i64, i64) {
  var data bytes, e i64 = ovid/io.ReadFile(io, path)
  if e != 0 {
    return 0, e
  }
  var text bytes = ""
  text, e = Find(data, key)
  if e != 0 {
    return 0, e
  }
  return Num(text)
}
OV
ovid replace fn:conf.main --force <<'OV'
func main(io *ovid/io.Cap) i64 {
  if ovid/io.Argc(io) != 3 {
    ovid/io.Eprint("usage: conf FILE KEY\n")
    return 64
  }
  var file bytes = ovid/io.Arg(io, 1)
  var key bytes = ovid/io.Arg(io, 2)
  var v i64, e i64 = Get(io, file, key)
  if e == E_SYNTAX {
    Say(file, "bad line")
    return 3
  } else if e == E_NOKEY {
    Say(key, "not set")
    return 4
  } else if e == E_NOTNUM {
    Say(key, "not a number")
    return 5
  } else if e != 0 {
    Say(file, ovid/io.ErrText(e))
    return 2
  }
  ovid/io.PrintInt(io, v)
  ovid/io.Print("\n")
  return 0
}
OV
