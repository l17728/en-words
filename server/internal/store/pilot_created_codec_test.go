// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestPilotCreatedBaselineStrictDraft(t *testing.T) {
	state, facts := newPilotCreatedBaseline()
	s, e := json.Marshal(state)
	require.NoError(t, e)
	f, e := json.Marshal(facts)
	require.NoError(t, e)
	gotS, gotF, e := decodePilotCreatedBaseline(s, f)
	require.NoError(t, e)
	require.Equal(t, state, gotS)
	require.Equal(t, facts, gotF)
	// Explicit null is required, not an absent field or an empty object.
	require.JSONEq(t, `{"schema_version":2,"lifecycle":"created","machine":null}`, string(s))
	for _, side := range []string{"state", "facts"} {
		original := s
		if side == "facts" {
			original = f
		}
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(original, &fields))
		check := func(t *testing.T, b []byte) {
			ss, ff := s, f
			if side == "state" {
				ss = b
			} else {
				ff = b
			}
			_, _, err := decodePilotCreatedBaseline(ss, ff)
			require.Error(t, err)
		}
		for key := range fields {
			t.Run(side+"/missing/"+key, func(t *testing.T) {
				clone := map[string]json.RawMessage{}
				for k, v := range fields {
					if k != key {
						clone[k] = v
					}
				}
				b, err := json.Marshal(clone)
				require.NoError(t, err)
				check(t, b)
			})
			t.Run(side+"/duplicate/"+key, func(t *testing.T) {
				k, _ := json.Marshal(key)
				b := append([]byte(nil), original[:len(original)-1]...)
				b = append(b, ',')
				b = append(b, k...)
				b = append(b, ':')
				b = append(b, fields[key]...)
				b = append(b, '}')
				check(t, b)
			})
			t.Run(side+"/wrong_type/"+key, func(t *testing.T) {
				clone := map[string]json.RawMessage{}
				for k, v := range fields {
					clone[k] = v
				}
				clone[key] = json.RawMessage(`{"unexpected":true}`)
				b, err := json.Marshal(clone)
				require.NoError(t, err)
				check(t, b)
			})
		}
		for name, b := range map[string][]byte{
			"invalid_utf8": append(append([]byte(nil), original...), 0xff),
			"oversize":     append(append([]byte(nil), original...), bytes.Repeat([]byte(" "), 4097-len(original))...),
			"tail_object":  append(append([]byte(nil), original...), []byte(` {}`)...),
			"root_null":    []byte(`null`), "root_array": []byte(`[]`),
			"unknown":           []byte(strings.Replace(string(original), `"schema_version":`, `"unknown":0,"schema_version":`, 1)),
			"case_alias":        []byte(strings.Replace(string(original), `"schema_version":`, `"Schema_version":`, 1)),
			"escaped_duplicate": []byte(strings.Replace(string(original), `"schema_version":`, `"\u0073chema_version":1,"schema_version":`, 1)),
		} {
			t.Run(side+"/"+name, func(t *testing.T) { check(t, b) })
		}
	}
	for _, tc := range []struct{ name, side, key, value string }{
		{"state_version_null", "state", "schema_version", "null"},
		{"lifecycle_null", "state", "lifecycle", "null"},
		{"active_lifecycle", "state", "lifecycle", `"active"`},
		{"machine_active", "state", "machine", `{"phase":"teaching"}`},
		{"version_float", "state", "schema_version", "2.0"},
		{"version_exponent", "state", "schema_version", "2e0"},
		{"facts_version_null", "facts", "schema_version", "null"},
		{"counter_null", "facts", "encourage_counter", "null"},
		{"pending_null", "facts", "teacher_pending", "null"},
		{"actions_null", "facts", "last_actions", "null"},
		{"effects_null", "facts", "pending_output_effects", "null"},
		{"counter_active", "facts", "encourage_counter", "1"},
		{"pending_active", "facts", "teacher_pending", "true"},
		{"text_even_empty", "facts", "teacher_text", `""`},
		{"phase_even_zero", "facts", "phase_since", "0"},
		{"deadline_even_zero", "facts", "phase_deadline", "0"},
		{"remaining_even_zero", "facts", "paused_remaining_ms", "0"},
		{"actions_active", "facts", "last_actions", `[{}]`},
		{"effects_active", "facts", "pending_output_effects", `[{}]`},
		{"input_active", "facts", "input", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ss, ff := s, f
			raw := s
			if tc.side == "facts" {
				raw = f
			}
			var obj map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &obj))
			obj[tc.key] = json.RawMessage(tc.value)
			b, err := json.Marshal(obj)
			require.NoError(t, err)
			if tc.side == "state" {
				ss = b
			} else {
				ff = b
			}
			_, _, err = decodePilotCreatedBaseline(ss, ff)
			require.Error(t, err)
		})
	}
	// Exact size boundary and JSON whitespace around explicit null remain legal.
	padded := append(append([]byte(nil), s...), bytes.Repeat([]byte(" "), 4096-len(s))...)
	_, _, e = decodePilotCreatedBaseline(padded, f)
	require.NoError(t, e)
	_, _, e = decodePilotCreatedBaseline([]byte(strings.Replace(string(s), `:null`, `: null `, 1)), f)
	require.NoError(t, e)
}
