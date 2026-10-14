package ir

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// Encode writes p to w as the indented JSON Marshal returns, without ever
// holding the document: one func is encoded at a time and indented as it is
// written. Marshal builds the whole text, and several copies of its parts on
// the way, which cost a dump more memory than checking the module did.
func Encode(w io.Writer, p *Program) error {
	if p == nil || p.Packages == nil {
		raw, err := Marshal(p)
		if err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	in := &indenter{w: bw}
	// The program and each package are encoded without their last list,
	// and the list's elements follow one by one. That the list is the last
	// key is what TestEncodeIsMarshal holds.
	head := *p
	head.Packages = []Package{}
	raw, err := compact(&head)
	if err != nil {
		return err
	}
	in.write(bytes.TrimSuffix(raw, []byte("[]}")))
	in.write([]byte("["))
	for i := range p.Packages {
		if i > 0 {
			in.write([]byte(","))
		}
		pkg := p.Packages[i]
		funcs := pkg.Funcs
		pkg.Funcs = nil
		if raw, err = compact(&pkg); err != nil {
			return err
		}
		if len(funcs) == 0 {
			in.write(raw)
			continue
		}
		in.write(bytes.TrimSuffix(raw, []byte("}")))
		in.write([]byte(`,"funcs":[`))
		for j := range funcs {
			if j > 0 {
				in.write([]byte(","))
			}
			if raw, err = compact(&funcs[j]); err != nil {
				return err
			}
			in.write(raw)
		}
		in.write([]byte("]}"))
	}
	in.write([]byte("]}"))
	bw.WriteByte('\n')
	return bw.Flush()
}

// compact is v as JSON on one line, with <, >, and & as themselves.
func compact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// indenter writes compact JSON as encoding/json indents it with two
// spaces: a line for each member and element, a space after each colon,
// and empty objects and arrays left closed on one line.
type indenter struct {
	w      *bufio.Writer
	depth  int
	inStr  bool
	esc    bool
	opened bool // the last byte opened an object or array: its line break is still owed
}

func (in *indenter) newline() {
	in.w.WriteByte('\n')
	for i := 0; i < in.depth; i++ {
		in.w.WriteString("  ")
	}
}

func (in *indenter) write(p []byte) {
	for _, c := range p {
		if in.inStr {
			in.w.WriteByte(c)
			switch {
			case in.esc:
				in.esc = false
			case c == '\\':
				in.esc = true
			case c == '"':
				in.inStr = false
			}
			continue
		}
		if c == '}' || c == ']' {
			in.depth--
			if !in.opened {
				in.newline()
			}
			in.opened = false
			in.w.WriteByte(c)
			continue
		}
		if in.opened {
			in.opened = false
			in.newline()
		}
		switch c {
		case '{', '[':
			in.w.WriteByte(c)
			in.depth++
			in.opened = true
		case ',':
			in.w.WriteByte(c)
			in.newline()
		case ':':
			in.w.WriteString(": ")
		case '"':
			in.inStr = true
			in.w.WriteByte(c)
		default:
			in.w.WriteByte(c)
		}
	}
}
