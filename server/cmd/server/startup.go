package main

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"time"

	"enwords/internal/obs"
	"enwords/internal/store"
)

// ownedRuntimeCleanup owns only resources acquired by this invocation of run.
// A worker timeout leaves the pool to process exit rather than closing it
// concurrently with a worker that may still be writing.
type ownedRuntimeCleanup struct {
	pool     *sql.DB
	server   *http.Server
	listener net.Listener
	cancel   context.CancelFunc
	done     <-chan struct{}
	timeout  time.Duration
	log      *obs.Logger
	ctx      context.Context
}

func (c *ownedRuntimeCleanup) Close() error {
	if c.server != nil {
		_ = c.server.Close()
	}
	if c.listener != nil {
		_ = c.listener.Close()
	}
	if c.cancel != nil {
		if !stopRuntime(c.cancel, c.done, c.timeout) {
			c.log.ErrorCtx(c.ctx, "main", "shutdown_incomplete", map[string]any{"reason": "runtime_stop_timeout"})
			return errors.New("runtime stop incomplete")
		}
		c.log.InfoCtx(c.ctx, "main", "runtime_stopped", nil)
	}
	if c.pool != nil {
		if err := c.pool.Close(); err != nil {
			c.log.ErrorCtx(c.ctx, "main", "shutdown_incomplete", map[string]any{"reason": "pool_close_failed"})
			return errors.New("database close incomplete")
		}
	}
	return nil
}

// Startup observations deliberately exclude errors, paths and SQL text.
func startupObserver(ctx context.Context, log *obs.Logger) func(store.StageEvent) {
	return func(e store.StageEvent) {
		payload := map[string]any{"stage": e.Stage, "duration_ms": e.DurationMS}
		if e.StorageMode != "" {
			payload["storage_mode"] = e.StorageMode
		}
		if e.Fresh != nil {
			payload["fresh"] = *e.Fresh
		}
		if e.SchemaObjectCount != nil {
			payload["schema_object_count"] = *e.SchemaObjectCount
		}
		if e.Reason != "" {
			payload["reason"] = e.Reason
		}
		if e.TransactionOutcome != "" {
			payload["transaction_outcome"] = e.TransactionOutcome
		}
		if e.Event == "startup_stage_failed" {
			log.ErrorCtx(ctx, "startup", e.Event, payload)
		} else {
			log.InfoCtx(ctx, "startup", e.Event, payload)
		}
	}
}

func runStartupStage(ctx context.Context, log *obs.Logger, stage, reason string, fn func() error) (err error) {
	started := time.Now()
	log.InfoCtx(ctx, "startup", "startup_stage_started", map[string]any{"stage": stage, "duration_ms": int64(0)})
	completed := false
	defer func() {
		payload := map[string]any{"stage": stage, "duration_ms": time.Since(started).Milliseconds()}
		if !completed || err != nil {
			payload["reason"] = reason
			log.ErrorCtx(ctx, "startup", "startup_stage_failed", payload)
		} else {
			log.InfoCtx(ctx, "startup", "startup_stage_completed", payload)
		}
	}()
	err = fn()
	completed = true
	return err
}

func listenStartup(ctx context.Context, log *obs.Logger, addr string) (listener net.Listener, err error) {
	err = runStartupStage(ctx, log, "listen", "listen_failed", func() error {
		var listenErr error
		listener, listenErr = net.Listen("tcp", addr)
		return listenErr
	})
	return listener, err
}

func stopRuntime(cancel context.CancelFunc, done <-chan struct{}, timeout time.Duration) bool {
	cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
