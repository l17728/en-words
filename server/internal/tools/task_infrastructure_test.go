package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/obs"
	"github.com/stretchr/testify/require"
)

func TestSharedTaskBudgetAcrossConcurrentRegistries(t *testing.T) {
	audit := &MemAuditor{}
	registries := []*Registry{New(audit), New(audit)}
	var handled atomic.Int32
	for _, registry := range registries {
		require.NoError(t, registry.Register(Tool{Name: "task.run", Risk: RiskLow, Scope: []Role{RoleSystemAgent}, Handler: func(context.Context, Caller, json.RawMessage) (any, error) { handled.Add(1); return nil, nil }}))
	}
	ctx := WithEventBudget(obs.WithTrace(context.Background(), "fixture-task"), 8)
	var successes, denied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			derived := WithEventBudget(context.WithValue(ctx, struct{ Index int }{i}, i), 100)
			_, err := registries[i%2].Call(derived, systemA, "task.run", json.RawMessage(`{}`))
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, ErrBudgetExceeded) {
				denied.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 8, successes.Load())
	require.EqualValues(t, 8, handled.Load())
	require.EqualValues(t, 56, denied.Load())
	entries := audit.Snapshot()
	require.Len(t, entries, 64)
	for _, entry := range entries {
		require.Equal(t, "fixture-task", entry.TraceID)
		require.Positive(t, entry.DurationMs)
	}
	entries[0].Result = "external mutation"
	require.NotEqual(t, "external mutation", audit.Snapshot()[0].Result)
}

func TestCanceledCallsPreserveBudgetAndPropagateContext(t *testing.T) {
	audit := &MemAuditor{}
	registry := New(audit)
	entered := make(chan struct{})
	var calls atomic.Int32
	require.NoError(t, registry.Register(Tool{Name: "task.wait", Risk: RiskLow, Scope: []Role{RoleSystemAgent}, Handler: func(ctx context.Context, _ Caller, _ json.RawMessage) (any, error) {
		calls.Add(1)
		if obs.TraceID(ctx) != "cancel-trace" {
			return nil, fmt.Errorf("trace lost")
		}
		close(entered)
		<-ctx.Done()
		return nil, fmt.Errorf("wrapped: %w", ctx.Err())
	}}))
	parent := WithEventBudget(obs.WithTrace(context.Background(), "cancel-trace"), 1)
	canceled, cancel := context.WithCancel(parent)
	cancel()
	_, err := registry.Call(canceled, systemA, "task.wait", nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, calls.Load())
	active, stop := context.WithCancel(parent)
	defer stop()
	result := make(chan error, 1)
	go func() { _, err := registry.Call(active, systemA, "task.wait", nil); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not enter with remaining budget")
	}
	stop()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("handler did not observe cancellation")
	}
	require.EqualValues(t, 1, calls.Load())
	require.Len(t, audit.Snapshot(), 2)
	for _, entry := range audit.Snapshot() {
		require.Equal(t, "canceled", entry.Result)
	}
	deadline, cancelDeadline := context.WithDeadline(parent, time.Now().Add(-time.Second))
	defer cancelDeadline()
	_, err = registry.Call(deadline, systemA, "task.wait", nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "deadline_exceeded", audit.Snapshot()[2].Result)
}

func TestAuditNeverStoresSensitiveHandlerErrorsOrArguments(t *testing.T) {
	audit := &MemAuditor{}
	registry := New(audit)
	const private = "student private answer and secret token"
	fixtureErr := fmt.Errorf("upstream echoed %s", private)
	require.NoError(t, registry.Register(Tool{Name: "task.fail", Risk: RiskLow, Scope: []Role{RoleSystemAgent}, Handler: func(context.Context, Caller, json.RawMessage) (any, error) { return nil, fixtureErr }}))
	_, err := registry.Call(context.Background(), systemA, "task.fail", json.RawMessage(`{"answer":"`+private+`"}`))
	require.ErrorIs(t, err, fixtureErr)
	entry := audit.Snapshot()[0]
	require.Equal(t, "handler_failed", entry.Result)
	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), private)
	var summary map[string]int
	require.NoError(t, json.Unmarshal([]byte(entry.Args), &summary))
	require.Len(t, summary, 1)
	require.Positive(t, summary["bytes"])
}

func TestCompletedHandlerIsNotReportedAsRolledBackByLateCancellation(t *testing.T) {
	audit := &MemAuditor{}
	registry := New(audit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.Register(Tool{Name: "task.commit", Risk: RiskLow, Scope: []Role{RoleSystemAgent}, Handler: func(context.Context, Caller, json.RawMessage) (any, error) {
		// The side effect has completed successfully before cancellation arrives.
		cancel()
		return "committed", nil
	}}))
	result, err := registry.Call(ctx, systemA, "task.commit", nil)
	require.NoError(t, err)
	require.Equal(t, "committed", result)
	require.Equal(t, "ok", audit.Snapshot()[0].Result)
	require.True(t, audit.Snapshot()[0].OK)
}

func TestToolDefinitionsStableAndIsolatedFromExternalMutation(t *testing.T) {
	registry := New(nil)
	scope := []Role{RoleSystemAgent}
	schema := json.RawMessage(`{"type":"object","required":["reason"]}`)
	handler := func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil }
	require.NoError(t, registry.Register(Tool{Name: "z.last", Risk: RiskLow, Scope: scope, InputSchema: schema, Handler: handler}))
	require.NoError(t, registry.Register(Tool{Name: "a.first", Risk: RiskLow, Scope: scope, Handler: handler}))
	scope[0] = RoleStudent
	schema[0] = '!'
	for i := 0; i < 20; i++ {
		defs := registry.ToolsFor(RoleSystemAgent)
		require.Len(t, defs, 2)
		require.Equal(t, "a.first", defs[0].Name)
		require.Equal(t, "z.last", defs[1].Name)
		require.True(t, json.Valid(defs[1].InputSchema))
		defs[1].InputSchema[0] = '!'
	}
	require.Empty(t, registry.ToolsFor(RoleStudent))
	_, err := registry.Call(context.Background(), Caller{Role: RoleStudent}, "z.last", json.RawMessage(`{"reason":"test"}`))
	require.ErrorIs(t, err, ErrScopeDenied)
	_, err = registry.Call(context.Background(), systemA, "z.last", json.RawMessage(`{}`))
	require.Error(t, err)
	_, err = registry.Call(context.Background(), systemA, "z.last", json.RawMessage(`{"reason":"test"}`))
	require.NoError(t, err)
}
