// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

type pilotCreatedState struct {
	SchemaVersion int64           `json:"schema_version"`
	Lifecycle     string          `json:"lifecycle"`
	Machine       json.RawMessage `json:"machine"`
}

type pilotCreatedRoomFacts struct {
	SchemaVersion        int64             `json:"schema_version"`
	EncourageCounter     int64             `json:"encourage_counter"`
	TeacherPending       bool              `json:"teacher_pending"`
	TeacherText          *string           `json:"teacher_text"`
	PhaseSince           *int64            `json:"phase_since"`
	PhaseDeadline        *int64            `json:"phase_deadline"`
	PausedRemainingMS    *int64            `json:"paused_remaining_ms"`
	LastActions          []json.RawMessage `json:"last_actions"`
	PendingOutputEffects []json.RawMessage `json:"pending_output_effects"`
	Input                json.RawMessage   `json:"input"`
}

func newPilotCreatedBaseline() (pilotCreatedState, pilotCreatedRoomFacts) {
	return pilotCreatedState{SchemaVersion: 2, Lifecycle: "created", Machine: json.RawMessage("null")},
		pilotCreatedRoomFacts{SchemaVersion: 1, LastActions: []json.RawMessage{},
			PendingOutputEffects: []json.RawMessage{}, Input: json.RawMessage("null")}
}

// Unlike marker objects, baseline fields deliberately permit explicit null.
// Duplicate/unknown/missing fields must still be rejected, so use an object
// decoder which tracks tokens rather than default Unmarshal's last-value wins.
// The nullable-field variant below retains the same token/size/UTF-8 checks;
// only the explicitly named fields accept null.
func decodePilotCreatedBaseline(stateJSON, factsJSON []byte) (pilotCreatedState, pilotCreatedRoomFacts, error) {
	var state pilotCreatedState
	var facts pilotCreatedRoomFacts
	if _, err := pilotStrictNullableObject(stateJSON,
		[]string{"schema_version", "lifecycle", "machine"}, []string{"machine"}); err != nil {
		return state, facts, err
	}
	if _, err := pilotStrictNullableObject(factsJSON,
		[]string{"schema_version", "encourage_counter", "teacher_pending", "teacher_text",
			"phase_since", "phase_deadline", "paused_remaining_ms", "last_actions", "pending_output_effects", "input"},
		[]string{"teacher_text", "phase_since", "phase_deadline", "paused_remaining_ms", "input"}); err != nil {
		return state, facts, err
	}
	if json.Unmarshal(stateJSON, &state) != nil || json.Unmarshal(factsJSON, &facts) != nil {
		return pilotCreatedState{}, pilotCreatedRoomFacts{}, errPilotSchemaInvalid
	}
	if state.SchemaVersion != 2 || state.Lifecycle != "created" || !bytes.Equal(bytes.TrimSpace(state.Machine), []byte("null")) ||
		facts.SchemaVersion != 1 || facts.EncourageCounter != 0 || facts.TeacherPending ||
		facts.TeacherText != nil || facts.PhaseSince != nil || facts.PhaseDeadline != nil || facts.PausedRemainingMS != nil ||
		facts.LastActions == nil || len(facts.LastActions) != 0 || facts.PendingOutputEffects == nil ||
		len(facts.PendingOutputEffects) != 0 || !bytes.Equal(bytes.TrimSpace(facts.Input), []byte("null")) {
		return pilotCreatedState{}, pilotCreatedRoomFacts{}, errPilotSchemaInvalid
	}
	return state, facts, nil
}

func pilotStrictNullableObject(raw []byte, keys, nullable []string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 4096 || !utf8.Valid(raw) {
		return nil, errPilotSchemaInvalid
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	nulls := make(map[string]bool, len(nullable))
	for _, key := range nullable {
		if !allowed[key] {
			return nil, errPilotSchemaInvalid
		}
		nulls[key] = true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errPilotSchemaInvalid
	}
	fields := make(map[string]json.RawMessage, len(keys))
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] {
			return nil, errPilotSchemaInvalid
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errPilotSchemaInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errPilotSchemaInvalid
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !nulls[key] {
			return nil, errPilotSchemaInvalid
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, errPilotSchemaInvalid
	}
	if len(fields) != len(keys) {
		return nil, errPilotSchemaInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errPilotSchemaInvalid
	}
	return fields, nil
}
