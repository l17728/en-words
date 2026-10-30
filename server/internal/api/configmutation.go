package api

import (
	"context"
	"encoding/json"
	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"reflect"
	"sort"
	"time"
)

type configMutationWork struct {
	cfg   *config.Store
	sched *scheduler.Service
	db    *gorm.DB
}

// InWork never begins a transaction, acquires the App mutex, or publishes live caches.
func applyConfigMutationInWork(ctx context.Context, w *configMutationWork, m config.Mutation) (config.MutationResult, error) {
	var result config.MutationResult
	if err := config.ValidateMutation(m); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if m.Operation == "delete_override" && !w.cfg.HasOverride(m.StudentID, m.Key) {
		result.Noop = true
		return result, nil
	}
	candidate := w.cfg.CopyValues()
	if _, err := candidate.ApplyContext(ctx, m); err != nil {
		return result, err
	}
	affected := []int64{}
	if config.SensitiveKey(m.Key) {
		ids, err := w.sched.CardStudentIDs(ctx)
		if err != nil {
			return result, err
		}
		protected := map[int64]bool{}
		if w.db != nil {
			protected, err = store.ConfigReservedStudents(w.db)
			if err != nil {
				return result, err
			}
			seen := map[int64]bool{}
			for _, id := range ids {
				seen[id] = true
			}
			for id := range protected {
				if !seen[id] {
					ids = append(ids, id)
				}
			}
		}
		if m.Scope == config.ScopeStudent {
			ids = []int64{m.StudentID}
		}
		for _, sid := range ids {
			if !reflect.DeepEqual(w.cfg.Ladder(sid), candidate.Ladder(sid)) {
				affected = append(affected, sid)
			}
		}
		for _, sid := range affected {
			if protected[sid] {
				return result, fmt.Errorf("atomic_configuration_unavailable")
			}
		}
	}
	if _, err := w.cfg.ApplyContext(ctx, m); err != nil {
		return result, err
	}
	sort.Slice(affected, func(i, j int) bool { return affected[i] < affected[j] })
	for _, sid := range affected {
		n, err := w.sched.ClampLadderSteps(ctx, sid, len(candidate.Ladder(sid))-1)
		if err != nil {
			return result, err
		}
		result.ChangedCards += n
	}
	result.AffectedStudents = int64(len(affected))
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if w.db != nil {
		// Fixed summaries only: no configuration values or free-form reason in logs.
		summary, _ := json.Marshal(map[string]any{"operation": m.Operation, "scope": m.Scope, "student_id": m.StudentID, "key": m.Key, "changed_cards": result.ChangedCards})
		row := store.AuditLog{TS: time.Now(), ActorID: m.Actor, ActorRole: "admin", Tool: "config.mutate", Risk: "high", OK: true, Result: "committed", Args: string(summary), TraceID: obs.TraceID(ctx)}
		if err := w.db.Create(&row).Error; err != nil {
			return result, err
		}
	}
	return result, nil
}

func (a *App) ChangeConfig(ctx context.Context, m config.Mutation) (config.MutationResult, error) {
	var result config.MutationResult
	var err error
	if err := config.ValidateMutation(m); err != nil {
		return result, err
	}
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	logResult := func(event, outcome string) {
		code := ""
		if outcome == "rolled_back" {
			code = "configuration_transaction_failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = "canceled"
			} else if err != nil && err.Error() == "atomic_configuration_unavailable" {
				code = "atomic_configuration_unavailable"
			}
		}
		if event == "config_mutation_refresh_pending" {
			code = "configuration_refresh_pending"
		}
		a.Log.InfoCtx(ctx, "config", event, map[string]any{"operation": m.Operation, "key": m.Key, "scope": m.Scope, "student_id": m.StudentID, "affected_students": result.AffectedStudents, "changed_cards": result.ChangedCards, "transaction_outcome": outcome, "error_code": code})
	}
	logResult("config_mutation_started", "pending")
	if a.DB != nil {
		err = store.WithAtomicWrite(ctx, a.DB, func(tx *gorm.DB) error {
			cfg, e := config.NewStore(store.NewConfigPersister(tx), nil)
			if e != nil {
				return e
			}
			result, e = applyConfigMutationInWork(ctx, &configMutationWork{cfg: cfg, sched: scheduler.NewService(store.NewSchedulerRepo(tx), cfg, a.Clock), db: tx}, m)
			return e
		})
	} else {
		if !a.Cfg.SupportsMemoryTransactions() || !a.Sched.SupportsMemoryTransactions() || !a.Mem.SupportsMemoryTransactions() {
			err = fmt.Errorf("atomic_configuration_unavailable")
			logResult("config_mutation_rolled_back", "rolled_back")
			return result, err
		}
		a.mu.Lock()
		err = a.Cfg.WithMemoryTransaction(func(cfg *config.Store) error {
			return a.Sched.WithMemoryTransaction(cfg, func(s *scheduler.Service) error {
				return a.Mem.WithMemoryTransaction(func(_ *memory.Service) error {
					var e error
					result, e = applyConfigMutationInWork(ctx, &configMutationWork{cfg: cfg, sched: s}, m)
					return e
				})
			})
		})
		a.mu.Unlock()
	}
	if err != nil {
		result = config.MutationResult{}
		logResult("config_mutation_rolled_back", "rolled_back")
		return result, err
	}
	result.Committed = true
	if a.DB != nil {
		if e := a.Cfg.Reload(); e != nil {
			a.configRefreshPending = true
			result.RefreshPending = true
			logResult("config_mutation_refresh_pending", "committed")
			return result, nil
		}
		a.configRefreshPending = false
	}
	logResult("config_mutation_committed", "committed")
	return result, nil
}

// Fresh reads never fall back to the live cache after persistence errors.
func (a *App) readCurrentConfig(ctx context.Context, fn func(*config.Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.DB != nil {
		err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			cfg, err := config.NewStore(store.NewConfigPersister(tx), nil)
			if err != nil {
				return err
			}
			return fn(cfg)
		})
		if err != nil {
			return err
		}
		// The fresh response is already authoritative. Repair only the local cache;
		// never reapply the committed write, and never replace the response with stale data.
		a.agentWriteMu.Lock()
		if a.configRefreshPending {
			if e := a.Cfg.Reload(); e == nil {
				a.configRefreshPending = false
			}
		}
		a.agentWriteMu.Unlock()
		return nil
	}
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	return fn(a.Cfg)
}
func (a *App) EffectiveConfiguration(ctx context.Context, sid int64) (config.EffectiveConfig, error) {
	var out config.EffectiveConfig
	err := a.readCurrentConfig(ctx, func(cfg *config.Store) error { var e error; out, e = cfg.EffectiveView(sid); return e })
	return out, err
}
func (a *App) FreshConfigSnapshot(ctx context.Context, sid int64) (config.EffectiveSnapshot, error) {
	var out config.EffectiveSnapshot
	err := a.readCurrentConfig(ctx, func(cfg *config.Store) error { out = cfg.Snapshot(sid); return nil })
	return out, err
}

func (a *App) WithCurrentConfigWork(ctx context.Context, fn func(*scheduler.Service) error) error {
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	type plannedEvent struct {
		sid, bid int64
		lesson   *scheduler.Lesson
	}
	var planned []plannedEvent
	run := func(s *scheduler.Service) error {
		s.OnPlanned = func(sid, bid int64, l *scheduler.Lesson) {
			cp := *l
			cp.BatchWordIDs = append([]int64(nil), l.BatchWordIDs...)
			planned = append(planned, plannedEvent{sid, bid, &cp})
		}
		return fn(s)
	}
	var err error
	if a.DB != nil {
		err = store.WithAtomicWrite(ctx, a.DB, func(tx *gorm.DB) error {
			cfg, err := config.NewStore(store.NewConfigPersister(tx), nil)
			if err != nil {
				return err
			}
			return run(scheduler.NewService(store.NewSchedulerRepo(tx), cfg, a.Clock))
		})
	} else {
		if !a.Cfg.SupportsMemoryTransactions() || !a.Sched.SupportsMemoryTransactions() {
			return fmt.Errorf("atomic_configuration_unavailable")
		}
		err = a.Cfg.WithMemoryTransaction(func(cfg *config.Store) error {
			return a.Sched.WithMemoryTransaction(cfg, func(s *scheduler.Service) error {
				if err := run(s); err != nil {
					return err
				}
				return ctx.Err()
			})
		})
	}
	if err == nil && a.Sched.OnPlanned != nil {
		for _, event := range planned {
			a.Sched.OnPlanned(event.sid, event.bid, event.lesson)
		}
	}
	return err
}
