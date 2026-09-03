package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/friend"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/config"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/database"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/health"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/logging"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/shutdown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("friend service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load("friend", "127.0.0.1:8085")
	if err != nil {
		return err
	}
	if err := requireLoopbackAddress(cfg.HTTPAddress); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.MySQLDSN) == "" {
		return errors.New("FriendSvr requires MYSQL_DSN; in-memory friend authority is intentionally unsupported")
	}
	logger, err := logging.New(cfg.ServiceName, cfg.Environment, cfg.LogLevel)
	if err != nil {
		return err
	}
	db, err := database.OpenMySQL(context.Background(), cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	store, err := friend.NewMySQLStore(db)
	if err != nil {
		return err
	}
	friendHandler, err := friend.NewHandler(store, time.Now, &friend.HTTPTaskCreditor{
		Client:         &http.Client{Timeout: 5 * time.Second},
		CoordinatorURL: envOr("COORDINATOR_URL", "http://127.0.0.1:8083"),
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/internal/v1/", friendHandler)
	healthHandler := health.NewHandler()
	mux.Handle("GET /livez", healthHandler)
	mux.Handle("GET /readyz", healthHandler)
	server := &http.Server{
		Addr: cfg.HTTPAddress, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	logger.Info("friend listening", "address", cfg.HTTPAddress, "storage", "mysql")
	ctx, cancel := shutdown.SignalContext(context.Background())
	defer cancel()
	return shutdown.Serve(ctx, server, cfg.ShutdownTimeout, logger)
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("development FriendSvr must bind an explicit loopback address")
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
