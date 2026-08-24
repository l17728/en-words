// 叮当words 服务入口：装配 App、启动 HTTP(REST+WS) 与课堂计时循环。
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"enwords/internal/api"
	"enwords/internal/obs"
)

func main() {
	port := flag.Int("port", 8080, "listen port")
	testmode := flag.Bool("testmode", false, "test mode (in-memory everything, no seed)")
	providers := flag.String("providers", "mock", "mock | real（real=智谱 GLM 判分L2+反馈解析，secrets 密钥）")
	flag.Parse()

	app := api.NewApp(nil)
	log := obs.New(os.Stdout)
	app.SetLogger(log)
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

	// 首启自动播种（SDD §17：无 admin = 第一次运行 → 种子词库就位，双击即用）
	if !app.Auth.HasAdmin() {
		if books, words, err := app.AutoSeed(); err != nil {
			log.Warn("bootstrap", "auto_seed_skipped", map[string]any{"error": err.Error()})
		} else {
			log.Info("bootstrap", "auto_seed_done", map[string]any{"books": books, "words": words})
		}
	}

	// 计时循环（相位超时/会话超时）
	go app.Rooms.RunTimers()

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
	log.Info("main", "shutting_down", nil)
	_ = srv.Close()
}
