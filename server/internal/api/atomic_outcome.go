package api

import (
	"context"
	"enwords/internal/store"
	"errors"
	"fmt"
	"gorm.io/gorm"
)

var errAtomicOutcomeUncertain = errors.New("atomic outcome uncertain")

func preserveAtomicOutcome(err error, outcome string) error {
	if err == nil || outcome != "outcome_uncertain" {
		return err
	}
	return fmt.Errorf("%w: %v", errAtomicOutcomeUncertain, err)
}

func (a *App) withAtomicWriteObserved(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error, observe func(store.AtomicConnectionTiming)) error {
	if a.atomicWriteObserved != nil {
		return a.atomicWriteObserved(ctx, db, fn, observe)
	}
	return store.WithAtomicWriteObserved(ctx, db, fn, observe)
}
