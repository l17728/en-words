package api

import (
	"context"
	"enwords/internal/classroom"
)

// All current SystemAgent completion actions use one audited unit of work.
func (a *App) commitLesson(id, sid, bid int64, res *classroom.Result, st classroom.State) error {
	lease, err := a.generations.acquire(context.Background(), id)
	if err != nil {
		return err
	}
	defer lease.Release()
	return a.commitLessonWithGeneration(withGenerationLease(lease.Context(), lease), lease.Token(), id, sid, bid, res, st)
}

func (a *App) commitLessonWithGeneration(ctx context.Context, token generationToken, id, sid, bid int64, res *classroom.Result, st classroom.State) error {
	effect, err := token.Permit()
	if err != nil {
		return err
	}
	defer effect.Release()
	if ctx == nil {
		ctx = context.Background()
	}
	return a.commitLessonWithAgents(ctx, token, id, sid, bid, res, st)
}

func max50(n int) int {
	if n < 50 {
		return 50
	}
	return n
}
