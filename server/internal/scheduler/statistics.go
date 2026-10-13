package scheduler

// StatisticsData propagates repository failures instead of treating them as
// an empty student history. Persistent callers bind this service to one tx.
func (s *Service) StatisticsData(studentID int64) ([]*Card, []*Lesson, error) {
	books, err := s.repo.StudentBooks(studentID)
	if err != nil {
		return nil, nil, err
	}
	cards := make([]*Card, 0)
	lessons := make([]*Lesson, 0)
	for _, book := range books {
		rows, err := s.repo.ListCards(studentID, book)
		if err != nil {
			return nil, nil, err
		}
		cards = append(cards, rows...)
		planned, err := s.repo.ListLessons(studentID, book)
		if err != nil {
			return nil, nil, err
		}
		lessons = append(lessons, planned...)
	}
	return cards, lessons, nil
}
