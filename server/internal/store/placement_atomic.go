package store

import (
	"context"
	"encoding/json"
	"time"

	"enwords/internal/scheduler"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *SchedulerRepo) MarkPlacementAtomic(ctx context.Context, studentID, bookID int64, known []int64) (int, error) {
	selected := map[int64]bool{}
	for _, id := range known {
		selected[id] = true
	}
	draftJSON, err := json.Marshal(known)
	if err != nil {
		return 0, err
	}
	changed := 0
	err = atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		// Reserve the writer before the scoped read, without changing any row.
		if err := tx.Exec("UPDATE placement_drafts SET student_id = student_id WHERE 1 = 0").Error; err != nil {
			return err
		}
		var cards []MemoryCard
		if err := tx.Where("student_id = ? AND book_id = ?", studentID, bookID).Order("word_id").Find(&cards).Error; err != nil {
			return err
		}
		toMastered, toNew := []int64{}, []int64{}
		for _, card := range cards {
			if card.Status == string(scheduler.StatusNew) && selected[card.WordID] {
				toMastered = append(toMastered, card.WordID)
			}
			if card.Status == string(scheduler.StatusMastered) && !selected[card.WordID] {
				toNew = append(toNew, card.WordID)
			}
		}
		update := func(ids []int64, before, after scheduler.Status) error {
			for start := 0; start < len(ids); start += 100 {
				end := start + 100
				if end > len(ids) {
					end = len(ids)
				}
				result := tx.Model(&MemoryCard{}).Where("student_id = ? AND book_id = ? AND status = ? AND word_id IN ?", studentID, bookID, string(before), ids[start:end]).UpdateColumn("status", string(after))
				if result.Error != nil {
					return result.Error
				}
				changed += int(result.RowsAffected)
			}
			return nil
		}
		if err := update(toMastered, scheduler.StatusNew, scheduler.StatusMastered); err != nil {
			return err
		}
		if err := update(toNew, scheduler.StatusMastered, scheduler.StatusNew); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "student_id"}, {Name: "book_id"}}, DoUpdates: clause.AssignmentColumns([]string{"known_ids", "updated_at"})}).Create(&PlacementDraft{StudentID: studentID, BookID: bookID, KnownIDs: string(draftJSON), UpdatedAt: time.Now()}).Error
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}
