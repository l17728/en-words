package api

import "enwords/internal/classroom"

// All current SystemAgent completion actions use one audited unit of work.
func (a *App) commitLesson(id, sid, bid int64, res *classroom.Result, st classroom.State) error {
	return a.commitLessonWithAgents(id, sid, bid, res, st)
}

func max50(n int) int {
	if n < 50 {
		return 50
	}
	return n
}
