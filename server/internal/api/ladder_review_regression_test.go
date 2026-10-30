package api

import (
	"context"
	"encoding/json"
	"enwords/internal/config"
	"enwords/internal/store"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLadderOwnedScopeObjectsCannotBypassProtection(t *testing.T) {
	for _, ddl := range []string{
		"CREATE TABLE CLASSROOM_SCOPE_RESERVATIONS(student_id INTEGER, mode TEXT, schema_version INTEGER, reserved_at INTEGER)",
		"CREATE VIEW classroom_scope_reservations AS SELECT 1 AS student_id, 'legacy' AS mode, 2 AS schema_version, 0 AS reserved_at",
		"CREATE TABLE classroom_scope_reservations(student_id INTEGER)",
	} {
		t.Run(ddl, func(t *testing.T) {
			a, _, sid := feedbackTwoApps(t)
			require.NoError(t, a.DB.Exec(ddl).Error)
			if strings.Contains(ddl, "TABLE CLASSROOM") {
				require.NoError(t, a.DB.Exec("INSERT INTO CLASSROOM_SCOPE_RESERVATIONS VALUES(?, 'pilot', 2, 0)", sid).Error)
			}
			before, err := a.EffectiveConfiguration(context.Background(), sid)
			require.NoError(t, err)
			var audits int64
			require.NoError(t, a.DB.Model(&store.AuditLog{}).Count(&audits).Error)
			_, err = a.ChangeConfig(context.Background(), config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeGlobal, Key: "ladder", Value: []int{1, 2, 3}, Reason: "must fail closed"})
			require.ErrorContains(t, err, "atomic_configuration_unavailable")
			after, err := a.EffectiveConfiguration(context.Background(), sid)
			require.NoError(t, err)
			require.Equal(t, before, after)
			var count int64
			require.NoError(t, a.DB.Model(&store.AuditLog{}).Count(&count).Error)
			require.Equal(t, audits, count)
			require.NoError(t, a.DB.Model(&store.MemoryCard{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestLadderRawJSONRejectsAmbiguousKeysWithoutWrites(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	token, _ := loginGet(t, a, "dad", "family123")
	router := a.Router()
	before, err := a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, err)
	var audits int64
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Count(&audits).Error)
	fields := []string{`"scope":"global"`, `"studentId":0`, `"key":"ladder"`, `"value":[1,2,3]`, `"reason":"test"`}
	for i, field := range fields {
		key := strings.SplitN(field, ":", 2)[0]
		for _, bad := range []string{field + "," + field, strings.Replace(field, key, strings.ToUpper(key), 1), key + ":null"} {
			candidate := append([]string(nil), fields...)
			candidate[i] = bad
			body := "{" + strings.Join(candidate, ",") + "}"
			req := httptest.NewRequest("POST", "/api/v1/config/set", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			var result struct{ Code int }
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
			require.Equal(t, 400, result.Code, body)
		}
	}
	for _, body := range []string{fmt.Sprintf(`{"studentId":%d,"StudentId":%d,"key":"ladder"}`, sid, sid), `{"studentId":null,"key":"ladder"}`} {
		req := httptest.NewRequest("DELETE", "/api/v1/config/override", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var result struct{ Code int }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.Equal(t, 400, result.Code)
	}
	for _, body := range []string{
		`{"scope":"global","key":"ladder","value":{"value":[1,2,3]},"reason":"nested"}`,
		`{"scope":"global","key":"ladder","value":[1,2,3],"reason":"tail"}{}`,
		`{"scope":"global","key":"ladder","value":[1,2,3],"reason":"` + strings.Repeat("x", 8192) + `"}`,
	} {
		req := httptest.NewRequest("POST", "/api/v1/config/set", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var result struct{ Code int }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.Equal(t, 400, result.Code)
	}

	after, err := a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, err)
	require.Equal(t, before, after)
	var count int64
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Count(&count).Error)
	require.Equal(t, audits, count)
	require.NoError(t, a.DB.Model(&store.MemoryCard{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestLadderPersistenceAndAuditFailuresRollbackAllEffects(t *testing.T) {
	for _, table := range []string{"config_globals", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			a, _, sid := feedbackTwoApps(t)
			require.NoError(t, a.Sched.SeedCard(sid, 1, 1))
			card, err := a.Sched.GetCard(sid, 1, 1)
			require.NoError(t, err)
			card.LadderStep = 6
			require.NoError(t, store.NewSchedulerRepo(a.DB).SaveCard(card))
			before, err := a.EffectiveConfiguration(context.Background(), sid)
			require.NoError(t, err)
			require.NoError(t, a.DB.Exec("CREATE TRIGGER reject_configuration BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'fixture'); END").Error)
			_, err = a.ChangeConfig(context.Background(), config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeGlobal, Key: "ladder", Value: []int{1, 2, 3}, Reason: "rollback"})
			require.Error(t, err)
			after, err := a.EffectiveConfiguration(context.Background(), sid)
			require.NoError(t, err)
			require.Equal(t, before, after)
			actual, err := a.Sched.GetCard(sid, 1, 1)
			require.NoError(t, err)
			require.Equal(t, card, actual)
		})
	}
}

func TestLadderCancellationAndAbsentDeleteHaveNoWrites(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	before, err := a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, err)
	var audits int64
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Count(&audits).Error)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.ChangeConfig(ctx, config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeGlobal, Key: "ladder", Value: []int{1, 2, 3}, Reason: "canceled"})
	require.ErrorIs(t, err, context.Canceled)
	for _, table := range []string{"config_globals", "config_overrides", "audit_logs", "memory_cards"} {
		require.NoError(t, a.DB.Exec("CREATE TRIGGER no_delete_"+table+" BEFORE DELETE ON "+table+" BEGIN SELECT RAISE(ABORT,'unexpected delete'); END").Error)
		require.NoError(t, a.DB.Exec("CREATE TRIGGER no_insert_"+table+" BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'unexpected insert'); END").Error)
		require.NoError(t, a.DB.Exec("CREATE TRIGGER no_update_"+table+" BEFORE UPDATE ON "+table+" BEGIN SELECT RAISE(ABORT,'unexpected update'); END").Error)
	}
	result, err := a.ChangeConfig(context.Background(), config.Mutation{Operation: "delete_override", Actor: "admin", Scope: config.ScopeStudent, StudentID: sid, Key: "ladder"})
	require.NoError(t, err)
	require.True(t, result.Noop)
	after, err := a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, err)
	require.Equal(t, before, after)
	var count int64
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Count(&count).Error)
	require.Equal(t, audits, count)
}
