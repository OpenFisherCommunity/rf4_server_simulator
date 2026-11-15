// aibridge —— AI 接入独立进程。只连本地 SQLite 与（默认开启的）模型 API，
// 不参与游戏协议；进程挂掉不影响 gateway / gameserver。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rf4server/internal/aibridge"
	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aibridge:", err)
		os.Exit(2)
	}
	log := logging.New(cfg.Log.Level, cfg.Log.Format)
	log.Info("aibridge 启动", "env", cfg.Env, "db", cfg.SQLite.Path, "obs", cfg.AI.ObsAddr)

	// 必须挂静态库：角色初始化、鱼情卡、鱼名归一化都要读只读静态表。
	// 静态库缺失时退回基础连接，聊天仍可用，但依赖静态表的功能会自动降级。
	db, err := store.OpenWithStatic(cfg.SQLite.Path, cfg.SQLite.StaticPath)
	if err != nil {
		log.Warn("静态库挂载失败，AI 资料查询降级", "err", err)
		db, err = store.Open(cfg.SQLite.Path)
		if err != nil {
			log.Error("SQLite 打开失败", "err", err)
			os.Exit(1)
		}
	}
	defer db.Close()
	// 表由 gameserver/seed 的迁移创建；这里只检查，避免两个进程并发 DDL。
	var one int
	if err := db.SQL.QueryRow(`SELECT 1 FROM ai_task LIMIT 1`).Scan(&one); err != nil {
		// 空表也返回 sql.ErrNoRows，用单独的存在性检查区分。
		var name string
		if e := db.SQL.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ai_task'`).Scan(&name); e != nil {
			log.Error("AI 数据表不存在，请先启动 gameserver 或运行 seed", "err", e)
			os.Exit(1)
		}
	}

	masterKey, err := aibridge.LoadOrCreateMasterKey(cfg.AI.MasterKeyFile)
	if err != nil {
		log.Error("主密钥加载失败", "err", err)
		os.Exit(1)
	}

	knowledge, err := aibridge.OpenKnowledge(cfg.SQLite.StaticPath)
	if err != nil {
		log.Warn("静态库不可用，内部资料检索降级为空", "err", err)
		knowledge = &aibridge.Knowledge{}
	} else {
		defer knowledge.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if addr := cfg.AI.ObsAddr; addr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "service": "aibridge",
				"outbound":   db.AISetting(ctx, "outbound_enabled", "1"),
				"web_search": db.AISetting(ctx, "web_search_enabled", "0"),
				"emergency":  db.AISetting(ctx, "emergency_stop", "0"),
				"ts":         time.Now().Unix(),
			})
		})
		srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Warn("观测端口启动失败", "addr", addr, "err", err)
			}
		}()
		defer func() { _ = srv.Close() }()
	}

	runner := aibridge.NewRunner(db, knowledge, cfg.AI, masterKey, log)
	runner.Loop(ctx)
	log.Info("aibridge 退出")
}
