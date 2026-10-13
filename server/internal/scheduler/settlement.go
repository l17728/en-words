package scheduler

import "time"

type settlementPlan struct {
	cards  []*Card
	lesson *Lesson
}

// SettleLesson is explicitly keyed by lesson, rather than guessing a scheduled lesson.
// A durable caller wraps this and the report/next-course writes in one transaction.
func (s *Service) SettleLesson(id int64, correct map[int64]bool, at time.Time) error {
	s.settlementMu.Lock()
	defer s.settlementMu.Unlock()
	l, err := s.repo.GetLesson(id)
	if err != nil {
		return err
	}
	if l.Status == LessonFinishedStr {
		return nil
	}
	if s.pendingSettlements == nil {
		s.pendingSettlements = map[int64]*settlementPlan{}
	}
	plan := s.pendingSettlements[id]
	if plan == nil {
		plan = &settlementPlan{}
		ladder := s.cfg.Ladder(l.StudentID)
		for _, wid := range l.BatchWordIDs {
			c, err := s.repo.GetCard(l.StudentID, l.BookID, wid)
			if err != nil {
				return err
			}
			// Copy: in-memory repos must not observe a mutation before SaveCard succeeds.
			cp := *c
			c = &cp
			wasNew := c.Status == StatusNew
			if wasNew {
				c.Status = StatusLearning
			}
			c.LastReviewAt = &at
			c.BoostFlag = false
			if correct[wid] {
				if wasNew {
					c.LadderStep = 0
				} else {
					c.LadderStep++
				}
				if c.LadderStep >= len(ladder) {
					c.Status = StatusGraduated
					c.NextDueAt = nil
				} else {
					due := at.Add(time.Duration(interval(ladder, c.LadderStep)) * 24 * time.Hour)
					c.NextDueAt = &due
				}
			} else {
				due := at.Add(time.Duration(interval(ladder, c.LadderStep)) * 24 * time.Hour)
				c.NextDueAt = &due
			}
			plan.cards = append(plan.cards, c)
		}
		cp := *l
		cp.Status = LessonFinishedStr
		cp.FinishedAt = &at
		plan.lesson = &cp
		// Freeze absolute targets before the first write. An in-process retry after
		// a partial repository failure must not increment an already saved card.
		s.pendingSettlements[id] = plan
	}
	for _, c := range plan.cards {
		if err := s.repo.SaveCard(c); err != nil {
			return err
		}
	}
	if err := s.repo.SaveLesson(plan.lesson); err != nil {
		return err
	}
	s.rememberFinished(id, *plan.lesson.FinishedAt)
	delete(s.pendingSettlements, id)
	return nil
}

func (s *Service) finishedTime(id int64) (time.Time, bool) {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	t, ok := s.finishedAt[id]
	return t, ok
}
func (s *Service) rememberFinished(id int64, at time.Time) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	s.finishedAt[id] = at
}
