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
	"syscall"
	"time"

	"enwords/internal/api"
	"enwords/internal/obs"
	"enwords/internal/store"

	"gorm.io/gorm"
)

func main() {
	port := flag.Int("port", 8080, "listen port")
	testmode := flag.Bool("testmode", false, "test mode (in-memory everything, no seed)")
	providers := flag.String("providers", "mock", "mock | local（本机8090模型平台）| real（智谱，secrets 密钥）")
	dbPath := flag.String("db", "", "SQLite 路径（空=纯内存；默认 data/enwords.db 当 -persist）")
	persist := flag.Bool("persist", false, "启用 SQLite 持久化（等价 -db data/enwords.db）")
	flag.Parse()

	// 持久化决策：-db 指定路径 或 -persist 默认路径
	var db *gorm.DB
	if *dbPath != "" || *persist {
		p := *dbPath
		if p == "" {
			p = filepath.Join("data", "enwords.db")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Println("mkdir fail:", err)
			os.Exit(1)
		}
		var err error
		db, err = store.Open(p)
		if err != nil {
			fmt.Println("open db fail:", err)
			os.Exit(1) // fail-fast
		}
	}

	app := api.NewApp(nil, db)
	log := obs.New(os.Stdout)
	app.SetLogger(log)
	if app.StartupError != nil {
		log.Error("store", "restore_failed", map[string]any{"error": app.StartupError.Error()})
		os.Exit(1)
	}
	if *testmode {
		app.SetTestMode()
		log.Info("main", "testmode_enabled", nil)
	}
	if *providers == "real" {
		if err := app.EnableRealProviders(); err != nil {
			log.Error("main", "real_providers_failed", map[string]any{"error": err.Error()})
			os.Exit(1) // fail-fast：要求真实却配不了=配置错误
		}
	}
	if *providers == "local" {
		if err := app.EnableLocalProviders(context.Background()); err != nil {
			log.Error("main", "local_providers_failed", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
	}
	if *providers != "mock" && *providers != "real" && *providers != "local" {
		log.Error("main", "invalid_provider", nil)
		os.Exit(1)
	}

	// 首启自动播种（SDD §17：无 admin = 第一次运行 → 种子词库就位，双击即用）
	if !*testmode && !app.Auth.HasAdmin() {
		if books, words, err := app.AutoSeed(); err != nil {
			log.Warn("bootstrap", "auto_seed_skipped", map[string]any{"error": err.Error()})
		} else {
			log.Info("bootstrap", "auto_seed_done", map[string]any{"books": books, "words": words})
		}
	}

	// 计时循环（相位超时/会话超时）
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	defer cancelRuntime()
	go app.Rooms.RunTimersContext(runtimeCtx)
	go func() {
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

	r := app.Router()
	srv := &http.Server{Addr: fmt.Sprintf(":%d", *port), Handler: r,
		ReadHeaderTimeout: 10 * time.Second}

	go func() {
		log.Info("main", "server_started", map[string]any{
			"port": *port, "testmode": *testmode})
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("main", "listen_failed", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	cancelRuntime()
	log.Info("main", "shutting_down", nil)
	_ = srv.Close()
}
