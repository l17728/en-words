package scheduler

import (
	"errors"
	"math"
	"sort"
)

var (
	ErrInvalidPlanPolicy = errors.New("scheduler: invalid new-word ratio")
	ErrInvalidLessonSize = errors.New("scheduler: lesson size must be between 50 and 100")
)

// PlanPolicy is a one-call batch limit. Zero preserves the default planner;
// finite values in (0, 1] cap all new cards, including boosted new cards.
type PlanPolicy struct {
	MaxNewRatio float64
}

func (p PlanPolicy) validate() error {
	if math.IsNaN(p.MaxNewRatio) || math.IsInf(p.MaxNewRatio, 0) || p.MaxNewRatio < 0 || p.MaxNewRatio > 1 {
		return ErrInvalidPlanPolicy
	}
	return nil
}

func firstTeachingBatch(cards []*Card, lessons []*Lesson) bool {
	if len(lessons) != 0 {
		return false
	}
	for _, c := range cards {
		if c.Status == StatusLearning || c.Status == StatusGraduated || c.LastReviewAt != nil || c.LadderStep > 0 {
			return false
		}
	}
	return true
}

func (p PlanPolicy) batch(size int, due, boosted, fresh, cards []*Card, lessons []*Lesson) []int64 {
	limit := int(math.Floor(float64(size) * p.MaxNewRatio))
	if firstTeachingBatch(cards, lessons) {
		limit = size
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].NextDueAt.Equal(*due[j].NextDueAt) {
			return due[i].WordID < due[j].WordID
		}
		return due[i].NextDueAt.Before(*due[j].NextDueAt)
	})
	sort.Slice(boosted, func(i, j int) bool { return boosted[i].WordID < boosted[j].WordID })
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].WordID < fresh[j].WordID })
	batch := make([]int64, 0, size)
	newCount := 0
	for _, group := range [][]*Card{due, boosted, fresh} {
		for _, c := range group {
			if len(batch) == size {
				return batch
			}
			if c.Status == StatusNew {
				if newCount >= limit {
					continue
				}
				newCount++
			}
			batch = append(batch, c.WordID)
		}
	}
	return batch
}
