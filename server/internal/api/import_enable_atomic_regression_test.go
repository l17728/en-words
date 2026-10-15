package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/wordbook"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func importAtomicApp(t *testing.T) (*App, *gorm.DB, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "atomic.db"))
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	app := NewApp(nil, db)
	book, err := app.Books.CreateBook("atomic", "g7", "")
	require.NoError(t, err)
	return app, db, book.ID
}

func importAtomicInputs() []wordbook.EntryInput {
	return []wordbook.EntryInput{{Word: "apple", Meanings: []string{"苹果"}}, {Word: "bee", Meanings: []string{"蜜蜂"}}, {Word: "cat", Meanings: []string{"猫"}}}
}

func importAtomicMany(n int) []wordbook.EntryInput {
	inputs := make([]wordbook.EntryInput, n)
	for i := range inputs {
		inputs[i] = wordbook.EntryInput{Word: fmt.Sprintf("sample%04d", i), Meanings: []string{"示例"}}
	}
	return inputs
}

func TestImportStorageFailureRollsBackWholeValidBatch(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	calls := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_second_entry", func(tx *gorm.DB) {
		if tx.Statement.Table == "word_entries" {
			calls++
			if calls == 2 {
				tx.AddError(errors.New("injected storage failure"))
			}
		}
	}))
	result, err := app.Books.ImportBatch(bid, importAtomicMany(101))
	require.Error(t, err, "storage failure must not be reported as a skipped invalid word")
	require.Nil(t, result)
	var count int64
	require.NoError(t, db.Model(&store.WordEntry{}).Where("book_id=?", bid).Count(&count).Error)
	require.Zero(t, count, "successful earlier inserts must rollback")
}

func TestImportBatchUsesOneTransactionAndPreservesInputOrder(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	transactions := map[*sql.Tx]bool{}
	require.NoError(t, db.Callback().Create().After("gorm:begin_transaction").Before("gorm:create").Register("test:entry_transactions", func(tx *gorm.DB) {
		if tx.Statement.Table == "word_entries" {
			if underlying, ok := tx.Statement.ConnPool.(*sql.Tx); ok {
				transactions[underlying] = true
			}
		}
	}))
	result, err := app.Books.ImportBatch(bid, importAtomicInputs())
	require.NoError(t, err)
	require.Equal(t, 3, result.Inserted)
	require.Len(t, transactions, 1, "one atomic import must not commit each row separately")
	var rows []store.WordEntry
	require.NoError(t, db.Where("book_id=?", bid).Order("id").Find(&rows).Error)
	require.Len(t, rows, 3)
	require.Equal(t, []string{"apple", "bee", "cat"}, []string{rows[0].Word, rows[1].Word, rows[2].Word})
}

func TestEnableStorageFailureRollsBackNewCards(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	_, err := app.Books.ImportBatch(bid, importAtomicMany(101))
	require.NoError(t, err)
	require.NoError(t, app.Books.Publish(bid))
	calls := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_second_card", func(tx *gorm.DB) {
		if tx.Statement.Table == "memory_cards" {
			calls++
			if calls == 2 {
				tx.AddError(errors.New("injected storage failure"))
			}
		}
	}))
	err = app.EnableBook(701, bid)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&store.MemoryCard{}).Where("student_id=? AND book_id=?", 701, bid).Count(&count).Error)
	require.Zero(t, count, "enable must not expose a partially initialized book")
}

func TestImportDuplicateInvalidAndCrossBookSemantics(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	inputs := []wordbook.EntryInput{{Word: " apple ", Meanings: []string{" first ", " ", "second", "third"}}, {Word: "APPLE", Meanings: []string{"duplicate"}}, {Word: "", Meanings: []string{"invalid"}}, {Word: "bee"}, {Word: "cat", Meanings: []string{"猫"}}}
	result, err := app.Books.ImportBatch(bid, inputs)
	require.NoError(t, err)
	require.Equal(t, 2, result.Inserted)
	require.Equal(t, []string{"APPLE", "", "bee"}, result.Skipped)
	var rows []store.WordEntry
	require.NoError(t, db.Where("book_id=?", bid).Order("id").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, "apple", rows[0].Word)
	require.JSONEq(t, `["first","second"]`, rows[0].Meanings)
	require.Equal(t, "cat", rows[1].Word)
	other, err := app.Books.CreateBook("other", "g8", "")
	require.NoError(t, err)
	result, err = app.Books.ImportBatch(other.ID, inputs[:1])
	require.NoError(t, err)
	require.Equal(t, 1, result.Inserted)
	result, err = app.Books.ImportBatch(bid, inputs[:1])
	require.NoError(t, err)
	require.Zero(t, result.Inserted)
	book, err := app.Books.GetBook(bid)
	require.NoError(t, err)
	require.EqualValues(t, 2, book.WordCount)
}

func TestImportAndEnableCancellationRollBack(t *testing.T) {
	for _, operation := range []string{"import", "enable"} {
		for _, when := range []string{"before", "after_first_batch"} {
			t.Run(operation+"/"+when, func(t *testing.T) {
				app, db, bid := importAtomicApp(t)
				if operation == "enable" {
					_, err := app.Books.ImportBatch(bid, importAtomicMany(101))
					require.NoError(t, err)
					require.NoError(t, app.Books.Publish(bid))
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if when == "before" {
					cancel()
				} else {
					table := "word_entries"
					if operation == "enable" {
						table = "memory_cards"
					}
					require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:cancel_after_write", func(tx *gorm.DB) {
						if tx.Statement.Table == table {
							cancel()
						}
					}))
				}
				var err error
				if operation == "import" {
					result, e := app.Books.ImportBatchContext(ctx, bid, importAtomicMany(101))
					err = e
					require.Nil(t, result)
				} else {
					err = app.EnableBookContext(ctx, 701, bid)
				}
				require.Error(t, err)
				var count int64
				if operation == "import" {
					require.NoError(t, db.Model(&store.WordEntry{}).Where("book_id=?", bid).Count(&count).Error)
				} else {
					require.NoError(t, db.Model(&store.MemoryCard{}).Where("student_id=? AND book_id=?", 701, bid).Count(&count).Error)
				}
				require.Zero(t, count)
			})
		}
	}
}

func TestEnableAcrossAppsPreservesProgressAndStudentScope(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	_, err := app.Books.ImportBatch(bid, importAtomicMany(101))
	require.NoError(t, err)
	require.Error(t, app.EnableBook(701, bid), "unpublished cannot enable")
	require.NoError(t, app.Books.Publish(bid))
	var entry store.WordEntry
	require.NoError(t, db.Where("book_id=?", bid).Order("id").First(&entry).Error)
	stamp := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := store.MemoryCard{StudentID: 701, BookID: bid, WordID: entry.ID, Status: "mastered", LadderStep: 4, BoostFlag: true, BoostSequence: 123, LastReviewAt: &stamp, NextDueAt: &stamp}
	require.NoError(t, db.Create(&old).Error)
	// Independently opened pools exercise real SQLite writer contention, not only one pool's queue.
	var files []struct{ File string }
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
	require.NotEmpty(t, files)
	secondDB, err := store.Open(files[0].File)
	require.NoError(t, err)
	secondPool, err := secondDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondPool.Close() })
	other := NewApp(nil, secondDB)
	start := make(chan struct{})
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for _, job := range []struct {
		app     *App
		student int64
	}{{app, 701}, {other, 701}, {other, 702}} {
		wg.Add(1)
		go func(a *App, student int64) { defer wg.Done(); <-start; results <- a.EnableBook(student, bid) }(job.app, job.student)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	var kept store.MemoryCard
	require.NoError(t, db.First(&kept, old.ID).Error)
	require.Equal(t, old, kept)
	for _, student := range []int64{701, 702} {
		var n int64
		require.NoError(t, db.Model(&store.MemoryCard{}).Where("student_id=? AND book_id=?", student, bid).Count(&n).Error)
		require.EqualValues(t, 101, n)
	}
}

func TestAtomicSQLiteRejectsNilCardWithoutPartialWrites(t *testing.T) {
	_, db, bid := importAtomicApp(t)
	repo := store.NewSchedulerRepo(db)
	require.Error(t, repo.CreateCardsIfAbsent(context.Background(), []*scheduler.Card{{StudentID: 701, BookID: bid, WordID: 1}, nil}))
	var count int64
	require.NoError(t, db.Model(&store.MemoryCard{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestAtomicImportLookupFailureDoesNotBecomeAbsence(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:fail_keys", func(tx *gorm.DB) {
		if tx.Statement.Table == "word_entries" {
			tx.AddError(errors.New("injected lookup failure"))
		}
	}))
	result, err := app.Books.ImportBatch(bid, importAtomicInputs())
	require.Error(t, err)
	require.Nil(t, result)
	require.NoError(t, db.Callback().Query().Remove("test:fail_keys"))
	var count int64
	require.NoError(t, db.Model(&store.WordEntry{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestAtomicImportAndEnableCommitFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"import", "enable"} {
		t.Run(operation, func(t *testing.T) {
			app, db, bid := importAtomicApp(t)
			if operation == "enable" {
				_, err := app.Books.ImportBatch(bid, importAtomicInputs())
				require.NoError(t, err)
				require.NoError(t, app.Books.Publish(bid))
			}
			require.NoError(t, db.Exec("CREATE TABLE ix14_parent (id INTEGER PRIMARY KEY)").Error)
			require.NoError(t, db.Exec("CREATE TABLE ix14_deferred (parent_id INTEGER REFERENCES ix14_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
			table := "word_entries"
			if operation == "enable" {
				table = "memory_cards"
			}
			require.NoError(t, db.Exec("CREATE TRIGGER ix14_commit_failure AFTER INSERT ON "+table+" BEGIN INSERT INTO ix14_deferred(parent_id) VALUES(999); END").Error)
			insertSucceeded := false
			require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:commit_stage", func(tx *gorm.DB) {
				if tx.Statement.Table == table && tx.Error == nil && tx.RowsAffected > 0 {
					insertSucceeded = true
				}
			}))
			var err error
			if operation == "import" {
				result, e := app.Books.ImportBatch(bid, importAtomicInputs())
				err = e
				require.Nil(t, result)
			} else {
				err = app.EnableBook(701, bid)
			}
			require.True(t, insertSucceeded, "must fail at deferred commit, not INSERT")
			require.Error(t, err)
			var files []struct{ File string }
			require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
			observer, openErr := sql.Open("sqlite", files[0].File)
			require.NoError(t, openErr)
			defer observer.Close()
			var visible int64
			require.NoError(t, observer.QueryRow("SELECT count(*) FROM "+table).Scan(&visible))
			require.Zero(t, visible, "failed commit must not become visible on an independent connection")
			var count int64
			require.NoError(t, db.Table(table).Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, db.Table("ix14_deferred").Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, db.Exec("DROP TRIGGER ix14_commit_failure").Error)
			if operation == "import" {
				result, e := app.Books.ImportBatch(bid, importAtomicInputs())
				require.NoError(t, e)
				require.Equal(t, 3, result.Inserted)
			} else {
				require.NoError(t, app.EnableBook(701, bid))
			}
			require.NoError(t, db.Table(table).Count(&count).Error)
			require.EqualValues(t, 3, count)
			require.NoError(t, observer.QueryRow("SELECT count(*) FROM "+table).Scan(&visible))
			require.EqualValues(t, 3, visible, "next successful operation commits independently")
		})
	}
}
