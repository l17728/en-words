// 叮当words 服务入口：装配 App、启动 HTTP(REST+WS) 与课堂计时循环。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"enwords/internal/api"
	"enwords/internal/obs"
	"enwords/internal/store"

	"gorm.io/gorm"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() (runErr error) {
	port := flag.Int("port", 8080, "listen port")
	testmode := flag.Bool("testmode", false, "test mode (in-memory everything, no seed)")
	providers := flag.String("providers", "mock", "mock | local（本机8090模型平台）| real（智谱，secrets 密钥）")
	dbPath := flag.String("db", "", "SQLite 路径（空=纯内存；默认 data/enwords.db 当 -persist）")
	persist := flag.Bool("persist", false, "启用 SQLite 持久化（等价 -db data/enwords.db）")
	flag.Parse()
	log := obs.New(os.Stdout)
	startupCtx := obs.WithTrace(context.Background(), obs.NewTraceID())
	startupStarted := time.Now()
	owned := &ownedRuntimeCleanup{ctx: startupCtx, log: log, timeout: 5 * time.Second}
	defer func() {
		if err := owned.Close(); runErr == nil {
			runErr = err
		}
	}()

	// 持久化决策：-db 指定路径 或 -persist 默认路径
	var db *gorm.DB
	if *dbPath != "" || *persist {
		p := *dbPath
		if p == "" {
			p = filepath.Join("data", "enwords.db")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			log.ErrorCtx(startupCtx, "startup", "startup_stage_failed", map[string]any{
				"stage": "open", "reason": "open_failed", "duration_ms": time.Since(startupStarted).Milliseconds()})
			return err
		}
		var err error
		db, err = store.OpenContext(startupCtx, p, startupObserver(startupCtx, log))
		if err != nil {
			return err
		}
		pool, err := db.DB()
		if err != nil {
			log.ErrorCtx(startupCtx, "startup", "startup_stage_failed", map[string]any{"stage": "open", "reason": "open_failed"})
			return err
		}
		owned.pool = pool
	}

	var app *api.App
	if err := runStartupStage(startupCtx, log, "app_restore", "restore_failed", func() error {
		app = api.NewApp(nil, db)
		return app.StartupError
	}); err != nil {
		return err
	}
	app.DuplexPreflight = os.Getenv("ENWORDS_DUPLEX_PILOT") == "1"
	app.SetLogger(log)
	if *testmode {
		app.SetTestMode()
		log.Info("main", "testmode_enabled", nil)
	}
	if *providers == "real" {
		if err := app.EnableRealProviders(); err != nil {
			log.Error("main", "real_providers_failed", map[string]any{"error": err.Error()})
			return err // fail-fast：要求真实却配不了=配置错误
		}
	}
	if *providers == "local" {
		if err := app.EnableLocalProviders(context.Background()); err != nil {
			log.Error("main", "local_providers_failed", map[string]any{"error": err.Error()})
			return err
		}
	}
	if *providers != "mock" && *providers != "real" && *providers != "local" {
		log.Error("main", "invalid_provider", nil)
		return fmt.Errorf("invalid provider")
	}

	// 首启自动播种（SDD §17：无 admin = 第一次运行 → 种子词库就位，双击即用）
	if !*testmode && !app.Auth.HasAdmin() {
		if books, words, err := app.AutoSeed(); err != nil {
			log.Warn("bootstrap", "auto_seed_skipped", map[string]any{"error": err.Error()})
		} else {
			log.Info("bootstrap", "auto_seed_done", map[string]any{"books": books, "words": words})
		}
	}

	r := app.Router()
	srv := &http.Server{Addr: fmt.Sprintf(":%d", *port), Handler: r,
		ReadHeaderTimeout: 10 * time.Second}
	listener, err := listenStartup(startupCtx, log, srv.Addr)
	if err != nil {
		return err
	}
	owned.listener = listener
	owned.server = srv
	log.InfoCtx(startupCtx, "main", "server_started", map[string]any{
		"port": *port, "testmode": *testmode,
		"startup_total_ms": time.Since(startupStarted).Milliseconds()})

	// 计时循环（相位超时/会话超时）
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	var runtimeWorkers sync.WaitGroup
	runtimeWorkers.Add(2)
	runtimeDone := make(chan struct{})
	owned.cancel = cancelRuntime
	owned.done = runtimeDone
	go func() {
		defer runtimeWorkers.Done()
		app.Rooms.RunTimersContext(runtimeCtx)
	}()
	go func() {
		defer runtimeWorkers.Done()
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			if err := app.RunOverdue(runtimeCtx); err != nil && runtimeCtx.Err() == nil {
				log.Error("scheduler", "overdue_scan_failed", map[string]any{"reason": "scan_or_transaction_failed"})
			}
			select {
			case <-runtimeCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		runtimeWorkers.Wait()
		close(runtimeDone)
	}()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.ErrorCtx(startupCtx, "main", "serve_failed", map[string]any{"reason": "serve_failed"})
			serveErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	select {
	case <-quit:
	case err := <-serveErr:
		return err
	}
	cancelRuntime()
	log.Info("main", "shutting_down", nil)
	_ = srv.Close()
	return nil
}
