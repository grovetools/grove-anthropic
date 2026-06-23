// Package ccsettings models Claude Code's settings.json domain, discovers the
// scope files that contribute to it, merges them with per-key/per-element
// provenance under Claude's explicit precedence, and evaluates permission and
// sandbox rules against tool calls.
//
// The package is pure Go with no TUI or Cobra dependencies: it is the
// foundation that a settings browser renders on top of.
//
// A note on forward compatibility: Claude Code ships no version-exact JSON
// schema, and new releases add keys regularly. Every typed struct in this
// package therefore carries an Unknown bag and a tolerant decoder so that keys
// the model does not recognize are preserved verbatim and round-trip on write,
// rather than being silently dropped.
package ccsettings

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

var fieldNameCache sync.Map // reflect.Type -> map[string]struct{}

// jsonFieldNames returns the set of JSON object keys that the struct type t
// declares via `json:"..."` tags. The result is cached per type.
func jsonFieldNames(t reflect.Type) map[string]struct{} {
	if v, ok := fieldNameCache.Load(t); ok {
		return v.(map[string]struct{})
	}
	names := make(map[string]struct{})
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		names[name] = struct{}{}
	}
	fieldNameCache.Store(t, names)
	return names
}

// decodeKnown decodes the JSON-tagged ("known") fields of alias from data,
// tolerating per-key decode errors, and returns the leftover unknown keys plus
// warnings for any known key that failed to decode.
//
// alias MUST be a pointer to a struct type that has NO custom UnmarshalJSON of
// its own (declare a local `type alias T` to strip T's method set), otherwise
// this recurses infinitely.
//
// Decoding is done one key at a time so that a single key with an unexpected
// shape — for example a future Claude release that changes a field's type —
// neither aborts the whole decode nor drops the value: the offending key is
// preserved in the unknown bag and surfaced as a warning.
func decodeKnown(data []byte, alias any) (unknown map[string]json.RawMessage, warnings []string, err error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, err
	}
	known := jsonFieldNames(reflect.TypeOf(alias).Elem())
	unknown = make(map[string]json.RawMessage)
	for key, val := range raw {
		if _, ok := known[key]; !ok {
			unknown[key] = val
			continue
		}
		single, _ := json.Marshal(map[string]json.RawMessage{key: val})
		if e := json.Unmarshal(single, alias); e != nil {
			unknown[key] = val
			warnings = append(warnings, key+": "+e.Error())
		}
	}
	return unknown, warnings, nil
}

// marshalWithUnknown marshals alias (the typed view of a value) and folds the
// preserved unknown keys back in, so passthrough config round-trips on write.
//
// Note: this does not preserve original key order; structure-preserving,
// order-stable write-back is a later concern (the settings editor). What it
// guarantees here is that no key is lost across a decode/encode cycle.
func marshalWithUnknown(alias any, unknown map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	if len(unknown) == 0 {
		return b, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range unknown {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	return json.Marshal(m)
}
