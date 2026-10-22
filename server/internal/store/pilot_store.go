// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"gorm.io/gorm"
)

type PilotStore struct {
	db       *gorm.DB
	verifier *pilotSchemaVerifier
	observer func(context.Context, PilotStoreEvent)
}

// Fixed vocabulary; never carry raw SQL, errors, JSON, paths, epochs or keys.
type PilotStoreEvent struct {
	Event                                 PilotStoreEventKind
	Outcome                               PilotStoreOutcome
	Reason                                PilotStoreReason
	StudentID, BookID, LessonID           int64
	SchemaVersion                         int64
	WriterWaitMS, TransactionMS           int64
	Replayed, CacheHit                    bool
	SchemaCookie, ObjectCount, ScopeCount int64
}
type PilotStoreEventKind uint8

const (
	PilotScopeReserved PilotStoreEventKind = iota + 1
	PilotLessonCreated
	PilotSchemaAudited
	PilotSchemaMigrated
)

type PilotStoreOutcome uint8

const (
	PilotPrepared PilotStoreOutcome = iota + 1
	PilotCommitted
	PilotRejected
)

type PilotStoreReason uint8

const (
	PilotNoFailure PilotStoreReason = iota
	PilotScopeProtected
	PilotSchemaInvalid
	PilotCommitFailed
	PilotOperationFailed
	PilotUnauthorized
	PilotPlanStale
	PilotCreationConflict
	PilotNotReady
	PilotCancelled
)

// Install before publication. Trusted observer must not block, panic or reenter
// the store. It cannot turn a prepared transaction into a committed receipt.
func (p *PilotStore) SetObserver(observer func(context.Context, PilotStoreEvent)) {
	p.observer = observer
}
func (p *PilotStore) observe(ctx context.Context, event PilotStoreEvent) {
	if p != nil && p.observer != nil {
		p.observer(ctx, event)
	}
}

func newPilotStore(ctx context.Context, db *gorm.DB) (*PilotStore, error) {
	v, err := newPilotSchemaVerifier(db)
	if err != nil {
		return nil, err
	}
	if err = v.AuditCommitted(ctx); err != nil {
		return nil, err
	}
	return &PilotStore{db: db, verifier: v}, nil
}

// Deliberately no SchedulerRepo/ConfigPersister factory, ScopeRepo, HTTP/tool
// adapter, application registration or automatic migration in this slice.
