// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"enwords/internal/classroom"
	"enwords/internal/scheduler"
	"gorm.io/gorm"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

func auditPilotLegacyRuntimeTx(tx *gorm.DB) ([]pilotScopeKey, error) {
	var records []RuntimeRecord
	if err := tx.Where("substr(key,1,9)='snapshot/' OR substr(key,1,7)='report/' OR substr(key,1,8)='settled/' OR substr(key,1,13)='agent_result/'").Find(&records).Error; err != nil {
		return nil, err
	}
	scopes := make([]pilotScopeKey, 0, len(records))
	for _, record := range records {
		parts := strings.Split(record.Key, "/")
		if len(parts) != 2 {
			return nil, errPilotV2SchemaInvalid
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[1] {
			return nil, errPilotV2SchemaInvalid
		}
		var lesson Lesson
		if err = tx.First(&lesson, id).Error; err != nil {
			return nil, err
		}
		if lesson.StudentID <= 0 || lesson.BookID <= 0 {
			return nil, errPilotV2SchemaInvalid
		}
		raw := []byte(record.Value)
		if err = pilotAuditJSON(raw); err != nil {
			return nil, err
		}
		if err = auditPilotLegacyPayload(parts[0], raw, id, lesson.StudentID, lesson.BookID); err != nil {
			return nil, err
		}
		scopes = append(scopes, pilotScopeKey{lesson.StudentID, lesson.BookID})
	}
	return scopes, nil
}

func pilotDecodeLegacy(raw []byte, dst any) error {
	if err := pilotRejectLegacyScalarNull(raw, reflect.TypeOf(dst).Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return errPilotV2SchemaInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return errPilotV2SchemaInvalid
	}
	return nil
}

func pilotRejectLegacyScalarNull(raw []byte, t reflect.Type) error {
	// Raw tool payloads are deliberately opaque historical JSON, not null-safe
	// numeric fields. Their syntax has already passed the recursive token walk.
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		switch t.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
			return nil
		}
		return errPilotV2SchemaInvalid
	}
	if t.Kind() == reflect.Pointer {
		return pilotRejectLegacyScalarNull(raw, t.Elem())
	}
	switch t.Kind() {
	case reflect.Struct:
		// time.Time and other JSON codecs consume scalars themselves.
		if reflect.PointerTo(t).Implements(reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()) {
			return nil
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return errPilotV2SchemaInvalid
		}
		allowed := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			allowed[name] = f.Type
		}
		// Persisted writers use exact field/tag spelling. Do not inherit Go JSON's
		// Unicode case-fold aliases (which are broader than strings.ToLower).
		for key, value := range fields {
			fieldType, ok := allowed[key]
			if !ok {
				return errPilotV2SchemaInvalid
			}
			if err := pilotRejectLegacyScalarNull(value, fieldType); err != nil {
				return err
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return errPilotV2SchemaInvalid
		}
		for key, value := range fields {
			switch t.Key().Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				n, err := strconv.ParseInt(key, 10, t.Key().Bits())
				if err != nil || strconv.FormatInt(n, 10) != key {
					return errPilotV2SchemaInvalid
				}
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				n, err := strconv.ParseUint(key, 10, t.Key().Bits())
				if err != nil || strconv.FormatUint(n, 10) != key {
					return errPilotV2SchemaInvalid
				}
			}
			if err := pilotRejectLegacyScalarNull(value, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return errPilotV2SchemaInvalid
		}
		for _, value := range values {
			if err := pilotRejectLegacyScalarNull(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func pilotRequired(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
	}
	return true
}

func auditPilotLegacyPayload(kind string, raw []byte, id, sid, bid int64) error {
	switch kind {
	case "snapshot":
		if !pilotRequired(raw, "LessonID", "StudentID", "BookID", "Phase") {
			return errPilotV2SchemaInvalid
		}
		var st classroom.State
		if err := pilotDecodeLegacy(raw, &st); err != nil {
			return err
		}
		if st.LessonID != id || st.StudentID != sid || st.BookID != bid {
			return errPilotV2SchemaInvalid
		}
		switch st.Phase {
		case classroom.PhaseShowWord, classroom.PhaseTTSSeg, classroom.PhaseRepeating, classroom.PhaseWaitAnswer, classroom.PhaseJudging, classroom.PhaseReteachShow, classroom.PhaseLessonEnd:
		default:
			return errPilotV2SchemaInvalid
		}
		// This does not call Restore, recover delivery, validate a new pilot state,
		// or reinterpret old optional fields as a new teaching opportunity.
	case "report":
		if !pilotRequired(raw, "TotalWords", "Report") {
			return errPilotV2SchemaInvalid
		}
		var result classroom.Result
		if err := pilotDecodeLegacy(raw, &result); err != nil {
			return err
		}
		if result.TotalWords < 0 {
			return errPilotV2SchemaInvalid
		}
		for wordID := range result.Correct {
			if wordID <= 0 {
				return errPilotV2SchemaInvalid
			}
		}
	case "settled":
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return errPilotV2SchemaInvalid
		}
		// Empty object is the explicit owner-less legacy marker form. Its owner
		// must still be resolved from the persisted Lesson above. Other historical
		// forms require source evidence and a named compatibility case, not guessing.
		if len(fields) == 0 {
			return nil
		}
		if !pilotRequired(raw, "version", "student_id", "book_id", "fingerprint") || len(fields) != 4 {
			return errPilotV2SchemaInvalid
		}
		var marker struct {
			Version     int    `json:"version"`
			StudentID   int64  `json:"student_id"`
			BookID      int64  `json:"book_id"`
			Fingerprint string `json:"fingerprint"`
		}
		if err := pilotDecodeLegacy(raw, &marker); err != nil {
			return err
		}
		hash, err := hex.DecodeString(marker.Fingerprint)
		if marker.Version != 1 || marker.StudentID != sid || marker.BookID != bid || err != nil || len(hash) != 32 || hex.EncodeToString(hash) != marker.Fingerprint {
			return errPilotV2SchemaInvalid
		}
	case "agent_result":
		// Shapes mirror api.lessonStep and lessonScheduleResult without an api import.
		var result struct {
			Actions []struct {
				Index               int
				Kind                string
				Delta               int
				MemoryKind, Content string
				Importance          int
			} `json:"actions"`
			Results map[int]json.RawMessage `json:"results"`
			Next    *struct {
				Status               string            `json:"status"`
				Lesson               *scheduler.Lesson `json:"lesson,omitempty"`
				RequestedRatio       float64           `json:"requested_ratio"`
				SelectedNewCount     int               `json:"selected_new_count"`
				FirstLessonExemption bool              `json:"first_lesson_exemption"`
			} `json:"next"`
			Replies []string `json:"replies"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 4 {
			return errPilotV2SchemaInvalid
		}
		for _, k := range []string{"actions", "results", "next", "replies"} {
			if _, ok := fields[k]; !ok {
				return errPilotV2SchemaInvalid
			}
		}
		if err := pilotDecodeLegacy(raw, &result); err != nil {
			return err
		}
		indices := map[int]bool{}
		for _, step := range result.Actions {
			if step.Index < 0 || indices[step.Index] || step.Kind == "" {
				return errPilotV2SchemaInvalid
			}
			indices[step.Index] = true
		}
		for index := range result.Results {
			if !indices[index] {
				return errPilotV2SchemaInvalid
			}
		}
		if result.Next != nil && result.Next.Lesson != nil {
			next := result.Next.Lesson
			if next.ID <= 0 || next.StudentID != sid || next.BookID != bid {
				return errPilotV2SchemaInvalid
			}
		}
		// Tool result bodies are heterogeneous historical facts. The recursive JSON
		// audit enforces unambiguous syntax, not an invented current tool schema.
	default:
		return errPilotV2SchemaInvalid
	}
	return nil
}

// Legacy maps/slices legitimately contain null. Reject duplicates (including
// case aliases), invalid UTF-8/trailing data and excessive nesting globally.
// No new arbitrary byte cap: old runtime records have no such stored contract.
func pilotAuditJSON(raw []byte) error {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return errPilotV2SchemaInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errPilotV2SchemaInvalid
		}
		token, err := d.Token()
		if err != nil {
			return errPilotV2SchemaInvalid
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				s, ok := key.(string)
				if err != nil || !ok {
					return errPilotV2SchemaInvalid
				}
				s = strings.ToLower(s)
				if seen[s] {
					return errPilotV2SchemaInvalid
				}
				seen[s] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return errPilotV2SchemaInvalid
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return errPilotV2SchemaInvalid
			}
		default:
			return errPilotV2SchemaInvalid
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errPilotV2SchemaInvalid
	}
	return nil
}
