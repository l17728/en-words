package api

import (
	"bufio"
	"context"
	"enwords/internal/obs"
	"enwords/internal/store"
	"fmt"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		require.Equal(t, "overdue", strings.TrimSpace(scanner.Text()), "only overdue command is supported")
		err := app.RunOverdue(ctx)
		if err != nil {
			fmt.Println("BROWSER_OVERDUE_ERROR", err)
		} else {
			fmt.Println("BROWSER_OVERDUE_DONE")
		}
	}
	require.NoError(t, scanner.Err())
}
