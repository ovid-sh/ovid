// Package ir is the Ovid program tree. The source of truth is .ov text; the
// parser builds this tree with an id and a source span on every node. The JSON
// form (ovid dump) is a derived view for tools and for the self-hosted CLI.
package ir

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Span is a byte range in one source file. File indexes Program.Files.
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
	// Toolchain is set by the loader for a package that came from the
	// toolchain's own standard library, not from the module or a std dir
	// that ovid.mod names. Only such an ovid/io may call syscall, so it is
	// never read from JSON.
	Toolchain bool `json:"-"`
}

type Import struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Span Span   `json:"-"`
}

type Const struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value int64  `json:"value"`
	Span  Span   `json:"-"`
}

type TypeDecl struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
	Span   Span    `json:"-"`
}

type Field struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Span Span   `json:"-"`
}

type Func struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Params []Param `json:"params,omitempty"`
	Result string  `json:"result"`
	Body   []*Node `json:"body,omitempty"`
	Span   Span    `json:"-"`
}

type Param struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Span Span   `json:"-"`
}

// Node is a statement or expression. ValK selects the JSON "value" payload:
// 1 int, 2 bool, 3 string.
type Node struct {
	ID    string
	Op    string
	Name  string
	Type  string
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
	Cond  *Node
	Args  []*Node
	Then  []*Node
	Else  []*Node
	Body  []*Node
	Span  Span
}

func (n *Node) MarshalJSON() ([]byte, error) {
	if n == nil {
		return []byte("null"), nil
	}
	var b bytes.Buffer
	b.WriteString(`{"id":`)
	writeJSONString(&b, n.ID)
	b.WriteString(`,"op":`)
	writeJSONString(&b, n.Op)
	if n.Name != "" {
		b.WriteString(`,"name":`)
		writeJSONString(&b, n.Name)
	}
	if n.Type != "" {
		b.WriteString(`,"type":`)
		writeJSONString(&b, n.Type)
	}
	if n.Pkg != "" {
		b.WriteString(`,"pkg":`)
		writeJSONString(&b, n.Pkg)
	}
	if n.Func != "" {
		b.WriteString(`,"func":`)
		writeJSONString(&b, n.Func)
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
		b.WriteString(`,"value":`)
		writeJSONString(&b, n.Str)
	}
	writeNode(&b, "left", n.Left)
	writeNode(&b, "right", n.Right)
	writeNode(&b, "arg", n.Arg)
	writeNode(&b, "base", n.Base)
	writeNode(&b, "addr", n.Addr)
	writeNode(&b, "val", n.Val)
	writeNode(&b, "cond", n.Cond)
	writeNodes(&b, "args", n.Args)
	writeNodes(&b, "then", n.Then)
	writeNodes(&b, "else", n.Else)
	writeNodes(&b, "body", n.Body)
	b.WriteByte('}')
	return b.Bytes(), nil
}

func writeNode(b *bytes.Buffer, key string, n *Node) {
	if n == nil {
		return
	}
	b.WriteByte(',')
	b.WriteByte('"')
	b.WriteString(key)
	b.WriteString(`":`)
	raw, _ := json.Marshal(n)
	b.Write(raw)
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
		raw, _ := json.Marshal(n)
		b.Write(raw)
	}
	b.WriteByte(']')
}

func writeJSONString(b *bytes.Buffer, s string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	b.Write(raw)
}

func (n *Node) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	var raw struct {
		ID    string          `json:"id"`
		Op    string          `json:"op"`
		Name  string          `json:"name"`
		Type  string          `json:"type"`
		Pkg   string          `json:"pkg"`
		Func  string          `json:"func"`
		Value json.RawMessage `json:"value"`
		Left  *Node           `json:"left"`
		Right *Node           `json:"right"`
		Arg   *Node           `json:"arg"`
		Base  *Node           `json:"base"`
		Addr  *Node           `json:"addr"`
		Val   *Node           `json:"val"`
		Cond  *Node           `json:"cond"`
		Args  []*Node         `json:"args"`
		Then  []*Node         `json:"then"`
		Else  []*Node         `json:"else"`
		Body  []*Node         `json:"body"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	n.ID = raw.ID
	n.Op = raw.Op
	n.Name = raw.Name
	n.Type = raw.Type
	n.Pkg = raw.Pkg
	n.Func = raw.Func
	n.Left = raw.Left
	n.Right = raw.Right
	n.Arg = raw.Arg
	n.Base = raw.Base
	n.Addr = raw.Addr
	n.Val = raw.Val
	n.Cond = raw.Cond
	n.Args = raw.Args
	n.Then = raw.Then
	n.Else = raw.Else
	n.Body = raw.Body
	if len(raw.Value) == 0 {
		return nil
	}
	switch raw.Value[0] {
	case '"':
		if err := json.Unmarshal(raw.Value, &n.Str); err != nil {
			return err
		}
		n.ValK = 3
	case 't', 'f':
		if err := json.Unmarshal(raw.Value, &n.Bool); err != nil {
			return err
		}
		n.ValK = 2
	default:
		if err := json.Unmarshal(raw.Value, &n.Int); err != nil {
			return err
		}
		n.ValK = 1
	}
	return nil
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

func Unmarshal(data []byte) (*Program, error) {
	var p Program
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Children returns the direct child nodes of n in source order.
func (n *Node) Children() []*Node {
	var out []*Node
	for _, c := range []*Node{n.Left, n.Right, n.Arg, n.Base, n.Addr, n.Val, n.Cond} {
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
	case "var", "assign", "setfield", "store8", "store64", "return", "if", "while", "expr":
		return true
	}
	return false
}
