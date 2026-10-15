package api

import (
	"bufio"
	"context"
	"enwords/internal/obs"
	"enwords/internal/store"
	"fmt"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBrowserOverdueHarness exists only in the compiled Go test binary.
// It adds no product endpoint, timer configuration or fake WebSocket behavior.
func TestBrowserOverdueHarness(t *testing.T) {
	if os.Getenv("ENWORDS_BROWSER_HARNESS") != "1" {
		t.Skip("browser-only harness: set ENWORDS_BROWSER_HARNESS=1 for Playwright lifecycle acceptance")
	}
	path := os.Getenv("ENWORDS_BROWSER_DB")
	require.NotEmpty(t, path)
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root, err := filepath.Abs(filepath.Join(filepath.Dir(source), "../../..", ".test-artifacts"))
	require.NoError(t, err)
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	rel, err := filepath.Rel(root, abs)
	require.NoError(t, err)
	require.False(t, filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)), "harness DB must be isolated under workspace .test-artifacts")
	require.Equal(t, "18081", os.Getenv("ENWORDS_BROWSER_PORT"))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0755))
	db, err := store.Open(abs)
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	defer sql.Close()
	app := NewApp(nil, db)
	require.NoError(t, app.StartupError)
	app.SetTestMode()
	app.SetLogger(obs.New(os.Stdout))
	// Installed before serving: callback registration is never mutated while
	// HTTP requests run. Count only selected completed-room snapshot keys.
	var observeMu sync.Mutex
	observedKeys := map[string]bool{}
	observedWrites := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("browser_finished_snapshot_count", func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*store.RuntimeRecord)
		if !ok {
			return
		}
		observeMu.Lock()
		defer observeMu.Unlock()
		if observedKeys[row.Key] {
			observedWrites++
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timerDone := make(chan struct{})
	go func() { defer close(timerDone); app.Rooms.RunTimersContext(ctx) }()
	listener, err := net.Listen("tcp", "127.0.0.1:18081")
	require.NoError(t, err)
	server := &http.Server{Handler: app.Router(), ReadHeaderTimeout: 10 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() {
		cancel()
		_ = server.Close()
		app.Rooms.mu.Lock()
		for _, room := range app.Rooms.rooms {
			room.mu.Lock()
			room.retired = true
			room.cancelJudgmentLocked()
			for client := range room.clients {
				if closer, ok := client.(interface{ Close() error }); ok {
					_ = closer.Close()
				}
			}
			room.mu.Unlock()
		}
		app.Rooms.mu.Unlock()
		<-timerDone
		err := <-serveDone
		require.ErrorIs(t, err, http.ErrServerClosed)
	}()
	fmt.Println("BROWSER_OVERDUE_HARNESS_READY")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		command := strings.TrimSpace(scanner.Text())
		if command == "timeout-finished" {
			app.Rooms.mu.Lock()
			selected := []*Room{}
			keys := map[string]bool{}
			for _, room := range app.Rooms.rooms {
				room.mu.Lock()
				finished := room.finished
				room.mu.Unlock()
				if finished {
					selected = append(selected, room)
					keys[fmt.Sprintf("snapshot/%d", room.lessonID)] = true
				}
			}
			app.Rooms.mu.Unlock()
			observeMu.Lock()
			observedKeys = keys
			observedWrites = 0
			observeMu.Unlock()
			for _, room := range selected {
				room.SessionTimeout()
			}
			observeMu.Lock()
			writes := observedWrites
			observedKeys = map[string]bool{}
			observeMu.Unlock()
			app.Log.InfoCtx(obs.WithTrace(ctx, "browser-timeout-finished"), "test", "browser_timeout_finished", map[string]any{"completed_rooms": len(selected), "snapshot_write_attempts": writes})
			fmt.Printf("BROWSER_TIMEOUT_FINISHED_DONE rooms=%d writes=%d\n", len(selected), writes)
			continue
		}
		require.Equal(t, "overdue", command, "only overdue/timeout-finished commands are supported")
		err := app.RunOverdue(ctx)
		if err != nil {
			fmt.Println("BROWSER_OVERDUE_ERROR", err)
		} else {
			fmt.Println("BROWSER_OVERDUE_DONE")
		}
	}
	require.NoError(t, scanner.Err())
}
