//go:build interactionbaseline

package api

// Opt-in measurements, not performance assertions or production endpoints.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func baselineRecord(t *testing.T, kind string, data any) {
	b, err := json.Marshal(map[string]any{"kind": kind, "data": data})
	require.NoError(t, err)
	t.Log("BASELINE " + string(b))
}
func baselineDB(t *testing.T) *gorm.DB {
	db, err := store.Open(filepath.Join(t.TempDir(), "baseline.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestInteractionBaselineLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	app := NewApp(nil, db)
	app.SetLogger(obs.New(io.Discard))
	pool, _ := db.DB()
	defer pool.Close()
	second, err := store.Open(path)
	require.NoError(t, err)
	secondPool, _ := second.DB()
	defer secondPool.Close()
	for _, mode := range []string{"free", "pool_held", "sqlite_write_held"} {
		for sample := 0; sample < 20; sample++ {
			entered := make(chan struct{})
			require.NoError(t, db.Callback().Create().Before("gorm:begin_transaction").Register("baseline_entered", func(*gorm.DB) {
				select {
				case <-entered:
				default:
					close(entered)
				}
			}))
			var release func()
			if mode == "pool_held" {
				tx := db.Begin()
				require.NoError(t, tx.Error)
				release = func() { _ = tx.Rollback().Error }
			}
			if mode == "sqlite_write_held" {
				tx := second.Begin()
				require.NoError(t, tx.Error)
				require.NoError(t, tx.Exec("INSERT OR REPLACE INTO runtime_records (key,value) VALUES ('baseline_lock','{}')").Error)
				release = func() { _ = tx.Rollback().Error }
			}
			if release != nil {
				defer release()
			}
			start := time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan time.Duration, 1)
			before := pool.Stats()
			go func() { app.saveLessonStateContext(ctx, 1, classroom.State{}); done <- time.Since(start) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("save did not enter create")
			}
			bDone := make(chan time.Duration, 1)
			go func() {
				bStart := time.Now()
				_, _ = app.NotificationsContext(context.Background(), 2)
				bDone <- time.Since(bStart)
			}()
			if release != nil {
				time.Sleep(200 * time.Millisecond)
				cancel()
				time.Sleep(800 * time.Millisecond)
				release()
			} else {
				cancel()
			}
			var a, b time.Duration
			select {
			case a = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("save did not finish")
			}
			select {
			case b = <-bDone:
			case <-time.After(10 * time.Second):
				t.Fatal("notifications did not finish")
			}
			after := pool.Stats()
			require.NoError(t, db.Callback().Create().Remove("baseline_entered"))
			cancelAt, releaseAt := 200, 1000
			if mode == "free" {
				cancelAt, releaseAt = 0, 0
			}
			baselineRecord(t, "lock", map[string]any{"mode": mode, "sample": sample, "save_ms": a.Milliseconds(), "other_student_notifications_ms": b.Milliseconds(), "cancel_at_ms": cancelAt, "release_at_ms": releaseAt, "pool_wait_count": after.WaitCount - before.WaitCount, "pool_wait_ms": (after.WaitDuration - before.WaitDuration).Milliseconds()})
		}
	}
}

func TestInteractionBaselineTimer(t *testing.T) {
	app, clk, _, original := setupRoom(t)
	app.SetLogger(obs.New(io.Discard))
	app.DB = baselineDB(t)
	repo := scheduler.NewMemRepo()
	require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: original.studentID, BookID: original.bookID, WordID: 1}))
	app.Sched = scheduler.NewService(repo, app.Cfg, clk)
	var writes atomic.Int64
	require.NoError(t, app.DB.Callback().Create().Before("gorm:create").Register("baseline_count", func(*gorm.DB) { writes.Add(1) }))
	snapshot := original.machine.Snapshot()
	cfg := *snapshot.Config
	words := []classroom.Word{}
	for _, w := range original.words {
		words = append(words, w)
	}
	for _, n := range []int{0, 100, 1000} {
		rooms := make([]*Room, 0, n+2)
		for i := 0; i < n+2; i++ {
			status := "scheduled"
			if i < n {
				status = "finished"
			}
			lesson := &scheduler.Lesson{BookID: original.bookID, StudentID: original.studentID, Status: status, OpenAt: clk.Now()}
			require.NoError(t, repo.CreateLesson(lesson))
			id := lesson.ID
			m := classroom.NewMachine(id, original.bookID, original.studentID, words, cfg, clk)
			require.NoError(t, m.Start())
			m.DrainActions()
			if i < n {
				s := m.Snapshot()
				s.Phase = classroom.PhaseLessonEnd
				s.Finished = true
				require.NoError(t, m.Restore(s))
			}
			rooms = append(rooms, &Room{app: app, lessonID: id, bookID: original.bookID, studentID: original.studentID, machine: m, words: original.words, clients: map[Client]bool{}, finished: i < n, lastPhase: m.Phase(), phaseSince: clk.Now()})
		}
		for sample := -1; sample < 10; sample++ {
			before := writes.Load()
			start := time.Now()
			for _, r := range rooms {
				r.Tick()
			}
			tick := time.Since(start)
			start = time.Now()
			var activeWait time.Duration
			for i, r := range rooms {
				if i == n {
					activeWait = time.Since(start)
				}
				r.SessionTimeout()
			}
			if sample >= 0 {
				baselineRecord(t, "timer", map[string]any{"completed_rooms": n, "active_rooms": 2, "sample": sample, "tick_us": tick.Microseconds(), "session_scan_ms": time.Since(start).Milliseconds(), "active_first_wait_ms": activeWait.Milliseconds(), "create_or_upsert_calls": writes.Load() - before, "order": "completed_then_active_worst_order"})
			}
		}
	}
}

func TestInteractionBaselineBodies(t *testing.T) {
	app, _ := newTestApp(t)
	app.SetLogger(obs.New(io.Discard))
	srv := httptest.NewUnstartedServer(app.Router())
	srv.Config.ReadHeaderTimeout = 10 * time.Second
	srv.Start()
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	for _, size := range []int{1024, 64 << 10, 1 << 20, 8 << 20} {
		body := `{"username":"baseline-no-user","password":"` + strings.Repeat("x", size) + `"}`
		start := time.Now()
		resp, err := client.Post(srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		baselineRecord(t, "body", map[string]any{"bytes": len(body), "http_status": resp.StatusCode, "elapsed_ms": time.Since(start).Milliseconds(), "response_bytes": len(raw)})
	}
	conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "POST /api/v1/auth/login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1000000\r\n\r\n{\"password\":\"")
	require.NoError(t, err)
	start := time.Now()
	for i := 0; i < 20; i++ {
		_, err = conn.Write([]byte("x"))
		require.NoError(t, err)
		time.Sleep(100 * time.Millisecond)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	buf := make([]byte, 256)
	n, readErr := conn.Read(buf)
	healthy := time.Now()
	resp, err := client.Post(srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"baseline-no-user","password":"small"}`))
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	timedOut := false
	if ne, ok := readErr.(net.Error); ok {
		timedOut = ne.Timeout()
	}
	baselineRecord(t, "slow_body", map[string]any{"window_ms": time.Since(start).Milliseconds(), "response_bytes_before_client_close": n, "read_timed_out": timedOut, "read_other_error": readErr != nil && !timedOut, "other_request_ms": time.Since(healthy).Milliseconds(), "other_http_status": resp.StatusCode})
	conn.Close()
}
