// Package ir is the Ovid program tree. The source of truth is .ov text; the
// parser builds this tree with an id and a source span on every node. The JSON
// form (ovid dump) is a derived view for tools.
package ir

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// Span is a byte range in one source file. File indexes Program.Files.
// Declarations and nodes also record the token that spells their name
// (NameSpan) and the name in each type they spell (TypeSpan, ResultSpan:
// just T in *path.T), so tools can rewrite a name without scanning text.
type Span struct {
	File int
	Off  int
	End  int
}

type Program struct {
	Revision string    `json:"revision"`
	Files    []string  `json:"-"`
	Module   string    `json:"module"`
	Entry    string    `json:"entry"`
	Packages []Package `json:"packages"`
}

type Package struct {
	ID      string     `json:"id"`
	Path    string     `json:"path"`
	Imports []Import   `json:"imports,omitempty"`
	Consts  []Const    `json:"consts,omitempty"`
	Types   []TypeDecl `json:"types,omitempty"`
	Funcs   []Func     `json:"funcs,omitempty"`
	Span    Span       `json:"-"`
	// Sys says the package may call syscall: it is the toolchain's own
	// copy of a shipped package, not one a module or its std line supplied.
	Sys bool `json:"-"`
}

type Import struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Span Span   `json:"-"`
}

// Const is a const, or a table: a const whose Values are its elements,
// read with Name[i] and len(Name). A table's Type is [N]i64 and its Value
// 0; an empty table's Values are absent from the dump.
type Const struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Value    int64   `json:"value"`
	Table    bool    `json:"table,omitempty"`
	Values   []int64 `json:"values,omitempty"`
	Span     Span    `json:"-"`
	NameSpan Span    `json:"-"`
}

type TypeDecl struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Fields   []Field `json:"fields"`
	Span     Span    `json:"-"`
	NameSpan Span    `json:"-"`
}

type Field struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Span     Span   `json:"-"`
	NameSpan Span   `json:"-"`
	TypeSpan Span   `json:"-"`
}

type Func struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Params  []Param `json:"params,omitempty"`
	Result  string  `json:"result"`
	Result2 string  `json:"result2,omitempty"` // the error code of a func with two results
	// Async marks an async func: called only by await or spawn, and lowered
	// to a frame and a resume function before code generation.
	Async      bool    `json:"async,omitempty"`
	Body       []*Node `json:"body,omitempty"`
	Span       Span    `json:"-"`
	NameSpan   Span    `json:"-"`
	ResultSpan Span    `json:"-"`
}

type Param struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Span     Span   `json:"-"`
	NameSpan Span   `json:"-"`
	TypeSpan Span   `json:"-"`
}

// Node is a statement or expression. ValK selects the JSON "value" payload:
// 1 int, 2 bool, 3 string. For a call, NameSpan is the token of Func. A var2
// or assign2 receives a call's two results into Name and Two.Name (Type and
// Two.Type for a var2; "_" discards); a return with Val2 returns two. Spans
// are not part of the JSON form.
type Node struct {
	ID    string
	Op    string
	Name  string
	Type  string
	Two   *Second
	Pkg   string
	Func  string
	Str   string
	Int   int64
	Bool  bool
	ValK  int
	Left  *Node
	Right *Node
	Arg   *Node
	Base  *Node
	Addr  *Node
	Val   *Node
	Val2  *Node
	Cond  *Node
	Args  []*Node
	Then  []*Node
	Else  []*Node
	Body  []*Node
	Span  Span

	NameSpan Span
	TypeSpan Span
}

// Second is the second name of a var2 or assign2, kept off Node so that
// every other node does not carry it.
type Second struct {
	Name     string
	Type     string // "" for an assign2 or for _
	NameSpan Span
	TypeSpan Span
}

func (n *Node) MarshalJSON() ([]byte, error) {
	if n == nil {
		return []byte("null"), nil
	}
	var b bytes.Buffer
	n.appendJSON(&b)
	return b.Bytes(), nil
}

// appendJSON writes n and everything under it to b in one pass.
func (n *Node) appendJSON(b *bytes.Buffer) {
	b.WriteString(`{"id":`)
	writeJSONString(b, n.ID)
	b.WriteString(`,"op":`)
	writeJSONString(b, n.Op)
	if n.Name != "" {
		b.WriteString(`,"name":`)
		writeJSONString(b, n.Name)
	}
	if n.Type != "" {
		b.WriteString(`,"type":`)
		writeJSONString(b, n.Type)
	}
	if n.Two != nil {
		b.WriteString(`,"name2":`)
		writeJSONString(b, n.Two.Name)
		if n.Two.Type != "" {
			b.WriteString(`,"type2":`)
			writeJSONString(b, n.Two.Type)
		}
	}
	if n.Pkg != "" {
		b.WriteString(`,"pkg":`)
		writeJSONString(b, n.Pkg)
	}
	if n.Func != "" {
		b.WriteString(`,"func":`)
		writeJSONString(b, n.Func)
	}
	switch n.ValK {
	case 1:
		b.WriteString(`,"value":`)
		b.WriteString(strconv.FormatInt(n.Int, 10))
	case 2:
		b.WriteString(`,"value":`)
		if n.Bool {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case 3:
		if utf8.ValidString(n.Str) {
			b.WriteString(`,"value":`)
			writeJSONString(b, n.Str)
		} else {
			// Bytes JSON cannot carry: hex, so nothing is lost.
			b.WriteString(`,"value_hex":"` + hex.EncodeToString([]byte(n.Str)) + `"`)
		}
	}
	writeNode(b, "left", n.Left)
	writeNode(b, "right", n.Right)
	writeNode(b, "arg", n.Arg)
	writeNode(b, "base", n.Base)
	writeNode(b, "addr", n.Addr)
	writeNode(b, "val", n.Val)
	writeNode(b, "val2", n.Val2)
	writeNode(b, "cond", n.Cond)
	writeNodes(b, "args", n.Args)
	writeNodes(b, "then", n.Then)
	writeNodes(b, "else", n.Else)
	writeNodes(b, "body", n.Body)
	b.WriteByte('}')
}

func writeNode(b *bytes.Buffer, key string, n *Node) {
	if n == nil {
		return
	}
	b.WriteByte(',')
	b.WriteByte('"')
	b.WriteString(key)
	b.WriteString(`":`)
	n.appendJSON(b)
}

func writeNodes(b *bytes.Buffer, key string, ns []*Node) {
	if len(ns) == 0 {
		return
	}
	b.WriteByte(',')
	b.WriteByte('"')
	b.WriteString(key)
	b.WriteString(`":[`)
	for i, n := range ns {
		if i > 0 {
			b.WriteByte(',')
		}
		if n == nil {
			b.WriteString("null")
		} else {
			n.appendJSON(b)
		}
	}
	b.WriteByte(']')
}

func writeJSONString(b *bytes.Buffer, s string) {
	// Printable ASCII without a quote or a backslash is itself in JSON;
	// that is nearly every id, op, and name.
	plain := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			plain = false
			break
		}
	}
	if plain {
		b.WriteByte('"')
		b.WriteString(s)
		b.WriteByte('"')
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	b.Write(raw)
}

func (n *Node) Walk(fn func(*Node)) {
	if n == nil {
		return
	}
	fn(n)
	n.Left.Walk(fn)
	n.Right.Walk(fn)
	n.Arg.Walk(fn)
	n.Base.Walk(fn)
	n.Addr.Walk(fn)
	n.Val.Walk(fn)
	n.Val2.Walk(fn)
	n.Cond.Walk(fn)
	for _, c := range n.Args {
		c.Walk(fn)
	}
	for _, c := range n.Then {
		c.Walk(fn)
	}
	for _, c := range n.Else {
		c.Walk(fn)
	}
	for _, c := range n.Body {
		c.Walk(fn)
	}
}

func Marshal(p *Program) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Children returns the direct child nodes of n in source order.
func (n *Node) Children() []*Node {
	var out []*Node
	for _, c := range []*Node{n.Left, n.Right, n.Arg, n.Base, n.Addr, n.Val, n.Val2, n.Cond} {
		if c != nil {
			out = append(out, c)
		}
	}
	out = append(out, n.Args...)
	out = append(out, n.Then...)
	out = append(out, n.Else...)
	out = append(out, n.Body...)
	return out
}

// IsStmt reports whether op names a statement.
func IsStmt(op string) bool {
	switch op {
	case "var", "var2", "assign", "assign2", "setfield", "store8", "store16", "store32", "store64", "return", "if", "while", "expr":
		return true
	}
	return false
}
