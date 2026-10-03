// Package ir is the Ovid program database. A program is one JSON document
// (ovid.json). Agents address nodes by stable id. The revision is the SHA-256
// of the file with the 64-digit revision value replaced by zeros.
package ir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

const RevZeros = "0000000000000000000000000000000000000000000000000000000000000000"

const revKey = `"revision": "`

type Program struct {
	Revision string    `json:"revision"`
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
}

type Import struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Const struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value int64  `json:"value"`
}

type TypeDecl struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

type Field struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type Func struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Params []Param `json:"params,omitempty"`
	Result string  `json:"result"`
	Body   []*Node `json:"body,omitempty"`
}

type Param struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
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

// SplitRevision finds the stored revision and returns a copy of the file with
// that 64-digit field set to zeros.
func SplitRevision(file []byte) (stored string, zeroed []byte, err error) {
	i := bytes.Index(file, []byte(revKey))
	if i < 0 {
		return "", nil, fmt.Errorf("missing revision field")
	}
	start := i + len(revKey)
	if start+64 > len(file) {
		return "", nil, fmt.Errorf("truncated revision")
	}
	hexpart := file[start : start+64]
	for _, c := range hexpart {
		if !isHex(c) {
			return "", nil, fmt.Errorf("revision is not 64 hex digits")
		}
	}
	zeroed = append([]byte(nil), file...)
	for j := 0; j < 64; j++ {
		zeroed[start+j] = '0'
	}
	return string(hexpart), zeroed, nil
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// Hash returns the revision of a file that already contains a 64-digit revision.
func Hash(file []byte) (stored, computed string, err error) {
	stored, zeroed, err := SplitRevision(file)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(zeroed)
	return stored, hex.EncodeToString(sum[:]), nil
}

// Stamp replaces a 64-zero revision with the hash of the zeroed file.
func Stamp(file []byte) ([]byte, error) {
	stored, zeroed, err := SplitRevision(file)
	if err != nil {
		return nil, err
	}
	if stored != RevZeros {
		// Still recompute from the zeroed image so callers can pass either.
		_ = stored
	}
	sum := sha256.Sum256(zeroed)
	h := hex.EncodeToString(sum[:])
	out := append([]byte(nil), zeroed...)
	i := bytes.Index(out, []byte(revKey))
	start := i + len(revKey)
	copy(out[start:start+64], h)
	return out, nil
}

func ReadFile(dir string) ([]byte, *Program, error) {
	path := dir + "/ovid.json"
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	p, err := Unmarshal(b)
	if err != nil {
		return b, nil, err
	}
	return b, p, nil
}

// Replace swaps the node or function with this id. The new object's id must match.
func (p *Program) Replace(id string, raw []byte) error {
	for pi := range p.Packages {
		pkg := &p.Packages[pi]
		for fi := range pkg.Funcs {
			if pkg.Funcs[fi].ID == id {
				var nf Func
				if err := json.Unmarshal(raw, &nf); err != nil {
					return err
				}
				if nf.ID != id {
					return fmt.Errorf("id_mismatch")
				}
				pkg.Funcs[fi] = nf
				return nil
			}
		}
	}
	var nn Node
	if err := json.Unmarshal(raw, &nn); err != nil {
		return err
	}
	if nn.ID != id {
		return fmt.Errorf("id_mismatch")
	}
	if p.replaceNode(id, &nn) {
		return nil
	}
	return fmt.Errorf("not_found")
}

func (p *Program) replaceNode(id string, neu *Node) bool {
	for pi := range p.Packages {
		for fi := range p.Packages[pi].Funcs {
			body := p.Packages[pi].Funcs[fi].Body
			if replaceList(body, id, neu) {
				return true
			}
			for _, st := range body {
				if st.replaceChildren(id, neu) {
					return true
				}
			}
		}
	}
	return false
}

func replaceList(list []*Node, id string, neu *Node) bool {
	for i, n := range list {
		if n != nil && n.ID == id {
			list[i] = neu
			return true
		}
	}
	return false
}

func (n *Node) replaceChildren(id string, neu *Node) bool {
	if n == nil {
		return false
	}
	if replacePtr(&n.Left, id, neu) || replacePtr(&n.Right, id, neu) || replacePtr(&n.Arg, id, neu) ||
		replacePtr(&n.Base, id, neu) || replacePtr(&n.Addr, id, neu) || replacePtr(&n.Val, id, neu) ||
		replacePtr(&n.Cond, id, neu) {
		return true
	}
	if replaceList(n.Args, id, neu) || replaceList(n.Then, id, neu) || replaceList(n.Else, id, neu) || replaceList(n.Body, id, neu) {
		return true
	}
	kids := []*Node{n.Left, n.Right, n.Arg, n.Base, n.Addr, n.Val, n.Cond}
	kids = append(kids, n.Args...)
	kids = append(kids, n.Then...)
	kids = append(kids, n.Else...)
	kids = append(kids, n.Body...)
	for _, k := range kids {
		if k.replaceChildren(id, neu) {
			return true
		}
	}
	return false
}

func replacePtr(slot **Node, id string, neu *Node) bool {
	if slot == nil || *slot == nil {
		return false
	}
	if (*slot).ID == id {
		*slot = neu
		return true
	}
	return false
}
