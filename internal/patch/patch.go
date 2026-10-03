package patch

import (
	"bytes"
	"encoding/json"

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
	Ok              bool   `json:"ok"`
	Error           string `json:"error,omitempty"`
	Revision        string `json:"revision,omitempty"`
	BaseRevision    string `json:"baseRevision,omitempty"`
	CurrentRevision string `json:"currentRevision,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

func Apply(prog *ir.Program, current string, raw []byte) (*ir.Program, Response) {
	if err := ir.ValidateJSON(raw); err != nil {
		return nil, Response{Error: "bad_patch", Detail: err.Error()}
	}
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
			Revision:        current,
		}
	}
	if len(f.Ops) == 0 {
		return nil, Response{Ok: false, Error: "bad_patch", Detail: "no ops"}
	}
	encoded, err := ir.Marshal(prog)
	if err != nil {
		return nil, Response{Error: "bad_patch", Detail: err.Error()}
	}
	prog, err = ir.Unmarshal(encoded)
	if err != nil {
		return nil, Response{Error: "bad_patch", Detail: err.Error()}
	}
	for _, op := range f.Ops {
		if op.Op != "replace" {
			return nil, Response{Ok: false, Error: "bad_patch", Detail: "unsupported op"}
		}
		if err := prog.Replace(op.ID, op.Node); err != nil {
			return nil, Response{Ok: false, Error: "bad_patch", Detail: err.Error()}
		}
	}
	encoded, err = ir.Marshal(prog)
	if err == nil {
		err = ir.ValidateStructure(encoded)
	}
	if err != nil {
		return nil, Response{Error: "bad_patch", Detail: err.Error()}
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
