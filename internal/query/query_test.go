package query

import (
	"testing"

	"ovid/internal/ir"
)

func fixture() *ir.Program {
	return &ir.Program{Packages: []ir.Package{
		{ID: "pkg-a", Path: "a", Funcs: []ir.Func{
			{ID: "callee-stable-id", Name: "Write", Result: "i64"},
			{ID: "caller-stable-id", Name: "Save", Result: "i64", Params: []ir.Param{{ID: "param-path", Name: "path", Type: "i64"}}, Body: []*ir.Node{
				{ID: "stmt-save", Op: "return", Val: &ir.Node{ID: "local-call", Op: "call", Func: "Write"}},
				{ID: "stmt-other", Op: "expr", Val: &ir.Node{ID: "external-call", Op: "call", Pkg: "b", Func: "Write"}},
			}},
		}},
		{ID: "pkg-b", Path: "b", Funcs: []ir.Func{
			{ID: "other-callee", Name: "Write", Result: "i64", Body: []*ir.Node{
				{ID: "remote-stmt", Op: "return", Val: &ir.Node{ID: "remote-call", Op: "call", Pkg: "a", Func: "Write"}},
			}},
		}},
	}}
}

func TestDiscoveryIsCompactAndScoped(t *testing.T) {
	opts := DefaultOptions()
	opts.Pkg = "a"
	r := RunOptions(fixture(), "rev", opts)
	if r.Total != 3 || r.Returned != 3 || r.HasMore {
		t.Fatalf("bad metadata: %+v", r)
	}
	for _, m := range r.Matches {
		if len(m.Node) != 0 {
			t.Fatalf("discovery expanded %s", m.ID)
		}
		if m.Kind != "package" && m.Kind != "func" {
			t.Fatalf("unexpected nested match: %+v", m)
		}
	}
	opts.Full = true
	r = RunOptions(fixture(), "rev", opts)
	if r.Total != 3 || len(r.Matches[0].Node) == 0 {
		t.Fatalf("full must expand declarations without changing selection: %+v", r)
	}
}

func TestExactIDIncludesPayloadAndOwner(t *testing.T) {
	r := Run(fixture(), "rev", "local-call", "", "", "")
	if r.Total != 1 {
		t.Fatalf("got %d matches", r.Total)
	}
	m := r.Matches[0]
	if m.Func != "Save" || m.FuncID != "caller-stable-id" || m.Op != "call" || len(m.Node) == 0 {
		t.Fatalf("missing node or owner: %+v", m)
	}
	r = Run(fixture(), "rev", "", "path", "a", "")
	if r.Total != 1 || r.Matches[0].Kind != "param" || r.Matches[0].FuncID != "caller-stable-id" || len(r.Matches[0].Node) != 0 {
		t.Fatalf("name discovery failed: %+v", r)
	}
}

func TestPaginationHasExactTotalAndNoGaps(t *testing.T) {
	p := fixture()
	opts := DefaultOptions()
	opts.Limit = 2
	var ids []string
	for {
		r := RunOptions(p, "rev", opts)
		if r.Total != 5 || r.Offset != opts.Offset || r.Limit != 2 || r.Returned != len(r.Matches) {
			t.Fatalf("metadata: %+v", r)
		}
		for _, m := range r.Matches {
			ids = append(ids, m.ID)
		}
		if !r.HasMore {
			if r.NextOffset != nil {
				t.Fatal("unexpected next page")
			}
			break
		}
		if r.NextOffset == nil || *r.NextOffset != opts.Offset+len(r.Matches) {
			t.Fatalf("bad next page: %+v", r)
		}
		opts.Offset = *r.NextOffset
	}
	want := []string{"pkg-a", "callee-stable-id", "caller-stable-id", "pkg-b", "other-callee"}
	if len(ids) != len(want) {
		t.Fatalf("ids: %v", ids)
	}
	for i := range ids {
		if ids[i] != want[i] {
			t.Fatalf("ids: %v", ids)
		}
	}
	opts.Offset = 999
	r := RunOptions(p, "rev", opts)
	if r.Returned != 0 || r.Total != 5 || r.HasMore || r.Matches == nil {
		t.Fatalf("past end: %+v", r)
	}
	opts.Offset, opts.Limit = 0, 0
	if r = RunOptions(p, "rev", opts); r.Returned != 5 || r.HasMore {
		t.Fatalf("unlimited: %+v", r)
	}
}

func TestCallsToResolvesLocalAndQualifiedCallsByDefinitionID(t *testing.T) {
	opts := DefaultOptions()
	opts.CallsTo = "callee-stable-id"
	r := RunOptions(fixture(), "rev", opts)
	if r.Total != 2 || r.Matches[0].ID != "local-call" || r.Matches[1].ID != "remote-call" {
		t.Fatalf("wrong callers: %+v", r)
	}
	if r.Matches[1].FuncID != "other-callee" || len(r.Matches[1].Node) != 0 {
		t.Fatalf("wrong owner or payload: %+v", r.Matches[1])
	}
	opts.Pkg = "b"
	if r = RunOptions(fixture(), "rev", opts); r.Total != 1 || r.Matches[0].ID != "remote-call" {
		t.Fatalf("package selection: %+v", r)
	}
	opts.CallsTo = "missing"
	if r = RunOptions(fixture(), "rev", opts); r.Total != 0 {
		t.Fatalf("unknown target: %+v", r)
	}
}
