package ov

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"ovid/internal/ir"
)

// PreserveIDs reconciles bootstrap source reimports with a previous canonical
// program. Declarations match by package path and name; an unambiguous function
// rename can also match its otherwise unchanged structure. Within a function,
// unchanged subtrees survive insertion/reordering, and a changed scalar in an
// unambiguous role keeps its identity. Ambiguous edits receive new identities.
//
// This is best-effort source reconciliation, not an identity guarantee for
// arbitrary text edits. Canonical patches provide exact identity preservation.
// Fresh objects receive opaque random IDs; parser positions are never recycled
// as identities. previous is not mutated. On error, next is not mutated either.
func PreserveIDs(next, previous *ir.Program) error {
	if next == nil {
		return fmt.Errorf("cannot preserve IDs of a nil program")
	}
	neu, err := identityDocument(next)
	if err != nil {
		return err
	}
	var old any
	if previous != nil {
		old, err = identityDocument(previous)
		if err != nil {
			return err
		}
	}
	state := identityReconciler{oldCounts: map[string]int{}, used: map[string]bool{}, claimed: map[string]bool{}}
	visitIdentityObjects(old, func(obj map[string]any) {
		if id, ok := obj["id"].(string); ok && id != "" {
			state.oldCounts[id]++
			state.used[id] = true
		}
	})
	var entropyErr error
	visitIdentityObjects(neu, func(obj map[string]any) {
		id, ok := obj["id"].(string)
		if !ok || entropyErr != nil {
			return
		}
		prefix, _, _ := strings.Cut(id, ":")
		if prefix == "" {
			prefix = "node"
		}
		for {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				entropyErr = fmt.Errorf("allocate node identity: %w", err)
				return
			}
			fresh := prefix + ":" + hex.EncodeToString(random[:])
			if !state.used[fresh] {
				state.used[fresh] = true
				obj["id"] = fresh
				return
			}
		}
	})
	if entropyErr != nil {
		return entropyErr
	}
	state.reconcile(neu, old, "")
	raw, err := json.Marshal(neu)
	if err != nil {
		return err
	}
	result, err := ir.Unmarshal(raw)
	if err != nil {
		return err
	}
	*next = *result
	return nil
}

func identityDocument(program *ir.Program) (any, error) {
	raw, err := json.Marshal(program)
	if err != nil {
		return nil, err
	}
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // IDs must not change large i64 constants through float64.
	err = decoder.Decode(&doc)
	return doc, err
}

func visitIdentityObjects(value any, visit func(map[string]any)) {
	switch value := value.(type) {
	case map[string]any:
		visit(value)
		for _, child := range value {
			visitIdentityObjects(child, visit)
		}
	case []any:
		for _, child := range value {
			visitIdentityObjects(child, visit)
		}
	}
}

type identityReconciler struct {
	oldCounts map[string]int
	used      map[string]bool
	claimed   map[string]bool
}

func (r *identityReconciler) reconcile(neu, old any, role string) {
	switch neu := neu.(type) {
	case map[string]any:
		prior, ok := old.(map[string]any)
		if !ok || !compatibleIdentityRole(neu, prior) {
			return
		}
		if id, ok := prior["id"].(string); ok && r.oldCounts[id] == 1 && !r.claimed[id] {
			neu["id"] = id
			r.claimed[id] = true
		}
		for key, child := range neu {
			if key != "id" {
				r.reconcile(child, prior[key], key)
			}
		}
	case []any:
		prior, ok := old.([]any)
		if ok {
			r.reconcileList(neu, prior, role)
		}
	}
}

func compatibleIdentityRole(neu, old map[string]any) bool {
	nextOp, nextNode := neu["op"].(string)
	oldOp, oldNode := old["op"].(string)
	if !nextNode && !oldNode {
		return true // Declaration pairs were selected by their parent list.
	}
	if nextOp == oldOp {
		return true
	}
	// A literal edited in the same expression slot remains that expression,
	// including a bool/i64 correction prompted by the type checker.
	return (nextOp == "int" || nextOp == "bool") && (oldOp == "int" || oldOp == "bool")
}

func (r *identityReconciler) reconcileList(neu, old []any, role string) {
	if identityShape(neu, false, false) == identityShape(old, false, false) {
		// No observable edit: retain even repeated identical sibling statements.
		for i := range neu {
			r.reconcile(neu[i], old[i], role)
		}
		return
	}
	usedNext, usedOld := make([]bool, len(neu)), make([]bool, len(old))
	match := func(key func(any) string) {
		nextKeys, oldKeys := map[string][]int{}, map[string][]int{}
		for i, value := range neu {
			if !usedNext[i] {
				if k := key(value); k != "" {
					nextKeys[k] = append(nextKeys[k], i)
				}
			}
		}
		for i, value := range old {
			if !usedOld[i] {
				if k := key(value); k != "" {
					oldKeys[k] = append(oldKeys[k], i)
				}
			}
		}
		for key, ns := range nextKeys {
			os := oldKeys[key]
			if len(ns) == 1 && len(os) == 1 {
				i, j := ns[0], os[0]
				usedNext[i], usedOld[j] = true, true
				r.reconcile(neu[i], old[j], role)
			}
		}
	}
	switch role {
	case "packages", "imports":
		match(identityProperty("path"))
	case "types", "consts", "fields", "params":
		match(identityProperty("name"))
	case "funcs":
		match(identityProperty("name"))
		match(func(value any) string { return identityShape(value, true, false) })
	default:
		// Exact unique subtrees anchor insertion and reordering. If structure is
		// unchanged except literal values, that is also an unambiguous edit.
		match(func(value any) string { return identityShape(value, false, false) })
		match(func(value any) string { return identityShape(value, false, true) })
		// Finally retain a sole statement in a named role (e.g. a return) even
		// if its expression changed. Multiple candidates are deliberately left
		// unmatched rather than being assigned identities by source position.
		match(identityAnchor)
	}
}

func identityProperty(key string) func(any) string {
	return func(value any) string {
		obj, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		text, _ := obj[key].(string)
		return text
	}
}

func identityAnchor(value any) string {
	obj, ok := value.(map[string]any)
	if !ok || obj["op"] == nil {
		return ""
	}
	anchor := map[string]any{}
	for _, key := range []string{"op", "name", "type", "pkg", "func"} {
		if value, exists := obj[key]; exists {
			anchor[key] = value
		}
	}
	raw, _ := json.Marshal(anchor)
	return string(raw)
}

// ignoreName applies only to the root object (a renamed function). Literal
// values can be excluded when comparing otherwise unchanged AST structure.
func identityShape(value any, ignoreName, ignoreLiterals bool) string {
	var strip func(any, bool) any
	strip = func(value any, root bool) any {
		switch value := value.(type) {
		case map[string]any:
			result := make(map[string]any, len(value))
			for key, child := range value {
				if key == "id" || (root && ignoreName && key == "name") || (ignoreLiterals && key == "value" && value["op"] != nil) {
					continue
				}
				result[key] = strip(child, false)
			}
			return result
		case []any:
			result := make([]any, len(value))
			for i, child := range value {
				result[i] = strip(child, false)
			}
			return result
		default:
			return value
		}
	}
	raw, _ := json.Marshal(strip(value, true))
	return string(raw)
}
