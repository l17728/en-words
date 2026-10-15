package store

import (
	"context"
	"errors"
	"fmt"
	"math"

	"enwords/internal/scheduler"
	"enwords/internal/wordbook"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *WordbookRepo) ImportEntriesAtomic(ctx context.Context, bookID int64, ins []wordbook.EntryInput) (*wordbook.ImportResult, error) {
	var result *wordbook.ImportResult
	err := atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		// Acquire the SQLite writer before reading keys/IDs; never call the outer repo in a tx.
		locked := tx.Model(&WordBook{}).Where("id = ?", bookID).UpdateColumn("id", gorm.Expr("id"))
		if locked.Error != nil {
			return locked.Error
		}
		if locked.RowsAffected == 0 {
			return wordbook.ErrBookNotFound
		}
		var existing []string
		if err := tx.Model(&WordEntry{}).Where("book_id = ?", bookID).Pluck("norm_key", &existing).Error; err != nil {
			return err
		}
		keys := map[string]bool{}
		for _, key := range existing {
			keys[key] = true
		}
		entries, res := wordbook.PrepareImport(ins, keys)
		var last int64
		if err := tx.Model(&WordEntry{}).Select("COALESCE(MAX(id),0)").Scan(&last).Error; err != nil {
			return err
		}
		if int64(len(entries)) > math.MaxInt64-last {
			return fmt.Errorf("store: entry ID overflow")
		}
		rows := make([]WordEntry, 0, len(entries))
		for _, e := range entries {
			last++
			rows = append(rows, WordEntry{ID: last, BookID: bookID, Word: e.Word, NormKey: e.NormKey, Phonetic: e.Phonetic, Meanings: ToJSON(e.Meanings), Unit: e.Unit, IsPhrase: e.IsPhrase, FreqRank: e.FreqRank})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 100).Error; err != nil {
				return err
			}
		}
		result = res
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *SchedulerRepo) CreateCardsIfAbsent(ctx context.Context, cards []*scheduler.Card) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		rows := make([]MemoryCard, 0, len(cards))
		for _, c := range cards {
			if c == nil {
				return errors.New("store: nil card")
			}
			rows = append(rows, *cardFrom(c))
		}
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "student_id"}, {Name: "book_id"}, {Name: "word_id"}}, DoNothing: true}).CreateInBatches(rows, 100).Error; err != nil {
				return err
			}
		}
		return ctx.Err()
	})
}
