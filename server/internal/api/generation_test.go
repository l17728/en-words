package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/obs"
	"github.com/gin-gonic/gin"
)

func testGenerationContext(t *testing.T, app *App, lessonID int64) (*generationLease, context.Context) {
	t.Helper()
	lease, err := app.generations.acquire(context.Background(), lessonID)
	if err != nil {
		t.Fatal(err)
	}
	return lease, withGenerationLease(lease.Context(), lease)
}

func TestGenerationRetirementFencesEffectsAndDrainsLeases(t *testing.T) {
	c := newGenerationCoordinator()
	lease, err := c.acquire(context.Background(), 71)
	if err != nil {
		t.Fatal(err)
	}
	old := lease.Token()
	effect, err := old.Permit()
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	remove, err := lease.AddCloser(func() { close(closed) })
	if err != nil {
		t.Fatal(err)
	}
	retirement, err := c.retire(71)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("retirement did not cancel the operation lease")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("retirement did not close the registered connection")
	}
	if _, err := old.Permit(); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("retired token admitted an effect: %v", err)
	}
	if err := retirement.Wait(shortTestContext(t)); err == nil {
		t.Fatal("retirement drained while an admitted lease/effect remained")
	}
	effect.Release()
	lease.Release()
	lease.Release()
	remove()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := retirement.Wait(ctx); err != nil {
		t.Fatalf("retirement failed to drain: %v", err)
	}
	if err := retirement.Advance(); err != nil {
		t.Fatalf("advance after drain failed: %v", err)
	}
	next, err := c.acquire(context.Background(), 71)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Release()
	if next.Token().id == old.id {
		t.Fatal("generation identifier was reused")
	}
	if _, err := old.Permit(); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("old generation became valid after advance: %v", err)
	}
}

func TestGenerationDrainDeadlineFailsClosed(t *testing.T) {
	c := newGenerationCoordinator()
	lease, err := c.acquire(context.Background(), 72)
	if err != nil {
		t.Fatal(err)
	}
	retirement, err := c.retire(72)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := retirement.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded drain timeout, got %v", err)
	}
	retirement.Fail()
	lease.Release()
	if _, err := c.acquire(context.Background(), 72); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("failed generation opened another sample: %v", err)
	}
	if _, err := retirement.token.Permit(); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("failed generation admitted a write: %v", err)
	}
}

func TestGenerationRetirementWaitHonorsContext(t *testing.T) {
	c := newGenerationCoordinator()
	lease, err := c.acquire(context.Background(), 73)
	if err != nil {
		t.Fatal(err)
	}
	retirement, err := c.retire(73)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := retirement.Wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	lease.Release()
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClassroomPersistenceRejectsMissingOrRetiredGeneration(t *testing.T) {
	app := NewApp(nil, nil)
	state := classroom.State{LessonID: 901}
	if app.saveLessonStateContext(context.Background(), 901, state) {
		t.Fatal("snapshot write without a generation lease was accepted")
	}
	if app.lessonStates[901] != nil {
		t.Fatal("rejected snapshot mutated in-memory state")
	}

	lease, err := app.generations.acquire(context.Background(), 901)
	if err != nil {
		t.Fatal(err)
	}
	token := lease.Token()
	retirement, err := app.generations.retire(901)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := retirement.Advance(); err != nil {
		t.Fatal(err)
	}
	if app.saveLessonStateWithToken(context.Background(), 901, state, token) {
		t.Fatal("snapshot write with retired generation token was accepted")
	}
	if app.lessonStates[901] != nil {
		t.Fatal("retired snapshot mutated in-memory state")
	}
	if err := app.commitLessonWithGeneration(context.Background(), token, 901, 1, 1, nil, state); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("settlement with retired generation token was accepted: %v", err)
	}
}

type blockingClassroomResponseWriter struct {
	gin.ResponseWriter
	entered chan struct{}
	release chan struct{}
}

func (w *blockingClassroomResponseWriter) Write(body []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return w.ResponseWriter.Write(body)
}

func TestClassroomRESTResponsePermitDrainsBeforeGenerationAdvance(t *testing.T) {
	app := NewApp(nil, nil)
	lease, ctx := testGenerationContext(t, app, 902)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest("POST", "/api/v1/classroom/lessons/902/event", nil).WithContext(ctx)
	writer := &blockingClassroomResponseWriter{ResponseWriter: ginCtx.Writer, entered: make(chan struct{}, 1), release: make(chan struct{})}
	ginCtx.Writer = writer
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		app.writeClassroomResponse(ginCtx, 902, ok(gin.H{"frame": "old-generation-frame"}))
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("REST response did not reach its write barrier")
	}

	retirement, err := app.generations.retire(902)
	if err != nil {
		t.Fatal(err)
	}
	if err := retirement.Wait(shortTestContext(t)); err == nil {
		t.Fatal("generation advanced while an old-generation REST response was being written")
	}
	close(writer.release)
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("REST response did not finish after releasing its write barrier")
	}
	lease.Release()
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("generation failed to drain after response completion: %v", err)
	}
	if err := retirement.Advance(); err != nil {
		t.Fatalf("generation advance failed after REST response drain: %v", err)
	}
	if !strings.Contains(recorder.Body.String(), "old-generation-frame") {
		t.Fatalf("admitted response did not finish writing: %s", recorder.Body.String())
	}
}

func TestClassroomRESTResponseRejectsRetiredGeneration(t *testing.T) {
	app := NewApp(nil, nil)
	lease, ctx := testGenerationContext(t, app, 903)
	retirement, err := app.generations.retire(903)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest("POST", "/api/v1/classroom/lessons/903/event", nil).WithContext(ctx)
	app.writeClassroomResponse(ginCtx, 903, ok(gin.H{"frame": "stale-frame-must-not-escape"}))
	lease.Release()
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recorder.Body.String(), "stale-frame-must-not-escape") {
		t.Fatalf("retired response leaked stale classroom content: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "classroom generation retired") {
		t.Fatalf("expected retired-generation response, got %s", recorder.Body.String())
	}
}

func TestGenerationCoordinatorInvalidInputsAndRetirementEdges(t *testing.T) {
	c := newGenerationCoordinator()
	if _, err := c.acquire(nil, 0); !errors.Is(err, errLessonUnavailable) {
		t.Fatalf("acquire with invalid lesson id: %v", err)
	}
	if _, err := c.acquireFor(context.Background(), generationToken{}); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("acquireFor with empty token: %v", err)
	}
	other := newGenerationCoordinator()
	foreignLease, err := other.acquire(context.Background(), 904)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.acquireFor(context.Background(), foreignLease.Token()); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("acquireFor with foreign owner: %v", err)
	}
	foreignLease.Release()
	if _, err := (generationToken{}).Permit(); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("permit with empty token: %v", err)
	}
	(*generationEffect)(nil).Release()
	(*generationLease)(nil).Release()
	(*generationRetirement)(nil).Fail()
	if err := (*generationRetirement)(nil).Wait(context.Background()); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("wait with nil retirement: %v", err)
	}
	if err := (*generationRetirement)(nil).Advance(); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("advance with nil retirement: %v", err)
	}
	if _, err := c.retire(999); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("retire missing lesson: %v", err)
	}

	lease, err := c.acquire(context.Background(), 904)
	if err != nil {
		t.Fatal(err)
	}
	effect, err := lease.Token().Permit()
	if err != nil {
		t.Fatal(err)
	}
	effect.Release()
	effect.Release()
	if _, err := lease.AddCloser(nil); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("nil closer: %v", err)
	}
	if _, err := lease.AddCloser(func() {}); err != nil {
		t.Fatal(err)
	}
	removed, err := lease.AddCloser(func() {})
	if err != nil {
		t.Fatal(err)
	}
	removed()
	removed()

	callbackCheckedUnlocked := false
	if _, err := lease.AddCloser(func() {
		callbackCheckedUnlocked = c.mu.TryLock()
		if callbackCheckedUnlocked {
			c.mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	retirement, err := c.retire(904)
	if err != nil {
		t.Fatal(err)
	}
	if !callbackCheckedUnlocked {
		t.Fatal("retirement callback ran while coordinator lock was held")
	}
	duplicate, err := c.retire(904)
	if err != nil || duplicate.token.id != retirement.token.id {
		t.Fatalf("duplicate retire did not return current retirement: retirement=%v err=%v", duplicate, err)
	}
	if _, err := lease.AddCloser(func() {}); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("closer admitted after retirement: %v", err)
	}
	if err := retirement.Advance(); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("advance before drain: %v", err)
	}
	lease.Release()
	if err := retirement.Wait(nil); err != nil {
		t.Fatalf("wait with nil context after drain: %v", err)
	}
	if err := retirement.Advance(); err != nil {
		t.Fatalf("advance after drain: %v", err)
	}
	if err := retirement.Advance(); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("second advance should reject stale retirement: %v", err)
	}
}

func TestGenerationAdvanceAndFailClosedBranches(t *testing.T) {
	c := newGenerationCoordinator()
	lease, err := c.acquire(context.Background(), 905)
	if err != nil {
		t.Fatal(err)
	}
	retirement, err := c.retire(905)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	retirement.Fail()
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("fail-closed without active operations must drain: %v", err)
	}
	if _, err := c.acquire(context.Background(), 905); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("failed lesson admitted a new lease: %v", err)
	}
	if err := retirement.Advance(); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("failed retirement advanced: %v", err)
	}
	if _, err := retirement.token.Permit(); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("failed retirement admitted an effect: %v", err)
	}

	other := newGenerationCoordinator()
	lease2, err := other.acquire(context.Background(), 906)
	if err != nil {
		t.Fatal(err)
	}
	retirement2, err := other.retire(906)
	if err != nil {
		t.Fatal(err)
	}
	lease2.Release()
	retirement2.token.id++
	if err := retirement2.Advance(); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("advance with mismatched token: %v", err)
	}

	withoutState := &generationRetirement{owner: newGenerationCoordinator(), token: generationToken{lesson: 907}}
	withoutState.Fail()
	if _, err := withoutState.owner.acquire(context.Background(), 907); !errors.Is(err, errGenerationFailed) {
		t.Fatalf("fail without token state did not close lesson: %v", err)
	}
}

func TestRoomOperationLeaseValidatesContextAndOwnership(t *testing.T) {
	if _, owned, err := (*Room)(nil).operationLease(context.Background()); err == nil || owned {
		t.Fatalf("nil room admitted an operation: owned=%t err=%v", owned, err)
	}
	app := NewApp(nil, nil)
	lease, err := app.generations.acquire(context.Background(), 908)
	if err != nil {
		t.Fatal(err)
	}
	room := &Room{app: app, lessonID: 908, generation: lease.Token()}
	valid, owned, err := room.operationLease(withGenerationLease(context.Background(), lease))
	if err != nil || owned || valid != lease {
		t.Fatalf("matching context lease: lease=%p owned=%t err=%v", valid, owned, err)
	}
	wrongLessonCtx := withGenerationLease(context.Background(), lease)
	otherLessonLease, err := app.generations.acquire(context.Background(), 909)
	if err != nil {
		t.Fatal(err)
	}
	wrongLessonRoom := &Room{app: app, lessonID: 909, generation: otherLessonLease.Token()}
	implicit, owned, err := wrongLessonRoom.operationLease(wrongLessonCtx)
	if err != nil || !owned || implicit == nil {
		t.Fatalf("context with another lesson should acquire this room's generation: lease=%p owned=%t err=%v", implicit, owned, err)
	}
	implicit.Release()
	otherLessonLease.Release()
	canceled, cancel := context.WithCancel(withGenerationLease(context.Background(), lease))
	cancel()
	if _, owned, err := room.operationLease(canceled); !errors.Is(err, context.Canceled) || owned {
		t.Fatalf("canceled context lease: owned=%t err=%v", owned, err)
	}
	lease.Release()
	retirement, err := app.generations.retire(room.lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("room generation did not drain: %v", err)
	}
	if _, owned, err := room.operationLease(context.Background()); !errors.Is(err, errGenerationRetired) || owned {
		t.Fatalf("retired room admitted implicit lease: owned=%t err=%v", owned, err)
	}
}

type cancelOnSecondErrContext struct {
	context.Context
	calls atomic.Int32
	done  chan struct{}
	once  sync.Once
}

func (c *cancelOnSecondErrContext) Err() error {
	if c.calls.Add(1) >= 2 {
		c.once.Do(func() { close(c.done) })
		return context.Canceled
	}
	return c.Context.Err()
}

func (c *cancelOnSecondErrContext) Done() <-chan struct{} { return c.done }

func TestClassroomResponseRejectedLogsTraceForEveryBranch(t *testing.T) {
	tests := []struct {
		name       string
		lessonID   int64
		prepare    func(*testing.T, *App) (*generationLease, context.Context)
		wantReason string
	}{
		{
			name:     "missing lease",
			lessonID: 910,
			prepare: func(_ *testing.T, _ *App) (*generationLease, context.Context) {
				return nil, obs.WithTrace(context.Background(), "trace-response-missing")
			},
			wantReason: "missing_or_canceled_lease",
		},
		{
			name:     "canceled lease",
			lessonID: 911,
			prepare: func(t *testing.T, app *App) (*generationLease, context.Context) {
				lease, err := app.generations.acquire(context.Background(), 911)
				if err != nil {
					t.Fatal(err)
				}
				ctx := withGenerationLease(obs.WithTrace(context.Background(), "trace-response-canceled"), lease)
				lease.Release()
				return lease, ctx
			},
			wantReason: "missing_or_canceled_lease",
		},
		{
			name:     "permit denied",
			lessonID: 912,
			prepare: func(t *testing.T, app *App) (*generationLease, context.Context) {
				lease, err := app.generations.acquire(context.Background(), 912)
				if err != nil {
					t.Fatal(err)
				}
				lease.token.state.state.Or(generationFailedBit)
				return lease, withGenerationLease(obs.WithTrace(context.Background(), "trace-response-permit"), lease)
			},
			wantReason: errGenerationFailed.Error(),
		},
		{
			name:     "canceled after permit",
			lessonID: 913,
			prepare: func(t *testing.T, app *App) (*generationLease, context.Context) {
				lease, err := app.generations.acquire(context.Background(), 913)
				if err != nil {
					t.Fatal(err)
				}
				lease.ctx = &cancelOnSecondErrContext{Context: lease.ctx, done: make(chan struct{})}
				return lease, withGenerationLease(obs.WithTrace(context.Background(), "trace-response-late-cancel"), lease)
			},
			wantReason: "lease_canceled_before_write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := NewApp(nil, nil)
			var logBuffer bytes.Buffer
			app.Log = obs.New(&logBuffer)
			lease, requestCtx := tt.prepare(t, app)
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			ginCtx.Request = httptest.NewRequest("POST", "/api/v1/classroom/response", nil).WithContext(requestCtx)
			app.writeClassroomResponse(ginCtx, tt.lessonID, ok(gin.H{"frame": "sensitive-old-frame"}))
			if lease != nil {
				lease.Release()
			}
			var event map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logBuffer.Bytes()), &event); err != nil {
				t.Fatalf("decode structured rejection log: %v; output=%q", err, logBuffer.String())
			}
			if event["trace_id"] != obs.TraceID(requestCtx) {
				t.Fatalf("trace_id not preserved: log=%#v expected=%q", event["trace_id"], obs.TraceID(requestCtx))
			}
			if event["module"] != "classroom" || event["event"] != "generation_response_rejected" || event["reason"] != tt.wantReason {
				t.Fatalf("unexpected rejection log fields: %#v", event)
			}
			if strings.Contains(logBuffer.String(), "sensitive-old-frame") || strings.Contains(recorder.Body.String(), "sensitive-old-frame") {
				t.Fatalf("rejection log or response leaked classroom payload: log=%q body=%q", logBuffer.String(), recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "classroom generation retired") {
				t.Fatalf("expected generic retired response, got %s", recorder.Body.String())
			}
		})
	}
}

func shortTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}
