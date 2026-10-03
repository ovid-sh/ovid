package patch

import (
	"bytes"
	"encoding/json"
	"testing"

	"ovid/internal/ir"
)

func fixture(t *testing.T) *ir.Program {
	t.Helper()
	p, err := ir.Unmarshal([]byte(`{"revision":"revision","module":"demo","entry":"demo","packages":[{"id":"package","path":"demo","imports":[{"id":"import","path":"io"}],"consts":[{"id":"constant","name":"N","type":"i64","value":1}],"types":[{"id":"type","name":"T","fields":[{"id":"field","name":"x","type":"i64"}]}],"funcs":[{"id":"function","name":"main","params":[{"id":"parameter","name":"arg","type":"i64"}],"result":"i64","body":[{"id":"statement","op":"return","val":{"id":"expression","op":"int","value":0}}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func apply(t *testing.T, p *ir.Program, ops ...Op) (*ir.Program, Response) {
	t.Helper()
	raw, err := json.Marshal(File{BaseRevision: "revision", Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	return Apply(p, "revision", raw)
}

func TestReplaceEveryAddressableKind(t *testing.T) {
	cases := map[string]string{
		"package":    `{"id":"package","path":"changed"}`,
		"import":     `{"id":"import","path":"changed"}`,
		"constant":   `{"id":"constant","name":"N","type":"i64","value":2}`,
		"type":       `{"id":"type","name":"Changed","fields":[]}`,
		"field":      `{"id":"field","name":"changed","type":"bool"}`,
		"function":   `{"id":"function","name":"changed","result":"i64"}`,
		"parameter":  `{"id":"parameter","name":"changed","type":"bool"}`,
		"statement":  `{"id":"statement","op":"return","val":{"id":"new_expression","op":"int","value":42}}`,
		"expression": `{"id":"expression","op":"bool","value":true}`,
	}
	for id, raw := range cases {
		t.Run(id, func(t *testing.T) {
			p := fixture(t)
			before, _ := ir.Marshal(p)
			updated, resp := apply(t, p, Op{Op: "replace", ID: id, Node: json.RawMessage(raw)})
			if !resp.Ok || updated == nil {
				t.Fatalf("replace: %+v", resp)
			}
			after, _ := ir.Marshal(p)
			if !bytes.Equal(before, after) {
				t.Fatal("Apply mutated input program")
			}
		})
	}
}

func TestFailedBatchLeavesInputUntouched(t *testing.T) {
	p := fixture(t)
	before, _ := ir.Marshal(p)
	_, resp := apply(t, p,
		Op{Op: "replace", ID: "expression", Node: json.RawMessage(`{"id":"expression","op":"int","value":42}`)},
		Op{Op: "replace", ID: "absent", Node: json.RawMessage(`{"id":"absent","op":"int","value":1}`)},
	)
	after, _ := ir.Marshal(p)
	if resp.Ok || !bytes.Equal(before, after) {
		t.Fatalf("failed batch mutated input: %+v", resp)
	}
}

func TestRejectAmbiguousAndWrongShapeReplacements(t *testing.T) {
	for _, raw := range []string{
		`{"id":"function","op":"int","value":1}`,
		`{"id":"function","name":"f","result":"i64","body":[{"op":"return"}]}`,
		`{"id":"function","name":"f","result":"i64","body":[{"id":"constant","op":"return"}]}`,
		`{"id":"function","name":"f","result":"i64","body":[null]}`,
		`{"id":"function","id":"function","name":"f","result":"i64"}`,
	} {
		_, resp := apply(t, fixture(t), Op{Op: "replace", ID: "function", Node: json.RawMessage(raw)})
		if resp.Ok {
			t.Fatalf("accepted invalid replacement: %s", raw)
		}
	}
}

func TestTypeBrokenDraftRemainsPatchable(t *testing.T) {
	updated, resp := apply(t, fixture(t), Op{Op: "replace", ID: "expression", Node: json.RawMessage(`{"id":"expression","op":"bool","value":true}`)})
	if !resp.Ok {
		t.Fatalf("type-broken draft rejected: %+v", resp)
	}
	_, resp = apply(t, updated, Op{Op: "replace", ID: "expression", Node: json.RawMessage(`{"id":"expression","op":"int","value":7}`)})
	if !resp.Ok {
		t.Fatalf("draft cannot be repaired: %+v", resp)
	}
}
