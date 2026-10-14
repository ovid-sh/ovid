package ir

import "testing"

// TestWalkSecondResult: Walk and Children both reach the error code of a
// return, so its nodes are claimed, indexed, and shown like the value's.
func TestWalkSecondResult(t *testing.T) {
	ret := &Node{ID: "st:1", Op: "return",
		Val:  &Node{ID: "ex:1", Op: "name", Name: "a"},
		Val2: &Node{ID: "ex:3", Op: "call", Func: "G", Args: []*Node{{ID: "ex:2", Op: "name", Name: "b"}}},
	}
	var walked []string
	ret.Walk(func(n *Node) { walked = append(walked, n.ID) })
	if got := len(walked); got != 4 || walked[2] != "ex:3" || walked[3] != "ex:2" {
		t.Fatalf("Walk visits %v", walked)
	}
	if cs := ret.Children(); len(cs) != 2 || cs[1].ID != "ex:3" {
		t.Fatalf("Children gives %d nodes", len(cs))
	}
}
