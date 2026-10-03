package patch

import (
	"bytes"
	"encoding/json"
	"fmt"

	"ovid/internal/ir"
)

type Op struct {
	Op   string          `json:"op"`
	ID   string          `json:"id"`
	Node json.RawMessage `json:"node"`
}

type File struct {
	BaseRevision string `json:"baseRevision"`
	Ops          []Op   `json:"ops"`
}

type Response struct {
	Ok               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
	Revision         string `json:"revision,omitempty"`
	BaseRevision     string `json:"baseRevision,omitempty"`
	CurrentRevision  string `json:"currentRevision,omitempty"`
	Detail           string `json:"detail,omitempty"`
}

func Apply(prog *ir.Program, current string, raw []byte) (*ir.Program, Response) {
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, Response{Ok: false, Error: "bad_patch", Detail: err.Error()}
	}
	if f.BaseRevision == "" {
		return nil, Response{Ok: false, Error: "bad_patch", Detail: "missing baseRevision"}
	}
	if f.BaseRevision != current {
		return nil, Response{
			Ok:              false,
			Error:           "stale_patch",
			BaseRevision:    f.BaseRevision,
			CurrentRevision: current,
		}
	}
	if len(f.Ops) == 0 {
		return nil, Response{Ok: false, Error: "bad_patch", Detail: "no ops"}
	}
	for _, op := range f.Ops {
		if op.Op != "replace" {
			return nil, Response{Ok: false, Error: "bad_patch", Detail: "unsupported op " + op.Op}
		}
		if err := prog.Replace(op.ID, op.Node); err != nil {
			return nil, Response{Ok: false, Error: "bad_patch", Detail: fmt.Sprintf("%s: %s", op.ID, err.Error())}
		}
	}
	return prog, Response{Ok: true}
}

func Marshal(r Response) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(r)
	return buf.Bytes()
}
