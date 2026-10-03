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
	"io"
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
	if err := uniqueJSONKeys(data); err != nil {
		return nil, err
	}
	var p Program
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return &p, nil
}

// ValidateJSON rejects malformed JSON, duplicate object keys and trailing input.
func ValidateJSON(data []byte) error { return uniqueJSONKeys(data) }

// uniqueJSONKeys prevents ambiguous programs from being interpreted differently
// by clients whose JSON decoders disagree about duplicate object members.
func uniqueJSONKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func() error
	value = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				k, ok := key.(string)
				if !ok {
					return fmt.Errorf("invalid object key")
				}
				if seen[k] {
					return fmt.Errorf("duplicate JSON key: %s", k)
				}
				seen[k] = true
				if err := value(); err != nil {
					return err
				}
			}
		} else if delim == '[' {
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

// SplitRevision finds the stored revision and returns a copy of the file with
// that 64-digit field set to zeros.
func SplitRevision(file []byte) (stored string, zeroed []byte, err error) {
	i := bytes.Index(file, []byte(revKey))
	if i < 0 {
		return "", nil, fmt.Errorf("missing revision field")
	}
	start := i + len(revKey)
	if start+64 >= len(file) {
		return "", nil, fmt.Errorf("truncated revision")
	}
	if file[start+64] != '"' {
		return "", nil, fmt.Errorf("revision is not 64 hex digits")
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

// Replace accepts every addressable object while preserving its identity and
// structural kind. Type errors are allowed: a draft must remain repairable.
func (p *Program) Replace(id string, raw []byte) error {
	if id == "" {
		return fmt.Errorf("missing_id")
	}
	if err := uniqueJSONKeys(raw); err != nil {
		return err
	}
	var replacement map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&replacement); err != nil {
		return err
	}
	if replacement["id"] != id {
		return fmt.Errorf("id_mismatch")
	}
	encoded, err := Marshal(p)
	if err != nil {
		return err
	}
	var root map[string]any
	d = json.NewDecoder(bytes.NewReader(encoded))
	d.UseNumber()
	if err := d.Decode(&root); err != nil {
		return err
	}
	found := false
	err = walkStructure(root, "program", func(obj map[string]any, kind string) error {
		if obj["id"] != id {
			return nil
		}
		if err := walkStructure(replacement, kind, func(map[string]any, string) error { return nil }); err != nil {
			return err
		}
		for k := range obj {
			delete(obj, k)
		}
		for k, v := range replacement {
			obj[k] = v
		}
		found = true
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("not_found")
	}
	encoded, err = json.Marshal(root)
	if err != nil {
		return err
	}
	updated, err := Unmarshal(encoded)
	if err != nil {
		return err
	}
	*p = *updated
	return nil
}

// ValidateStructure checks identities and JSON shape without typechecking.
func ValidateStructure(raw []byte) error {
	if err := uniqueJSONKeys(raw); err != nil {
		return err
	}
	var root map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&root); err != nil {
		return err
	}
	seen := map[string]bool{}
	return walkStructure(root, "program", func(obj map[string]any, kind string) error {
		if kind == "program" {
			return nil
		}
		id, ok := obj["id"].(string)
		if !ok || id == "" {
			return fmt.Errorf("missing_id")
		}
		if seen[id] {
			return fmt.Errorf("duplicate_id")
		}
		seen[id] = true
		return nil
	})
}

func walkStructure(obj map[string]any, kind string, visit func(map[string]any, string) error) error {
	if obj == nil {
		return fmt.Errorf("invalid_node")
	}
	allowed := map[string]bool{"id": true}
	var required []string
	lists := map[string]string{}
	singles := map[string]string{}
	switch kind {
	case "program":
		allowed = map[string]bool{"revision": true, "module": true, "entry": true, "packages": true}
		required = []string{"revision", "module", "entry"}
		lists["packages"] = "package"
	case "package":
		required = []string{"path"}
		lists = map[string]string{"imports": "import", "consts": "const", "types": "type", "funcs": "func"}
	case "import":
		required = []string{"path"}
	case "const":
		required = []string{"name", "type"}
		allowed["value"] = true
		if _, ok := obj["value"].(json.Number); !ok {
			return fmt.Errorf("invalid_node")
		}
	case "type":
		required = []string{"name"}
		lists["fields"] = "field"
	case "field", "param":
		required = []string{"name", "type"}
	case "func":
		required = []string{"name", "result"}
		lists = map[string]string{"params": "param", "body": "node"}
	case "node":
		required = []string{"op"}
		for _, k := range []string{"name", "type", "pkg", "func", "value"} {
			allowed[k] = true
		}
		for _, k := range []string{"left", "right", "arg", "base", "addr", "val", "cond"} {
			singles[k] = "node"
		}
		for _, k := range []string{"args", "then", "else", "body"} {
			lists[k] = "node"
		}
	default:
		return fmt.Errorf("invalid_node")
	}
	for _, k := range required {
		allowed[k] = true
		if _, ok := obj[k].(string); !ok {
			return fmt.Errorf("invalid_node")
		}
	}
	for k := range lists {
		allowed[k] = true
	}
	for k := range singles {
		allowed[k] = true
	}
	for k, v := range obj {
		if !allowed[k] {
			return fmt.Errorf("invalid_node")
		}
		if _, ok := lists[k]; ok {
			continue
		}
		if _, ok := singles[k]; ok {
			continue
		}
		if k == "value" {
			switch v.(type) {
			case json.Number, bool, string:
			default:
				return fmt.Errorf("invalid_node")
			}
		} else if _, ok := v.(string); !ok {
			return fmt.Errorf("invalid_node")
		}
	}
	if err := visit(obj, kind); err != nil {
		return err
	}
	for k, childKind := range lists {
		v, present := obj[k]
		if !present {
			continue
		}
		if v == nil {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("invalid_node")
		}
		for _, child := range list {
			c, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid_node")
			}
			if err := walkStructure(c, childKind, visit); err != nil {
				return err
			}
		}
	}
	for k, childKind := range singles {
		v, present := obj[k]
		if !present || v == nil {
			continue
		}
		c, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid_node")
		}
		if err := walkStructure(c, childKind, visit); err != nil {
			return err
		}
	}
	return nil
}
