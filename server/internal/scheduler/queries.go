package scheduler

import "sort"

// ProgressSummary is a read-only view: querying never reserves cards or plans lessons.
type ProgressSummary struct {
	Total     int `json:"total"`
	New       int `json:"new"`
	Learning  int `json:"learning"`
	Mastered  int `json:"mastered"`
	Graduated int `json:"graduated"`
	Due       int `json:"due"`
}

func (s *Service) DueWords(studentID, bookID int64) ([]int64, error) {
	cards, err := s.repo.ListCards(studentID, bookID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0)
	now := s.clock.Now()
	for _, c := range cards {
		if c.NextDueAt != nil && !c.NextDueAt.After(now) {
			ids = append(ids, c.WordID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (s *Service) Progress(studentID, bookID int64) (ProgressSummary, error) {
	cards, err := s.repo.ListCards(studentID, bookID)
	if err != nil {
		return ProgressSummary{}, err
	}
	out := ProgressSummary{Total: len(cards)}
	now := s.clock.Now()
	for _, c := range cards {
		switch c.Status {
		case StatusNew:
			out.New++
		case StatusLearning:
			out.Learning++
		case StatusMastered:
			out.Mastered++
		case StatusGraduated:
			out.Graduated++
		}
		if c.NextDueAt != nil && !c.NextDueAt.After(now) {
			out.Due++
		}
	}
	return out, nil
}
