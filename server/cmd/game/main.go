// Package main 负责选择存储模式、启动唯一的 HTTP/WebSocket 服务并优雅退出。
package main

import (
	"context"
	"database/sql"
	"github.com/Wriosley/supernova-classic-farm/server/internal/game"
	_ "github.com/go-sql-driver/mysql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	// 未配置 MYSQL_DSN 时使用内存模式，方便不安装数据库也能演示。
	var store game.Store = game.NewMemoryStore()
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			log.Print("Invalid MYSQL_DSN configuration")
			return context.Canceled
		}
		defer db.Close()
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(3 * time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = db.PingContext(ctx); err != nil {
			log.Print("Cannot connect to MySQL; check service, database and credentials")
			return context.Canceled
		}
		mysqlStore := &game.MySQLStore{DB: db}
		if err = mysqlStore.Init(ctx); err != nil {
			return err
		}
		store = mysqlStore
		log.Print("Data mode: MySQL; class_mid_* tables, synchronous transactions")
	} else {
		log.Print("Data mode: in-memory; data will be lost on exit")
	}
	app := game.NewServer(store)
	defer app.Close()
	addr := os.Getenv("GAME_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := &http.Server{Addr: addr, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() {
		log.Printf("Game server: http://%s (HTTP + JSON WebSocket /ws)", addr)
		result <- server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
	}
	// 先关闭 WebSocket，再停止 HTTP 服务，避免长连接阻塞退出。
	app.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
