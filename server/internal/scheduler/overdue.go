package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrOverdueConflict = errors.New("overdue schedule conflict")
var ErrOverdueStale = errors.New("overdue stale target")

// AllLessons propagates read failures; a failed scan is never an empty scan.
func (s *Service) AllLessons() ([]*Lesson, error) {
	if repo, ok := s.repo.(allLessoner); ok {
		lessons, err := repo.ListLessonsAll()
		if err != nil {
			return nil, err
		}
		out := make([]*Lesson, 0, len(lessons))
		for _, lesson := range lessons {
			if lesson == nil {
				out = append(out, nil)
				continue
			}
			copy := *lesson
			copy.BatchWordIDs = append([]int64(nil), lesson.BatchWordIDs...)
			if lesson.FinishedAt != nil {
				at := *lesson.FinishedAt
				copy.FinishedAt = &at
			}
			out = append(out, &copy)
		}
		return out, nil
	}
	if repo, ok := s.repo.(*MemRepo); ok {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		out := make([]*Lesson, 0, len(repo.lessons))
		for _, l := range repo.lessons {
			v := *l
			v.BatchWordIDs = append([]int64(nil), l.BatchWordIDs...)
			if l.FinishedAt != nil {
				at := *l.FinishedAt
				v.FinishedAt = &at
			}
			out = append(out, &v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	return nil, fmt.Errorf("all lessons query unavailable")
}

// ReplanExpired applies an Agent-approved exact target inside the caller's
// transaction. It never advances review history and never chooses a date.
func (s *Service) ReplanExpired(id int64, expected, target time.Time) (*Lesson, error) {
	l, err := s.repo.GetLesson(id)
	if err != nil {
		return nil, err
	}
	if l.Status != LessonScheduledStr || l.FinishedAt != nil || !l.OpenAt.Equal(expected) {
		return nil, ErrOverdueStale
	}
	if !target.After(s.clock.Now()) {
		return nil, fmt.Errorf("overdue target must be future")
	}
	rows, err := s.repo.ListLessons(l.StudentID, l.BookID)
	if err != nil {
		return nil, err
	}
	for _, other := range rows {
		if other.ID != l.ID && other.Status == LessonScheduledStr && other.FinishedAt == nil {
			return nil, ErrOverdueConflict
		}
	}
	l.Status = LessonExpiredStr
	if err := s.repo.SaveLesson(l); err != nil {
		return nil, err
	}
	next := &Lesson{StudentID: l.StudentID, BookID: l.BookID, Status: LessonScheduledStr, OpenAt: target, BatchWordIDs: append([]int64(nil), l.BatchWordIDs...)}
	if err := s.repo.CreateLesson(next); err != nil {
		return nil, err
	}
	if err := s.reserveCards(l.StudentID, l.BookID, next.BatchWordIDs, target); err != nil {
		return nil, err
	}
	return next, nil
}
