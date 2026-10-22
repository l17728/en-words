// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type pilotColumnSpec struct {
	Name, Type  string
	Notnull, PK int
}
type pilotIndexSpec struct {
	Name    string
	Unique  int
	Origin  string
	Partial int
	Columns []string
}
type pilotFKSpec struct {
	Table, From, To, OnUpdate, OnDelete, Match string
	Seq                                        int
}
type pilotSchemaObject struct {
	Kind, Name, Table, SQL string
	Columns                []pilotColumnSpec
	Indexes                []pilotIndexSpec
	ForeignKeys            []pilotFKSpec
}

var pilotV2Objects = []pilotSchemaObject{
	{Kind: "table", Name: "classroom_mode_metadata", Table: "classroom_mode_metadata", SQL: `CREATE TABLE classroom_mode_metadata (
 id INTEGER PRIMARY KEY CHECK(id=1),
 schema_version INTEGER NOT NULL CHECK(schema_version=2)
)`, Columns: []pilotColumnSpec{{Name: "id", Type: "INTEGER", Notnull: 0, PK: 1}, {Name: "schema_version", Type: "INTEGER", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "table", Name: "classroom_scope_reservations", Table: "classroom_scope_reservations", SQL: `CREATE TABLE classroom_scope_reservations (
 student_id INTEGER NOT NULL CHECK(student_id>0),
 book_id INTEGER NOT NULL CHECK(book_id>0),
 mode TEXT NOT NULL CHECK(mode IN ('legacy','pilot')),
 schema_version INTEGER NOT NULL CHECK(schema_version=2),
 reserved_at INTEGER NOT NULL CHECK(reserved_at>=0),
 PRIMARY KEY(student_id,book_id),
 UNIQUE(student_id,book_id,mode)
)`, Columns: []pilotColumnSpec{{Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 1}, {Name: "book_id", Type: "INTEGER", Notnull: 1, PK: 2}, {Name: "mode", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "schema_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "reserved_at", Type: "INTEGER", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "pk", Partial: 0, Columns: []string{"student_id", "book_id"}}, {Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"student_id", "book_id", "mode"}}}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "trigger", Name: "classroom_scope_no_update", Table: "classroom_scope_reservations", SQL: `CREATE TRIGGER classroom_scope_no_update BEFORE UPDATE ON classroom_scope_reservations
 BEGIN SELECT RAISE(ABORT,'scope_immutable'); END`, Columns: []pilotColumnSpec{}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "trigger", Name: "classroom_scope_no_delete", Table: "classroom_scope_reservations", SQL: `CREATE TRIGGER classroom_scope_no_delete BEFORE DELETE ON classroom_scope_reservations
 BEGIN SELECT RAISE(ABORT,'scope_immutable'); END`, Columns: []pilotColumnSpec{}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "trigger", Name: "classroom_scope_no_reinsert", Table: "classroom_scope_reservations", SQL: `CREATE TRIGGER classroom_scope_no_reinsert BEFORE INSERT ON classroom_scope_reservations
 WHEN EXISTS(SELECT 1 FROM classroom_scope_reservations
 WHERE student_id=NEW.student_id AND book_id=NEW.book_id)
 BEGIN SELECT RAISE(ABORT,'scope_immutable'); END`, Columns: []pilotColumnSpec{}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "table", Name: "classroom_mode_reservations", Table: "classroom_mode_reservations", SQL: `CREATE TABLE classroom_mode_reservations (
 lesson_id INTEGER PRIMARY KEY REFERENCES lessons(id) ON DELETE RESTRICT,
 student_id INTEGER NOT NULL CHECK(student_id>0),
 book_id INTEGER NOT NULL CHECK(book_id>0),
 mode TEXT NOT NULL CHECK(mode IN ('legacy','pilot')),
 schema_version INTEGER NOT NULL CHECK(schema_version=2),
 reserved_at INTEGER NOT NULL CHECK(reserved_at>=0),
 first_entered_at INTEGER CHECK(first_entered_at IS NULL OR first_entered_at>=0),
 creation_nonce TEXT UNIQUE,
 creation_version INTEGER,
 creation_fingerprint TEXT,
 UNIQUE(lesson_id,student_id,book_id),
 FOREIGN KEY(student_id,book_id,mode)
  REFERENCES classroom_scope_reservations(student_id,book_id,mode) ON DELETE RESTRICT,
 CHECK((mode='legacy' AND creation_nonce IS NULL AND creation_version IS NULL AND creation_fingerprint IS NULL)
 OR (mode='pilot' AND creation_nonce IS NOT NULL AND length(creation_nonce) BETWEEN 1 AND 128
 AND creation_version IS NOT NULL AND creation_version=1
 AND creation_fingerprint IS NOT NULL AND length(creation_fingerprint)=64))
)`, Columns: []pilotColumnSpec{{Name: "lesson_id", Type: "INTEGER", Notnull: 0, PK: 1}, {Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "book_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "mode", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "schema_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "reserved_at", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "first_entered_at", Type: "INTEGER", Notnull: 0, PK: 0}, {Name: "creation_nonce", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "creation_version", Type: "INTEGER", Notnull: 0, PK: 0}, {Name: "creation_fingerprint", Type: "TEXT", Notnull: 0, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"creation_nonce"}}, {Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"lesson_id", "student_id", "book_id"}}}, ForeignKeys: []pilotFKSpec{{Table: "lessons", From: "lesson_id", To: "id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 0}, {Table: "classroom_scope_reservations", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 0}, {Table: "classroom_scope_reservations", From: "book_id", To: "book_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 1}, {Table: "classroom_scope_reservations", From: "mode", To: "mode", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 2}}},
	{Kind: "table", Name: "classroom_pilot_states", Table: "classroom_pilot_states", SQL: `CREATE TABLE classroom_pilot_states (
 lesson_id INTEGER PRIMARY KEY,
 student_id INTEGER NOT NULL CHECK(student_id>0),
 book_id INTEGER NOT NULL CHECK(book_id>0),
 schema_version INTEGER NOT NULL CHECK(schema_version=2),
 mode TEXT NOT NULL CHECK(mode IN ('pilot','draining','legacy')),
 lifecycle TEXT NOT NULL CHECK(lifecycle IN ('created','active','completed','retired')),
 checkpoint_version INTEGER NOT NULL CHECK(checkpoint_version>0),
 logical_input_revision INTEGER NOT NULL CHECK(logical_input_revision>=0),
 question_key TEXT,
 phase TEXT NOT NULL,
 auth_generation INTEGER NOT NULL CHECK(auth_generation>=0),
 connection_epoch TEXT,
 retired INTEGER NOT NULL CHECK(retired IN (0,1)),
 active_command_id TEXT,
 state_json TEXT NOT NULL CHECK(json_valid(state_json)),
 room_facts_json TEXT NOT NULL CHECK(json_valid(room_facts_json)),
 frozen_words_json TEXT NOT NULL CHECK(json_valid(frozen_words_json)),
 frozen_config_json TEXT NOT NULL CHECK(json_valid(frozen_config_json)),
 config_snapshot_schema_version INTEGER NOT NULL CHECK(config_snapshot_schema_version=1),
 config_revisions_json TEXT NOT NULL CHECK(json_valid(config_revisions_json)),
 config_fingerprint TEXT NOT NULL CHECK(length(config_fingerprint)=64),
 content_fingerprint TEXT NOT NULL CHECK(length(content_fingerprint)=64),
 creation_plan_json TEXT NOT NULL CHECK(json_valid(creation_plan_json)),
 created_at INTEGER NOT NULL CHECK(created_at>=0),
 updated_at INTEGER NOT NULL CHECK(updated_at>=created_at),
 UNIQUE(lesson_id,student_id),
 FOREIGN KEY(lesson_id,student_id,book_id)
 REFERENCES classroom_mode_reservations(lesson_id,student_id,book_id) ON DELETE RESTRICT,
 FOREIGN KEY(lesson_id,active_command_id)
 REFERENCES classroom_active_commands(lesson_id,command_id) DEFERRABLE INITIALLY DEFERRED,
 CHECK((lifecycle='created' AND phase='pending' AND logical_input_revision=0 AND question_key IS NULL
  AND active_command_id IS NULL)
  OR lifecycle<>'created'),
 CHECK((auth_generation=0 AND retired=0 AND connection_epoch IS NULL)
 OR (auth_generation>0 AND retired=0 AND connection_epoch IS NOT NULL AND length(connection_epoch) BETWEEN 1 AND 128)
 OR (auth_generation>0 AND retired=1 AND connection_epoch IS NULL))
)`, Columns: []pilotColumnSpec{{Name: "lesson_id", Type: "INTEGER", Notnull: 0, PK: 1}, {Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "book_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "schema_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "mode", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "lifecycle", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "checkpoint_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "logical_input_revision", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "question_key", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "phase", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "auth_generation", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "connection_epoch", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "retired", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "active_command_id", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "state_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "room_facts_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "frozen_words_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "frozen_config_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "config_snapshot_schema_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "config_revisions_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "config_fingerprint", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "content_fingerprint", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "creation_plan_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "created_at", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "updated_at", Type: "INTEGER", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"lesson_id", "student_id"}}}, ForeignKeys: []pilotFKSpec{{Table: "classroom_mode_reservations", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 0}, {Table: "classroom_mode_reservations", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 1}, {Table: "classroom_mode_reservations", From: "book_id", To: "book_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 2}, {Table: "classroom_active_commands", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 0}, {Table: "classroom_active_commands", From: "active_command_id", To: "command_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 1}}},
	{Kind: "table", Name: "classroom_transition_receipts", Table: "classroom_transition_receipts", SQL: `CREATE TABLE classroom_transition_receipts (
 lesson_id INTEGER NOT NULL,
 command_id TEXT NOT NULL CHECK(length(command_id) BETWEEN 1 AND 128),
 student_id INTEGER NOT NULL,
 request_fingerprint TEXT NOT NULL CHECK(length(request_fingerprint)=64),
 kind TEXT NOT NULL,
 from_version INTEGER NOT NULL CHECK(from_version>0),
 to_version INTEGER NOT NULL CHECK(to_version=from_version+1),
 logical_input_revision INTEGER NOT NULL CHECK(logical_input_revision>=0),
 decision_id TEXT NOT NULL UNIQUE CHECK(length(decision_id) BETWEEN 1 AND 128),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 committed_at INTEGER NOT NULL CHECK(committed_at>=0),
 PRIMARY KEY(lesson_id,command_id),
 UNIQUE(lesson_id,command_id,decision_id),
 UNIQUE(lesson_id,student_id,decision_id),
 FOREIGN KEY(lesson_id,student_id) REFERENCES classroom_pilot_states(lesson_id,student_id) ON DELETE RESTRICT
)`, Columns: []pilotColumnSpec{{Name: "lesson_id", Type: "INTEGER", Notnull: 1, PK: 1}, {Name: "command_id", Type: "TEXT", Notnull: 1, PK: 2}, {Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "request_fingerprint", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "kind", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "from_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "to_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "logical_input_revision", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "decision_id", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "result_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "committed_at", Type: "INTEGER", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"decision_id"}}, {Name: "", Unique: 1, Origin: "pk", Partial: 0, Columns: []string{"lesson_id", "command_id"}}, {Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"lesson_id", "command_id", "decision_id"}}, {Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"lesson_id", "student_id", "decision_id"}}}, ForeignKeys: []pilotFKSpec{{Table: "classroom_pilot_states", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 0}, {Table: "classroom_pilot_states", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 1}}},
	{Kind: "table", Name: "classroom_answer_claims", Table: "classroom_answer_claims", SQL: `CREATE TABLE classroom_answer_claims (
 lesson_id INTEGER NOT NULL,
 logical_input_revision INTEGER NOT NULL CHECK(logical_input_revision>0),
 student_id INTEGER NOT NULL,
 question_key TEXT NOT NULL CHECK(length(question_key) BETWEEN 1 AND 128),
 command_id TEXT NOT NULL,
 decision_id TEXT NOT NULL UNIQUE,
 answer_kind TEXT NOT NULL CHECK(answer_kind IN ('quiz','repeat','skip','timeout')),
 result_fingerprint TEXT NOT NULL CHECK(length(result_fingerprint)=64),
 PRIMARY KEY(lesson_id,logical_input_revision),
 FOREIGN KEY(lesson_id,student_id,decision_id)
 REFERENCES classroom_transition_receipts(lesson_id,student_id,decision_id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(lesson_id,command_id,decision_id)
 REFERENCES classroom_transition_receipts(lesson_id,command_id,decision_id) DEFERRABLE INITIALLY DEFERRED
)`, Columns: []pilotColumnSpec{{Name: "lesson_id", Type: "INTEGER", Notnull: 1, PK: 1}, {Name: "logical_input_revision", Type: "INTEGER", Notnull: 1, PK: 2}, {Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "question_key", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "command_id", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "decision_id", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "answer_kind", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "result_fingerprint", Type: "TEXT", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"decision_id"}}, {Name: "", Unique: 1, Origin: "pk", Partial: 0, Columns: []string{"lesson_id", "logical_input_revision"}}}, ForeignKeys: []pilotFKSpec{{Table: "classroom_transition_receipts", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 0}, {Table: "classroom_transition_receipts", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 1}, {Table: "classroom_transition_receipts", From: "decision_id", To: "decision_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 2}, {Table: "classroom_transition_receipts", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 0}, {Table: "classroom_transition_receipts", From: "command_id", To: "command_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 1}, {Table: "classroom_transition_receipts", From: "decision_id", To: "decision_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 2}}},
	{Kind: "table", Name: "classroom_active_commands", Table: "classroom_active_commands", SQL: `CREATE TABLE classroom_active_commands (
 lesson_id INTEGER NOT NULL,
 command_id TEXT NOT NULL CHECK(length(command_id) BETWEEN 1 AND 128),
 student_id INTEGER NOT NULL,
 request_fingerprint TEXT NOT NULL CHECK(length(request_fingerprint)=64),
 kind TEXT NOT NULL,
 logical_input_revision INTEGER NOT NULL CHECK(logical_input_revision>0),
 question_key TEXT NOT NULL CHECK(length(question_key) BETWEEN 1 AND 128),
 auth_generation INTEGER NOT NULL CHECK(auth_generation>0),
 capture_attempt_generation INTEGER NOT NULL CHECK(capture_attempt_generation>=0),
 status TEXT NOT NULL CHECK(status IN ('computing','committing','committed','failed')),
 claim_token TEXT NOT NULL CHECK(length(claim_token) BETWEEN 1 AND 128),
 worker_generation INTEGER NOT NULL CHECK(worker_generation>0),
 compute_deadline INTEGER NOT NULL CHECK(compute_deadline>=0),
 claim_until INTEGER NOT NULL CHECK(claim_until>=compute_deadline AND claim_until<=compute_deadline+5000),
 decision_id TEXT,
 failure_code TEXT,
 created_at INTEGER NOT NULL CHECK(created_at>=0),
 updated_at INTEGER NOT NULL CHECK(updated_at>=created_at),
 PRIMARY KEY(lesson_id,command_id),
 FOREIGN KEY(lesson_id,student_id) REFERENCES classroom_pilot_states(lesson_id,student_id) ON DELETE RESTRICT,
 FOREIGN KEY(lesson_id,command_id,decision_id)
 REFERENCES classroom_transition_receipts(lesson_id,command_id,decision_id) DEFERRABLE INITIALLY DEFERRED,
 CHECK((status='committed' AND decision_id IS NOT NULL AND failure_code IS NULL)
 OR (status='failed' AND decision_id IS NULL AND failure_code IS NOT NULL)
 OR (status IN ('computing','committing') AND decision_id IS NULL AND failure_code IS NULL))
)`, Columns: []pilotColumnSpec{{Name: "lesson_id", Type: "INTEGER", Notnull: 1, PK: 1}, {Name: "command_id", Type: "TEXT", Notnull: 1, PK: 2}, {Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "request_fingerprint", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "kind", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "logical_input_revision", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "question_key", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "auth_generation", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "capture_attempt_generation", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "status", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "claim_token", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "worker_generation", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "compute_deadline", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "claim_until", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "decision_id", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "failure_code", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "created_at", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "updated_at", Type: "INTEGER", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "pk", Partial: 0, Columns: []string{"lesson_id", "command_id"}}, {Name: "idx_classroom_active_one", Unique: 1, Origin: "c", Partial: 1, Columns: []string{"lesson_id"}}}, ForeignKeys: []pilotFKSpec{{Table: "classroom_pilot_states", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 0}, {Table: "classroom_pilot_states", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 1}, {Table: "classroom_transition_receipts", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 0}, {Table: "classroom_transition_receipts", From: "command_id", To: "command_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 1}, {Table: "classroom_transition_receipts", From: "decision_id", To: "decision_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 2}}},
	{Kind: "index", Name: "idx_classroom_active_one", Table: "classroom_active_commands", SQL: `CREATE UNIQUE INDEX idx_classroom_active_one
 ON classroom_active_commands(lesson_id) WHERE status IN ('computing','committing')`, Columns: []pilotColumnSpec{}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "table", Name: "classroom_effect_outbox", Table: "classroom_effect_outbox", SQL: `CREATE TABLE classroom_effect_outbox (
 decision_id TEXT NOT NULL,
 effect_index INTEGER NOT NULL CHECK(effect_index>=0),
 lesson_id INTEGER NOT NULL,
 student_id INTEGER NOT NULL,
 schema_version INTEGER NOT NULL CHECK(schema_version=2),
 checkpoint_version INTEGER NOT NULL CHECK(checkpoint_version>0),
 kind TEXT NOT NULL,
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 delivery_status TEXT NOT NULL CHECK(delivery_status IN ('pending','claimed','received','completed','canceled')),
 attempt_count INTEGER NOT NULL CHECK(attempt_count>=0),
 next_attempt_at INTEGER NOT NULL CHECK(next_attempt_at>=0),
 claim_token TEXT,
 claim_until INTEGER,
 output_auth_generation INTEGER,
 segment_id TEXT,
 lease_id TEXT,
 created_at INTEGER NOT NULL CHECK(created_at>=0),
 PRIMARY KEY(decision_id,effect_index),
 FOREIGN KEY(lesson_id,student_id,decision_id)
 REFERENCES classroom_transition_receipts(lesson_id,student_id,decision_id) ON DELETE RESTRICT,
 CHECK((delivery_status='claimed' AND claim_token IS NOT NULL AND claim_until IS NOT NULL)
 OR (delivery_status<>'claimed' AND claim_token IS NULL AND claim_until IS NULL)),
 CHECK((lease_id IS NULL AND output_auth_generation IS NULL)
 OR (lease_id IS NOT NULL AND output_auth_generation IS NOT NULL AND output_auth_generation>0))
)`, Columns: []pilotColumnSpec{{Name: "decision_id", Type: "TEXT", Notnull: 1, PK: 1}, {Name: "effect_index", Type: "INTEGER", Notnull: 1, PK: 2}, {Name: "lesson_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "schema_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "checkpoint_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "kind", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "payload_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "delivery_status", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "attempt_count", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "next_attempt_at", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "claim_token", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "claim_until", Type: "INTEGER", Notnull: 0, PK: 0}, {Name: "output_auth_generation", Type: "INTEGER", Notnull: 0, PK: 0}, {Name: "segment_id", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "lease_id", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "created_at", Type: "INTEGER", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "pk", Partial: 0, Columns: []string{"decision_id", "effect_index"}}, {Name: "idx_classroom_outbox_due", Unique: 0, Origin: "c", Partial: 0, Columns: []string{"delivery_status", "next_attempt_at"}}, {Name: "idx_classroom_outbox_lesson", Unique: 0, Origin: "c", Partial: 0, Columns: []string{"lesson_id", "checkpoint_version", "effect_index"}}}, ForeignKeys: []pilotFKSpec{{Table: "classroom_transition_receipts", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 0}, {Table: "classroom_transition_receipts", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 1}, {Table: "classroom_transition_receipts", From: "decision_id", To: "decision_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 2}}},
	{Kind: "index", Name: "idx_classroom_outbox_due", Table: "classroom_effect_outbox", SQL: `CREATE INDEX idx_classroom_outbox_due ON classroom_effect_outbox(delivery_status,next_attempt_at)`, Columns: []pilotColumnSpec{}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "index", Name: "idx_classroom_outbox_lesson", Table: "classroom_effect_outbox", SQL: `CREATE INDEX idx_classroom_outbox_lesson ON classroom_effect_outbox(lesson_id,checkpoint_version,effect_index)`, Columns: []pilotColumnSpec{}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
	{Kind: "table", Name: "classroom_completion_jobs", Table: "classroom_completion_jobs", SQL: `CREATE TABLE classroom_completion_jobs (
 lesson_id INTEGER PRIMARY KEY,
 student_id INTEGER NOT NULL,
 book_id INTEGER NOT NULL,
 result_state_json TEXT NOT NULL CHECK(json_valid(result_state_json)),
 frozen_plan_json TEXT NOT NULL CHECK(json_valid(frozen_plan_json)),
 plan_version INTEGER NOT NULL CHECK(plan_version>0),
 fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
 status TEXT NOT NULL CHECK(status IN ('pending','running','done')),
 claim_token TEXT,
 claim_until INTEGER,
 attempt_count INTEGER NOT NULL CHECK(attempt_count>=0),
 next_attempt_at INTEGER NOT NULL CHECK(next_attempt_at>=0),
 last_error_code TEXT,
 completion_decision_id TEXT UNIQUE,
 created_at INTEGER NOT NULL CHECK(created_at>=0),
 updated_at INTEGER NOT NULL CHECK(updated_at>=created_at),
 FOREIGN KEY(lesson_id,student_id,book_id)
 REFERENCES classroom_mode_reservations(lesson_id,student_id,book_id) ON DELETE RESTRICT,
 FOREIGN KEY(lesson_id,student_id,completion_decision_id)
 REFERENCES classroom_transition_receipts(lesson_id,student_id,decision_id) DEFERRABLE INITIALLY DEFERRED,
 CHECK((status='running' AND claim_token IS NOT NULL AND claim_until IS NOT NULL)
 OR (status<>'running' AND claim_token IS NULL AND claim_until IS NULL)),
 CHECK((status='done' AND completion_decision_id IS NOT NULL)
 OR (status<>'done' AND completion_decision_id IS NULL))
)`, Columns: []pilotColumnSpec{{Name: "lesson_id", Type: "INTEGER", Notnull: 0, PK: 1}, {Name: "student_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "book_id", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "result_state_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "frozen_plan_json", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "plan_version", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "fingerprint", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "status", Type: "TEXT", Notnull: 1, PK: 0}, {Name: "claim_token", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "claim_until", Type: "INTEGER", Notnull: 0, PK: 0}, {Name: "attempt_count", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "next_attempt_at", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "last_error_code", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "completion_decision_id", Type: "TEXT", Notnull: 0, PK: 0}, {Name: "created_at", Type: "INTEGER", Notnull: 1, PK: 0}, {Name: "updated_at", Type: "INTEGER", Notnull: 1, PK: 0}}, Indexes: []pilotIndexSpec{{Name: "", Unique: 1, Origin: "u", Partial: 0, Columns: []string{"completion_decision_id"}}, {Name: "idx_classroom_completion_due", Unique: 0, Origin: "c", Partial: 0, Columns: []string{"status", "next_attempt_at"}}}, ForeignKeys: []pilotFKSpec{{Table: "classroom_mode_reservations", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 0}, {Table: "classroom_mode_reservations", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 1}, {Table: "classroom_mode_reservations", From: "book_id", To: "book_id", OnUpdate: "NO ACTION", OnDelete: "RESTRICT", Match: "NONE", Seq: 2}, {Table: "classroom_transition_receipts", From: "lesson_id", To: "lesson_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 0}, {Table: "classroom_transition_receipts", From: "student_id", To: "student_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 1}, {Table: "classroom_transition_receipts", From: "completion_decision_id", To: "decision_id", OnUpdate: "NO ACTION", OnDelete: "NO ACTION", Match: "NONE", Seq: 2}}},
	{Kind: "index", Name: "idx_classroom_completion_due", Table: "classroom_completion_jobs", SQL: `CREATE INDEX idx_classroom_completion_due ON classroom_completion_jobs(status,next_attempt_at)`, Columns: []pilotColumnSpec{}, Indexes: []pilotIndexSpec{}, ForeignKeys: []pilotFKSpec{}},
}

// This is a NON-PRODUCTION / UNCOMPILED draft. Do not put it into the frozen
// server tree before schema/identity implementation is separately admitted.
// Generated DDL descriptors precede this body in the .go.draft output.
// REVIEW LIMIT: inspectPilotSchemaTx is an uncached DDL fallback, not the final
// per-card fast gate. Data scans live in auditPilotDataTx; a pool-bound verifier
// and transaction-local reuse remain required before hot-path integration.
// Legacy runtime attribution codecs are separate; explicit migration audits
// them even for old databases. Ordinary unmigrated startup does not newly reject
// historical orphan records. Ready-v2 audit coverage is stated below.

type PilotSchemaState string

const (
	PilotLegacyUnmigrated PilotSchemaState = "legacy_unmigrated"
	PilotSchemaReady      PilotSchemaState = "pilot_schema_ready"
	pilotV2MarkerKey                       = "pilot/schema/version"
	pilotV2MigrationID                     = "classroom-pilot-v2-initial"
)

var errPilotV2SchemaInvalid = errors.New("store: pilot schema invalid")

// inspectPilotSchema uses one consistent transaction; no DDL repair and no
// environment flag changes its answer. The returned mode is not a cached write
// authorization: all later write entries must recheck under their writer gate.
func inspectPilotSchema(ctx context.Context, db *gorm.DB) (PilotSchemaState, error) {
	if db == nil || db.Statement == nil || db.Dialector == nil || db.Dialector.Name() != "sqlite" {
		return "", errPilotV2SchemaInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var state PilotSchemaState
	err := atomicConnection(ctx, db, func(tx *gorm.DB) error {
		var err error
		state, err = inspectPilotSchemaTx(tx)
		if err != nil {
			return err
		}
		if state == PilotLegacyUnmigrated {
			return nil
		}
		return auditPilotDataTx(tx, state)
	})
	if err != nil {
		return "", err
	}
	return state, nil
}

// migratePilotSchema is an explicit trusted maintenance operation, not startup
// AutoMigrate. No models, HTTP, logging bodies, Room publication or user-supplied
// callback run here. atomicConnection owns COMMIT failure cleanup.
func migratePilotSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil || db.Statement == nil || db.Dialector == nil || db.Dialector.Name() != "sqlite" {
		return errPilotV2SchemaInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return atomicConnection(ctx, db, func(tx *gorm.DB) error {
		// Serializes with unmigrated legacy-scope bridge writers as well as all
		// SQLite writers. Deferred Begin alone is insufficient.
		if err := tx.Exec("UPDATE runtime_records SET key=key WHERE 1=0").Error; err != nil {
			return err
		}
		state, err := inspectPilotSchemaTx(tx)
		if err != nil {
			return err
		}
		if err := auditPilotDataTx(tx, state); err != nil {
			return err
		}
		if state == PilotSchemaReady {
			return nil
		}
		if state != PilotLegacyUnmigrated {
			return errPilotV2SchemaInvalid
		}
		scopes, err := collectPilotLegacyScopesTx(tx)
		if err != nil {
			return err
		}
		for _, object := range pilotV2Objects {
			if err := tx.Exec(object.SQL).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec("INSERT INTO classroom_mode_metadata(id,schema_version) VALUES(1,2)").Error; err != nil {
			return err
		}
		now := time.Now().UTC().UnixMilli()
		for _, scope := range scopes {
			// Sorted and deduplicated; ordinary INSERT. Permanent triggers must
			// never be bypassed with OR IGNORE/REPLACE or an UPSERT update.
			if err := tx.Exec("INSERT INTO classroom_scope_reservations(student_id,book_id,mode,schema_version,reserved_at) VALUES(?,?,'legacy',2,?)", scope.StudentID, scope.BookID, now).Error; err != nil {
				return err
			}
		}
		marker := `{"schema_version":2,"migration_id":"classroom-pilot-v2-initial"}`
		if err := tx.Exec("INSERT INTO runtime_records(key,value) VALUES(?,?)", pilotV2MarkerKey, marker).Error; err != nil {
			return err
		}
		state, err = inspectPilotSchemaTx(tx)
		if err != nil {
			return err
		}
		if state != PilotSchemaReady {
			return errPilotV2SchemaInvalid
		}
		return auditPilotDataTx(tx, state)
	})
}

type pilotMasterRow struct {
	Type, Name, TblName string
	SQL                 sql.NullString
}
type pilotColumn struct {
	CID       int            `gorm:"column:cid"`
	Name      string         `gorm:"column:name"`
	Type      string         `gorm:"column:type"`
	Notnull   int            `gorm:"column:notnull"`
	DfltValue sql.NullString `gorm:"column:dflt_value"`
	PK        int            `gorm:"column:pk"`
}
type pilotScopeKey struct{ StudentID, BookID int64 }

func inspectPilotSchemaTx(tx *gorm.DB) (PilotSchemaState, error) {
	if tx == nil || tx.Statement == nil || tx.Dialector.Name() != "sqlite" {
		return "", errPilotV2SchemaInvalid
	}
	if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
		return "", errPilotV2SchemaInvalid
	}
	var masters []pilotMasterRow
	if err := tx.Raw("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE type IN ('table','index','trigger','view')").Scan(&masters).Error; err != nil {
		return "", err
	}
	byName := make(map[string]pilotMasterRow, len(masters))
	for _, row := range masters {
		name := pilotASCIIName(row.Name)
		if _, exists := byName[name]; exists {
			return "", errPilotV2SchemaInvalid
		}
		byName[name] = row
	}
	var marker struct{ Value string }
	markerErr := tx.Table("runtime_records").Select("value").Where("key=?", pilotV2MarkerKey).Take(&marker).Error
	if markerErr != nil && !errors.Is(markerErr, gorm.ErrRecordNotFound) {
		return "", markerErr
	}
	ownedTables := make(map[string]bool)
	expectedNames := make(map[string]bool)
	ownedCount := 0
	for _, expected := range pilotV2Objects {
		expectedNames[expected.Name] = true
		if expected.Kind == "table" {
			ownedTables[expected.Name] = true
		}
		if _, exists := byName[expected.Name]; exists {
			ownedCount++
		}
	}
	for _, row := range masters {
		if pilotOwnedNamespace(row.Name) && !expectedNames[row.Name] {
			return "", errPilotV2SchemaInvalid
		}
	}
	if errors.Is(markerErr, gorm.ErrRecordNotFound) {
		if ownedCount != 0 {
			return "", errPilotV2SchemaInvalid
		}
		for _, row := range masters {
			if ownedTables[pilotASCIIName(row.TblName)] {
				return "", errPilotV2SchemaInvalid
			}
		}
		// Empty G1 scaffolding is not v2. Any persisted G1 classroom facts need
		// explicit handling rather than silent promotion to the new protocol.
		for _, table := range []string{"duplex_checkpoints", "duplex_receipts", "duplex_outboxes"} {
			if row, exists := byName[table]; exists {
				if row.Type != "table" {
					return "", errPilotV2SchemaInvalid
				}
				var present bool
				if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM " + pilotQuote(table) + " LIMIT 1)").Scan(&present).Error; err != nil {
					return "", err
				}
				if present {
					return "", errPilotV2SchemaInvalid
				}
			}
		}
		return PilotLegacyUnmigrated, nil
	}
	fields, err := pilotStrictObject([]byte(marker.Value), []string{"schema_version", "migration_id"})
	if err != nil {
		return "", err
	}
	var version int64
	var migrationID string
	if json.Unmarshal(fields["schema_version"], &version) != nil || version != 2 || json.Unmarshal(fields["migration_id"], &migrationID) != nil || migrationID != pilotV2MigrationID {
		return "", errPilotV2SchemaInvalid
	}
	if ownedCount != len(pilotV2Objects) {
		return "", errPilotV2SchemaInvalid
	}
	for _, expected := range pilotV2Objects {
		actual := byName[expected.Name]
		if actual.Name != expected.Name || actual.Type != expected.Kind || actual.TblName != expected.Table || !actual.SQL.Valid {
			return "", errPilotV2SchemaInvalid
		}
		a, err := pilotSQLTokens(actual.SQL.String)
		if err != nil {
			return "", err
		}
		b, err := pilotSQLTokens(expected.SQL)
		if err != nil {
			return "", err
		}
		if a != b {
			return "", errPilotV2SchemaInvalid
		}
		if expected.Kind == "table" {
			var columns []pilotColumn
			if err := tx.Raw("PRAGMA table_info(" + pilotQuote(expected.Name) + ")").Scan(&columns).Error; err != nil {
				return "", err
			}
			if len(columns) != len(expected.Columns) {
				return "", errPilotV2SchemaInvalid
			}
			for i, want := range expected.Columns {
				got := columns[i]
				if got.CID != i || got.Name != want.Name || !strings.EqualFold(got.Type, want.Type) || got.Notnull != want.Notnull || got.PK != want.PK || got.DfltValue.Valid {
					return "", errPilotV2SchemaInvalid
				}
			}
			if err := checkPilotIndexesAndForeignKeysTx(tx, expected); err != nil {
				return "", err
			}
		}
	}
	// Reject extra explicit indexes/triggers/views on owned tables, while
	// SQLite-generated PK/UNIQUE autoindexes are determined by the exact table DDL.
	for _, row := range masters {
		if ownedTables[row.TblName] && !expectedNames[row.Name] {
			if row.Type != "index" || row.SQL.Valid || !strings.HasPrefix(row.Name, "sqlite_autoindex_") {
				return "", errPilotV2SchemaInvalid
			}
		}
	}
	var metadata []struct{ ID, SchemaVersion int64 }
	if err := tx.Table("classroom_mode_metadata").Find(&metadata).Error; err != nil {
		return "", err
	}
	if len(metadata) != 1 || metadata[0].ID != 1 || metadata[0].SchemaVersion != 2 {
		return "", errPilotV2SchemaInvalid
	}
	return PilotSchemaReady, nil
}

// Explicit migration: audit even legacy records before ANY DDL. Ready startup
// also uses this. Ordinary unmigrated startup must not call it.
// Ready-v2 additionally audits the supported CREATED payload codec. Active
// states/future command facts are unsupported in this component slice and fail
// closed, not silently skipped. This is not a complete classroom-state codec.
func auditPilotDataTx(tx *gorm.DB, state PilotSchemaState) error {
	bridges, err := loadPilotLegacyBridgesTx(tx)
	if err != nil {
		return err
	}
	if _, err := auditPilotLegacyRuntimeTx(tx); err != nil {
		return err
	}
	if state == PilotLegacyUnmigrated {
		return nil
	}
	if state != PilotSchemaReady {
		return errPilotV2SchemaInvalid
	}
	for _, expected := range pilotV2Objects {
		if expected.Kind != "table" {
			continue
		}
		var violations []struct{ Table string }
		if err := tx.Raw("PRAGMA foreign_key_check(" + pilotQuote(expected.Name) + ")").Scan(&violations).Error; err != nil {
			return err
		}
		if len(violations) != 0 {
			return errPilotV2SchemaInvalid
		}
	}
	for _, bridge := range bridges {
		var scope struct {
			Mode          string
			SchemaVersion int64
		}
		if err := tx.Table("classroom_scope_reservations").Select("mode,schema_version").Where("student_id=? AND book_id=?", bridge.StudentID, bridge.BookID).Take(&scope).Error; err != nil {
			return err
		}
		if scope.Mode != "legacy" || scope.SchemaVersion != 2 {
			return errPilotV2SchemaInvalid
		}
	}
	return auditPilotCreatedDataTx(tx)
}

func checkPilotIndexesAndForeignKeysTx(tx *gorm.DB, expected pilotSchemaObject) error {
	var indexes []struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Origin  string `gorm:"column:origin"`
		Partial int    `gorm:"column:partial"`
	}
	if err := tx.Raw("PRAGMA index_list(" + pilotQuote(expected.Name) + ")").Scan(&indexes).Error; err != nil {
		return err
	}
	if len(indexes) != len(expected.Indexes) {
		return errPilotV2SchemaInvalid
	}
	actualIndexKeys := make([]string, 0, len(indexes))
	wantedIndexKeys := make([]string, 0, len(indexes))
	for _, index := range indexes {
		var fields []struct {
			Seq  int            `gorm:"column:seqno"`
			CID  int            `gorm:"column:cid"`
			Name sql.NullString `gorm:"column:name"`
			Desc int            `gorm:"column:desc"`
			Coll sql.NullString `gorm:"column:coll"`
			Key  int            `gorm:"column:key"`
		}
		if err := tx.Raw("PRAGMA index_xinfo(" + pilotQuote(index.Name) + ")").Scan(&fields).Error; err != nil {
			return err
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Seq < fields[j].Seq })
		spec := pilotIndexSpec{Name: index.Name, Unique: index.Unique, Origin: index.Origin, Partial: index.Partial}
		if index.Origin != "c" {
			spec.Name = ""
		}
		for _, field := range fields {
			if field.Key == 0 {
				continue
			}
			if field.Key != 1 || field.Seq != len(spec.Columns) || field.CID < 0 || field.CID >= len(expected.Columns) || !field.Name.Valid || field.Name.String != expected.Columns[field.CID].Name || field.Desc != 0 || !field.Coll.Valid || field.Coll.String != "BINARY" {
				return errPilotV2SchemaInvalid
			}
			spec.Columns = append(spec.Columns, field.Name.String)
		}
		b, _ := json.Marshal(spec)
		actualIndexKeys = append(actualIndexKeys, string(b))
	}
	for _, spec := range expected.Indexes {
		b, _ := json.Marshal(spec)
		wantedIndexKeys = append(wantedIndexKeys, string(b))
	}
	sort.Strings(actualIndexKeys)
	sort.Strings(wantedIndexKeys)
	if strings.Join(actualIndexKeys, "\n") != strings.Join(wantedIndexKeys, "\n") {
		return errPilotV2SchemaInvalid
	}
	var foreign []struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
		Match    string `gorm:"column:match"`
		Seq      int    `gorm:"column:seq"`
	}
	if err := tx.Raw("PRAGMA foreign_key_list(" + pilotQuote(expected.Name) + ")").Scan(&foreign).Error; err != nil {
		return err
	}
	actualFKs := make([]string, 0, len(foreign))
	wantedFKs := make([]string, 0, len(expected.ForeignKeys))
	for _, row := range foreign {
		spec := pilotFKSpec{row.Table, row.From, row.To, row.OnUpdate, row.OnDelete, row.Match, row.Seq}
		b, _ := json.Marshal(spec)
		actualFKs = append(actualFKs, string(b))
	}
	for _, spec := range expected.ForeignKeys {
		b, _ := json.Marshal(spec)
		wantedFKs = append(wantedFKs, string(b))
	}
	sort.Strings(actualFKs)
	sort.Strings(wantedFKs)
	if strings.Join(actualFKs, "\n") != strings.Join(wantedFKs, "\n") {
		return errPilotV2SchemaInvalid
	}
	return nil
}

func loadPilotLegacyBridgesTx(tx *gorm.DB) ([]pilotScopeKey, error) {
	var records []RuntimeRecord
	// Avoid LIKE '_' wildcards: exact prefix comparison.
	if err := tx.Where("substr(key,1,?)=?", len(pilotLegacyBridgePrefix), pilotLegacyBridgePrefix).Order("key").Find(&records).Error; err != nil {
		return nil, err
	}
	var scopes []pilotScopeKey
	for _, record := range records {
		bridge, err := decodePilotLegacyBridge(record.Key, record.Value)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, pilotScopeKey{bridge.StudentID, bridge.BookID})
	}
	return scopes, nil
}

func collectPilotLegacyScopesTx(tx *gorm.DB) ([]pilotScopeKey, error) {
	bridges, err := loadPilotLegacyBridgesTx(tx)
	if err != nil {
		return nil, err
	}
	seen := make(map[pilotScopeKey]bool)
	for _, scope := range bridges {
		seen[scope] = true
	}
	for _, table := range []string{"memory_cards", "placement_drafts", "lessons"} {
		var scopes []pilotScopeKey
		if err := tx.Table(table).Distinct("student_id", "book_id").Find(&scopes).Error; err != nil {
			return nil, err
		}
		for _, scope := range scopes {
			if scope.StudentID <= 0 || scope.BookID <= 0 {
				return nil, errPilotV2SchemaInvalid
			}
			seen[scope] = true
		}
	}
	runtimeScopes, err := auditPilotLegacyRuntimeTx(tx)
	if err != nil {
		return nil, err
	}
	for _, scope := range runtimeScopes {
		seen[scope] = true
	}
	result := make([]pilotScopeKey, 0, len(seen))
	for scope := range seen {
		result = append(result, scope)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StudentID != result[j].StudentID {
			return result[i].StudentID < result[j].StudentID
		}
		return result[i].BookID < result[j].BookID
	})
	return result, nil
}

func pilotStrictObject(raw []byte, keys []string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 4096 || !utf8.Valid(raw) {
		return nil, errPilotV2SchemaInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errPilotV2SchemaInvalid
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	fields := make(map[string]json.RawMessage, len(keys))
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] {
			return nil, errPilotV2SchemaInvalid
		}
		if _, exists := fields[key]; exists {
			return nil, errPilotV2SchemaInvalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errPilotV2SchemaInvalid
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errPilotV2SchemaInvalid
	}
	if len(fields) != len(keys) {
		return nil, errPilotV2SchemaInvalid
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errPilotV2SchemaInvalid
	}
	return fields, nil
}

func pilotQuote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// SQLite identifiers fold ASCII only. Lookup must detect noncanonical objects,
// while the inventory check still rejects their noncanonical original spelling.
func pilotASCIIName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func pilotOwnedNamespace(name string) bool {
	name = pilotASCIIName(name)
	for _, prefix := range []string{"classroom_mode_", "classroom_scope_", "classroom_pilot_", "classroom_transition_", "classroom_answer_", "classroom_active_", "classroom_effect_", "classroom_completion_", "idx_classroom_active_", "idx_classroom_outbox_", "idx_classroom_completion_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Lexical SQL normalization only: strip whitespace/comments, fold unquoted
// ASCII tokens, preserve quoted literal bytes. Not substring-based constraints.
func pilotSQLTokens(input string) (string, error) {
	var tokens []string
	for i := 0; i < len(input); {
		c := input[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			i++
			continue
		}
		if c == '-' && i+1 < len(input) && input[i+1] == '-' {
			for i < len(input) && input[i] != '\n' {
				i++
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			start, quote := i, c
			i++
			closed := false
			for i < len(input) {
				if input[i] == quote {
					i++
					if i < len(input) && input[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", errPilotV2SchemaInvalid
			}
			tokens = append(tokens, input[start:i])
			continue
		}
		if c == ';' {
			i++
			continue
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			start := i
			for i < len(input) {
				c = input[i]
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
					break
				}
				i++
			}
			tokens = append(tokens, strings.ToLower(input[start:i]))
			continue
		}
		tokens = append(tokens, string(c))
		i++
	}
	return strings.Join(tokens, " "), nil
}
