//go:build interactionbaseline

package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/obs"
	"enwords/internal/store"
	"enwords/internal/wordbook"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Real isolated SQLite/product services; no time assertion or artificial delay.
func TestInteractionImportEnableBaseline(t *testing.T) {
	for _, size := range []int{100, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			db := baselineDB(t)
			app := NewApp(nil, db)
			app.SetLogger(obs.New(io.Discard))
			u, err := app.Auth.CreateUser("baseline-other", "pw12345", "other", auth.RoleStudent)
			require.NoError(t, err)
			require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, u.Username))
			token, _ := loginGet(t, app, u.Username, "pw12345")
			server := httptest.NewServer(app.Router())
			defer server.Close()
			client := &http.Client{Timeout: 10 * time.Second}
			book, err := app.Books.CreateBook("baseline", "g7", "")
			require.NoError(t, err)
			inputs := make([]wordbook.EntryInput, size)
			for i := range inputs {
				inputs[i] = wordbook.EntryInput{Word: fmt.Sprintf("baseline%04d", i), Meanings: []string{"基线词"}}
			}
			var creates, transactions, queries atomic.Int64
			var txMu sync.Mutex
			uniqueTx := map[*sql.Tx]bool{}
			require.NoError(t, db.Callback().Create().After("gorm:begin_transaction").Before("gorm:create").Register("baseline_transaction", func(tx *gorm.DB) {
				creates.Add(1)
				if sqlTx, ok := tx.Statement.ConnPool.(*sql.Tx); ok {
					transactions.Add(1)
					txMu.Lock()
					uniqueTx[sqlTx] = true
					txMu.Unlock()
				}
			}))
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("baseline_query", func(*gorm.DB) { queries.Add(1) }))
			measure := func(name string, operation func() error) {
				creates.Store(0)
				transactions.Store(0)
				queries.Store(0)
				txMu.Lock()
				uniqueTx = map[*sql.Tx]bool{}
				txMu.Unlock()
				pool, _ := db.DB()
				before := pool.Stats()
				done := make(chan struct{})
				samples := make(chan []int64, 1)
				failures := make(chan int, 1)
				go func() {
					var times []int64
					failed := 0
					for {
						select {
						case <-done:
							samples <- times
							failures <- failed
							return
						default:
						}
						req, _ := http.NewRequest("GET", server.URL+"/api/v1/student/books", nil)
						req.Header.Set("Authorization", "Bearer "+token)
						start := time.Now()
						response, e := client.Do(req)
						if e != nil {
							failed++
						} else {
							var body struct {
								Code int               `json:"code"`
								Data []json.RawMessage `json:"data"`
							}
							decodeErr := json.NewDecoder(response.Body).Decode(&body)
							_ = response.Body.Close()
							if response.StatusCode != 200 || decodeErr != nil || body.Code != 0 || body.Data == nil || len(body.Data) != 0 {
								failed++
							}
						}
						times = append(times, time.Since(start).Milliseconds())
						select {
						case <-done:
							samples <- times
							failures <- failed
							return
						case <-time.After(100 * time.Millisecond):
						}
					}
				}()
				start := time.Now()
				err := operation()
				elapsed := time.Since(start)
				close(done)
				times := <-samples
				failed := <-failures
				sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
				var p95, max int64
				if len(times) > 0 {
					max = times[len(times)-1]
					p95 = times[(95*len(times)+99)/100-1]
				}
				after := pool.Stats()
				txMu.Lock()
				txCount := len(uniqueTx)
				txMu.Unlock()
				baselineRecord(t, "import_enable", map[string]any{"size": size, "operation": name, "elapsed_ms": elapsed.Milliseconds(), "creates": creates.Load(), "create_calls_in_transaction": transactions.Load(), "unique_create_transactions": txCount, "queries_including_other_student": queries.Load(), "other_student_samples": len(times), "other_student_failures": failed, "other_student_p95_ms": p95, "other_student_max_ms": max, "pool_wait_count": after.WaitCount - before.WaitCount, "pool_wait_ms": (after.WaitDuration - before.WaitDuration).Milliseconds(), "error": fmt.Sprint(err)})
				require.NoError(t, err)
				require.Zero(t, failed)
			}
			measure("import", func() error {
				result, err := app.Books.ImportBatch(book.ID, inputs)
				if err == nil {
					require.Equal(t, size, result.Inserted)
					require.Empty(t, result.Skipped)
				}
				return err
			})
			require.NoError(t, app.Books.Publish(book.ID))
			measure("enable_first", func() error { return app.EnableBook(500, book.ID) })
			measure("enable_repeat", func() error { return app.EnableBook(500, book.ID) })
		})
	}
}
