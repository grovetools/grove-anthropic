package ccsettings

// This file implements a NON-DESTRUCTIVE settings.json writer.
//
// The hard requirement (a plan-review blocker) is that editing one value must
// preserve everything else byte-for-byte: key order, hand-authored comments,
// and any unknown keys the typed model does not recognize. The naive approach —
// decode into map[string]any, mutate, json.MarshalIndent — randomizes key order
// and strips all comments, destroying hand-authored structure. (See
// hooks/commands/install.go:mergeHooks for exactly the round-trip we avoid.)
//
// Instead we parse the file into a JWCC (JSON-with-commas-and-comments) AST via
// github.com/tailscale/hujson, mutate the targeted node in place, and re-pack.
// Comments, key order, and untouched subtrees survive verbatim; only the edited
// path changes.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/tailscale/hujson"
)

// EditOp is the kind of mutation an Edit performs at its target path.
type EditOp int

const (
	// OpSet sets (or creates) the scalar/value at Path to StringVal, BoolVal,
	// or IntVal depending on ValueKind, replacing any existing value.
	OpSet EditOp = iota
	// OpUnset removes the key at Path entirely. A no-op when it is absent.
	OpUnset
	// OpArrayAppend appends StringVal to the (string) array at Path, creating
	// the array (and intermediate objects) if needed. A no-op when the exact
	// element already exists.
	OpArrayAppend
	// OpArrayRemove removes the first element equal to StringVal from the array
	// at Path. A no-op when it is absent. An emptied array is left in place.
	OpArrayRemove
)

// ValueKind disambiguates the scalar type for OpSet.
type ValueKind int

const (
	KindString ValueKind = iota
	KindBool
	KindInt
)

// Edit is a single mutation against a settings document. Path is the sequence
// of object keys from the document root to the target (e.g.
// {"permissions","allow"}); array indices are never part of Path — array
// membership is addressed by value via OpArrayAppend/OpArrayRemove.
type Edit struct {
	Op   EditOp
	Path []string

	ValueKind ValueKind
	StringVal string
	BoolVal   bool
	IntVal    int
}

// ApplyEdits applies edits to the JWCC source and returns the re-packed
// document. The input may be empty (treated as an empty object) or contain
// comments and trailing commas; output preserves all comments, key order, and
// unknown keys, mutating only the targeted paths.
func ApplyEdits(src []byte, edits []Edit) ([]byte, error) {
	root, err := parseOrEmptyObject(src)
	if err != nil {
		return nil, err
	}
	for i, e := range edits {
		if len(e.Path) == 0 {
			return nil, fmt.Errorf("edit %d: empty path", i)
		}
		if err := applyEdit(&root, e); err != nil {
			return nil, fmt.Errorf("edit %d (%v): %w", i, e.Path, err)
		}
	}
	// Format re-flows only the whitespace it must; untouched comments and
	// member order are preserved. Pack serializes the AST.
	root.Format()
	out := root.Pack()
	return out, nil
}

// PreviewEdits returns the file contents that ApplyEdits would write, without
// touching disk — the dry-run rendering shown before a confirm.
func PreviewEdits(path string, edits []Edit) ([]byte, error) {
	src, err := readIfExists(path)
	if err != nil {
		return nil, err
	}
	return ApplyEdits(src, edits)
}

// WriteEdits applies edits to the file at path and writes the result back,
// creating parent directories as needed. The write is atomic (temp file +
// rename) so a crash mid-write never leaves a truncated settings file.
func WriteEdits(path string, edits []Edit) error {
	src, err := readIfExists(path)
	if err != nil {
		return err
	}
	out, err := ApplyEdits(src, edits)
	if err != nil {
		return err
	}
	return atomicWrite(path, out)
}

func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless if the rename already moved it
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// parseOrEmptyObject parses src as JWCC, or returns a fresh empty object when
// src is empty/whitespace-only so edits to a non-existent file still work.
func parseOrEmptyObject(src []byte) (hujson.Value, error) {
	if len(trimSpace(src)) == 0 {
		return hujson.Value{Value: &hujson.Object{}}, nil
	}
	v, err := hujson.Parse(src)
	if err != nil {
		return hujson.Value{}, fmt.Errorf("parse settings: %w", err)
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return hujson.Value{}, fmt.Errorf("settings root is not a JSON object")
	}
	return v, nil
}

func applyEdit(root *hujson.Value, e Edit) error {
	switch e.Op {
	case OpSet:
		target, err := navigateCreate(root, e.Path)
		if err != nil {
			return err
		}
		target.Value = scalarLiteral(e)
		// A freshly created member must not carry a stale trailing comma.
		target.BeforeExtra = nil
		return nil
	case OpUnset:
		removeMember(root, e.Path)
		return nil
	case OpArrayAppend:
		arr, err := navigateCreateArray(root, e.Path)
		if err != nil {
			return err
		}
		if arrayContains(arr, e.StringVal) {
			return nil
		}
		arr.Elements = append(arr.Elements, hujson.Value{Value: hujson.String(e.StringVal)})
		return nil
	case OpArrayRemove:
		arr := navigateArray(root, e.Path)
		if arr == nil {
			return nil
		}
		removeArrayElement(arr, e.StringVal)
		return nil
	default:
		return fmt.Errorf("unknown edit op %d", int(e.Op))
	}
}

func scalarLiteral(e Edit) hujson.ValueTrimmed {
	switch e.ValueKind {
	case KindBool:
		return hujson.Bool(e.BoolVal)
	case KindInt:
		return hujson.Int(int64(e.IntVal))
	default:
		return hujson.String(e.StringVal)
	}
}

// navigateCreate descends path, creating intermediate objects as needed, and
// returns the *Value slot of the final key (creating the member if absent).
func navigateCreate(root *hujson.Value, path []string) (*hujson.Value, error) {
	cur := root
	for i, key := range path {
		obj, ok := cur.Value.(*hujson.Object)
		if !ok {
			return nil, fmt.Errorf("path element %q: parent is not an object", key)
		}
		slot := objectMember(obj, key)
		if slot == nil {
			// Create the member. Intermediate keys become empty objects; the
			// final key gets a placeholder the caller overwrites.
			var val hujson.ValueTrimmed
			if i == len(path)-1 {
				val = hujson.Literal("null")
			} else {
				val = &hujson.Object{}
			}
			obj.Members = append(obj.Members, hujson.ObjectMember{
				Name:  hujson.Value{Value: hujson.String(key)},
				Value: hujson.Value{Value: val},
			})
			slot = &obj.Members[len(obj.Members)-1].Value
		}
		cur = slot
	}
	return cur, nil
}

// navigateCreateArray is navigateCreate plus a guarantee the final slot holds
// an *Array (created if the key is absent; an error if it holds a non-array).
func navigateCreateArray(root *hujson.Value, path []string) (*hujson.Array, error) {
	slot, err := navigateCreate(root, path)
	if err != nil {
		return nil, err
	}
	switch v := slot.Value.(type) {
	case *hujson.Array:
		return v, nil
	case hujson.Literal:
		// Freshly created (null) or an empty placeholder — install an array.
		if v.Kind() == 'n' {
			arr := &hujson.Array{}
			slot.Value = arr
			return arr, nil
		}
		return nil, fmt.Errorf("target at %v is a %c literal, not an array", path, v.Kind())
	default:
		return nil, fmt.Errorf("target at %v is not an array", path)
	}
}

// navigateArray returns the *Array at path, or nil if any segment is missing or
// the target is not an array.
func navigateArray(root *hujson.Value, path []string) *hujson.Array {
	cur := root
	for _, key := range path {
		obj, ok := cur.Value.(*hujson.Object)
		if !ok {
			return nil
		}
		slot := objectMember(obj, key)
		if slot == nil {
			return nil
		}
		cur = slot
	}
	arr, _ := cur.Value.(*hujson.Array)
	return arr
}

// objectMember returns the value slot for key, or nil. The first member wins
// when an object has duplicate keys (matching hujson.Find).
func objectMember(obj *hujson.Object, key string) *hujson.Value {
	for i := range obj.Members {
		if lit, ok := obj.Members[i].Name.Value.(hujson.Literal); ok && lit.String() == key {
			return &obj.Members[i].Value
		}
	}
	return nil
}

// removeMember deletes the key at path (the final element) from its parent
// object. A no-op when any segment is missing.
func removeMember(root *hujson.Value, path []string) {
	cur := root
	for _, key := range path[:len(path)-1] {
		obj, ok := cur.Value.(*hujson.Object)
		if !ok {
			return
		}
		slot := objectMember(obj, key)
		if slot == nil {
			return
		}
		cur = slot
	}
	obj, ok := cur.Value.(*hujson.Object)
	if !ok {
		return
	}
	last := path[len(path)-1]
	for i := range obj.Members {
		if lit, ok := obj.Members[i].Name.Value.(hujson.Literal); ok && lit.String() == last {
			obj.Members = append(obj.Members[:i], obj.Members[i+1:]...)
			return
		}
	}
}

func arrayContains(arr *hujson.Array, val string) bool {
	for i := range arr.Elements {
		if lit, ok := arr.Elements[i].Value.(hujson.Literal); ok && lit.String() == val {
			return true
		}
	}
	return false
}

func removeArrayElement(arr *hujson.Array, val string) {
	for i := range arr.Elements {
		if lit, ok := arr.Elements[i].Value.(hujson.Literal); ok && lit.String() == val {
			arr.Elements = append(arr.Elements[:i], arr.Elements[i+1:]...)
			return
		}
	}
}

// sortEditPathsForDisplay returns a stable ordering of edits for preview
// rendering (by path) without mutating the input slice.
func sortEditPathsForDisplay(edits []Edit) []Edit {
	out := append([]Edit(nil), edits...)
	sort.SliceStable(out, func(i, j int) bool {
		return pathString(out[i].Path) < pathString(out[j].Path)
	})
	return out
}

func pathString(path []string) string {
	s := ""
	for i, p := range path {
		if i > 0 {
			s += "."
		}
		s += p
	}
	return s
}

// Describe renders a one-line human summary of an edit for the dry-run preview.
func (e Edit) Describe() string {
	p := pathString(e.Path)
	switch e.Op {
	case OpSet:
		return fmt.Sprintf("set %s = %s", p, e.valueString())
	case OpUnset:
		return fmt.Sprintf("unset %s", p)
	case OpArrayAppend:
		return fmt.Sprintf("add %q to %s", e.StringVal, p)
	case OpArrayRemove:
		return fmt.Sprintf("remove %q from %s", e.StringVal, p)
	default:
		return p
	}
}

func (e Edit) valueString() string {
	switch e.ValueKind {
	case KindBool:
		return strconv.FormatBool(e.BoolVal)
	case KindInt:
		return strconv.Itoa(e.IntVal)
	default:
		return strconv.Quote(e.StringVal)
	}
}
