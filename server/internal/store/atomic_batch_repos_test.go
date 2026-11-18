package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"enwords/internal/scheduler"
	"enwords/internal/wordbook"
	"github.com/stretchr/testify/require"
)

func TestImportEntriesAtomicPreservesOrderAndSkipsDuplicates(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewWordbookRepo(db)
	book := &wordbook.Book{ID: 101, Name: "atomic import", Grade: "g7", Status: wordbook.StatusDraft}
	require.NoError(t, repo.CreateBook(book))
	result, err := repo.ImportEntriesAtomic(context.Background(), book.ID, []wordbook.EntryInput{
		{Word: "apple", Meanings: []string{"n. 苹果"}},
		{Word: "Apple", Meanings: []string{"duplicate"}},
		{Word: "", Meanings: []string{"invalid"}},
		{Word: "blue-green", Meanings: []string{"adj. 蓝绿色"}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Inserted)
	require.Equal(t, []string{"Apple", ""}, result.Skipped)
	entries, _, err := repo.ListEntries(book.ID, 1, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "apple", entries[0].Word)
	require.Equal(t, "blue-green", entries[1].Word)
	var normKeys []string
	require.NoError(t, db.Model(&WordEntry{}).Where("book_id = ?", book.ID).Order("id").Pluck("norm_key", &normKeys).Error)
	require.Equal(t, []string{"apple", "bluegreen"}, normKeys)
	_, err = repo.ImportEntriesAtomic(context.Background(), 9999, nil)
	require.ErrorIs(t, err, wordbook.ErrBookNotFound)
}

func TestImportEntriesAtomicCancellationRollsBack(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewWordbookRepo(db)
	book := &wordbook.Book{ID: 102, Name: "cancel import", Grade: "g7", Status: wordbook.StatusDraft}
	require.NoError(t, repo.CreateBook(book))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repo.ImportEntriesAtomic(ctx, book.ID, []wordbook.EntryInput{{Word: "cat", Meanings: []string{"猫"}}})
	require.ErrorIs(t, err, context.Canceled)
	count, err := repo.CountEntries(book.ID)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestImportEntriesAtomicRollsBackEarlierBatchOnLaterBatchFailure(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewWordbookRepo(db)
	book := &wordbook.Book{ID: 103, Name: "batch rollback", Grade: "g7", Status: wordbook.StatusDraft}
	require.NoError(t, repo.CreateBook(book))
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_atomic_batch_word BEFORE INSERT ON word_entries
		WHEN NEW.word = 'force-fail' BEGIN SELECT RAISE(ABORT, 'forced batch failure'); END`).Error)

	entries := make([]wordbook.EntryInput, 101)
	for i := range entries {
		entries[i] = wordbook.EntryInput{Word: fmt.Sprintf("atomic-%03d", i), Meanings: []string{"meaning"}}
	}
	entries[100].Word = "force-fail"
	result, err := repo.ImportEntriesAtomic(context.Background(), book.ID, entries)
	require.Error(t, err)
	require.Nil(t, result)
	count, err := repo.CountEntries(book.ID)
	require.NoError(t, err)
	require.Zero(t, count, "the first 100-row batch must roll back with the rejected second batch")
}

func TestCreateCardsIfAbsentIsIdempotentAndRejectsNil(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewSchedulerRepo(db)
	card := &scheduler.Card{StudentID: 31, BookID: 41, WordID: 51, Status: scheduler.StatusNew}
	require.NoError(t, repo.CreateCardsIfAbsent(context.Background(), []*scheduler.Card{card}))
	require.NoError(t, repo.CreateCardsIfAbsent(context.Background(), []*scheduler.Card{card}))
	require.Error(t, repo.CreateCardsIfAbsent(context.Background(), []*scheduler.Card{nil}))
	newCard := &scheduler.Card{StudentID: 31, BookID: 41, WordID: 52, Status: scheduler.StatusNew}
	require.Error(t, repo.CreateCardsIfAbsent(context.Background(), []*scheduler.Card{newCard, nil}))
	var count int64
	require.NoError(t, db.Model(&MemoryCard{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, repo.CreateCardsIfAbsent(context.Background(), nil))
}

func TestCreateCardsIfAbsentPreservesExistingStateAndUsesCompositeScope(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewSchedulerRepo(db)
	reviewAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dueAt := reviewAt.Add(24 * time.Hour)
	existing := MemoryCard{ID: 700, StudentID: 31, BookID: 41, WordID: 51, Status: string(scheduler.StatusLearning),
		LadderStep: 4, ConsecutiveCorrect: 3, WrongCount: 2, BoostFlag: true, BoostSequence: 9,
		LastReviewAt: &reviewAt, NextDueAt: &dueAt}
	require.NoError(t, db.Create(&existing).Error)

	input := &scheduler.Card{StudentID: 31, BookID: 41, WordID: 51, Status: scheduler.StatusNew}
	newSameBook := &scheduler.Card{StudentID: 31, BookID: 41, WordID: 52, Status: scheduler.StatusNew}
	newOtherStudent := &scheduler.Card{StudentID: 32, BookID: 41, WordID: 51, Status: scheduler.StatusNew}
	newOtherBook := &scheduler.Card{StudentID: 31, BookID: 42, WordID: 51, Status: scheduler.StatusNew}
	cards := []*scheduler.Card{input, newSameBook, newOtherStudent, newOtherBook}
	require.NoError(t, repo.CreateCardsIfAbsent(context.Background(), cards))
	require.NoError(t, repo.CreateCardsIfAbsent(context.Background(), cards))

	var got MemoryCard
	require.NoError(t, db.Where("student_id = ? AND book_id = ? AND word_id = ?", 31, 41, 51).Take(&got).Error)
	require.Equal(t, existing, got, "duplicate creation must never reset learning state")
	var count int64
	require.NoError(t, db.Model(&MemoryCard{}).Count(&count).Error)
	require.EqualValues(t, 4, count, "student and book are both part of the idempotency key")
}

func TestCreateCardsIfAbsentRollsBackEarlierBatchOnLaterBatchFailure(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewSchedulerRepo(db)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_atomic_card BEFORE INSERT ON memory_cards
		WHEN NEW.word_id = 101 BEGIN SELECT RAISE(ABORT, 'forced batch failure'); END`).Error)

	cards := make([]*scheduler.Card, 101)
	for i := range cards {
		cards[i] = &scheduler.Card{StudentID: 31, BookID: 41, WordID: int64(i + 1), Status: scheduler.StatusNew}
	}
	err = repo.CreateCardsIfAbsent(context.Background(), cards)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&MemoryCard{}).Count(&count).Error)
	require.Zero(t, count, "the first 100-card batch must roll back with the rejected second batch")
}

func TestMarkPlacementAtomicAppliesScopedStatusChangesAndDraft(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewSchedulerRepo(db)
	require.NoError(t, db.Create(&MemoryCard{StudentID: 1, BookID: 2, WordID: 10, Status: string(scheduler.StatusNew)}).Error)
	require.NoError(t, db.Create(&MemoryCard{StudentID: 1, BookID: 2, WordID: 11, Status: string(scheduler.StatusMastered)}).Error)
	require.NoError(t, db.Create(&MemoryCard{StudentID: 1, BookID: 3, WordID: 10, Status: string(scheduler.StatusNew)}).Error)
	require.NoError(t, db.Create(&MemoryCard{StudentID: 9, BookID: 2, WordID: 10, Status: string(scheduler.StatusNew)}).Error)
	require.NoError(t, db.Create(&MemoryCard{StudentID: 1, BookID: 2, WordID: 14, Status: string(scheduler.StatusLearning), LadderStep: 3, BoostFlag: true, BoostSequence: 8}).Error)
	require.NoError(t, db.Create(&MemoryCard{StudentID: 1, BookID: 2, WordID: 15, Status: string(scheduler.StatusGraduated), LadderStep: 7}).Error)
	changed, err := repo.MarkPlacementAtomic(context.Background(), 1, 2, []int64{10})
	require.NoError(t, err)
	require.Equal(t, 2, changed)
	assertCardStatus := func(studentID, bookID, wordID int64, want scheduler.Status) MemoryCard {
		var card MemoryCard
		require.NoError(t, db.Where("student_id = ? AND book_id = ? AND word_id = ?", studentID, bookID, wordID).Take(&card).Error)
		require.Equal(t, string(want), card.Status)
		return card
	}
	assertCardStatus(1, 2, 10, scheduler.StatusMastered)
	assertCardStatus(1, 2, 11, scheduler.StatusNew)
	assertCardStatus(1, 3, 10, scheduler.StatusNew)
	assertCardStatus(9, 2, 10, scheduler.StatusNew)
	learning := assertCardStatus(1, 2, 14, scheduler.StatusLearning)
	require.Equal(t, 3, learning.LadderStep)
	require.True(t, learning.BoostFlag)
	require.Equal(t, int64(8), learning.BoostSequence)
	graduated := assertCardStatus(1, 2, 15, scheduler.StatusGraduated)
	require.Equal(t, 7, graduated.LadderStep)
	var draft PlacementDraft
	require.NoError(t, db.Where("student_id = ? AND book_id = ?", 1, 2).Take(&draft).Error)
	require.JSONEq(t, `[10]`, draft.KnownIDs)
	unchanged, err := repo.MarkPlacementAtomic(context.Background(), 1, 2, []int64{10, 14, 15})
	require.NoError(t, err)
	require.Zero(t, unchanged, "learning/graduated cards are protected even when selected as known")
	learning = assertCardStatus(1, 2, 14, scheduler.StatusLearning)
	require.Equal(t, 3, learning.LadderStep)
	require.True(t, learning.BoostFlag)
	require.Equal(t, int64(8), learning.BoostSequence)
	graduated = assertCardStatus(1, 2, 15, scheduler.StatusGraduated)
	require.Equal(t, 7, graduated.LadderStep)
	require.NoError(t, db.Where("student_id = ? AND book_id = ?", 1, 2).Take(&draft).Error)
	require.JSONEq(t, `[10,14,15]`, draft.KnownIDs)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repo.MarkPlacementAtomic(canceled, 1, 2, nil)
	require.ErrorIs(t, err, context.Canceled)
	assertCardStatus(1, 2, 10, scheduler.StatusMastered)
	assertCardStatus(1, 2, 11, scheduler.StatusNew)
	assertCardStatus(1, 3, 10, scheduler.StatusNew)
	assertCardStatus(9, 2, 10, scheduler.StatusNew)
	assertCardStatus(1, 2, 14, scheduler.StatusLearning)
	assertCardStatus(1, 2, 15, scheduler.StatusGraduated)
	require.NoError(t, db.Where("student_id = ? AND book_id = ?", 1, 2).Take(&draft).Error)
	require.JSONEq(t, `[10,14,15]`, draft.KnownIDs)
}

func TestMarkPlacementAtomicRollsBackCardChangesWhenDraftWriteFails(t *testing.T) {
	db, _ := openTest(t)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	repo := NewSchedulerRepo(db)
	require.NoError(t, db.Create(&MemoryCard{StudentID: 1, BookID: 2, WordID: 10, Status: string(scheduler.StatusNew)}).Error)
	draft := PlacementDraft{StudentID: 1, BookID: 2, KnownIDs: `[99]`}
	require.NoError(t, db.Create(&draft).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_draft_update BEFORE UPDATE ON placement_drafts
		WHEN OLD.student_id = 1 AND OLD.book_id = 2 BEGIN SELECT RAISE(ABORT, 'forced draft failure'); END`).Error)

	changed, err := repo.MarkPlacementAtomic(context.Background(), 1, 2, []int64{10})
	require.Error(t, err)
	require.Zero(t, changed)
	var card MemoryCard
	require.NoError(t, db.Where("student_id = ? AND book_id = ? AND word_id = ?", 1, 2, 10).Take(&card).Error)
	require.Equal(t, string(scheduler.StatusNew), card.Status)
	var gotDraft PlacementDraft
	require.NoError(t, db.Where("student_id = ? AND book_id = ?", 1, 2).Take(&gotDraft).Error)
	require.JSONEq(t, `[99]`, gotDraft.KnownIDs)
}
