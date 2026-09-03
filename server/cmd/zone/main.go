package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/coordinatorclient"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/database"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/health"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/logging"
	"github.com/Wriosley/supernova-classic-farm/server/internal/platform/shutdown"
	"github.com/Wriosley/supernova-classic-farm/server/internal/player"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"github.com/Wriosley/supernova-classic-farm/server/internal/visit"
)

const (
	defaultListenAddress = "127.0.0.1:8082"
	dualRoutingMode      = "static-dual-zone"
)

func main() {
	routingMode := environmentOr("ROUTING_MODE", "local")
	if routingMode != "local" && routingMode != dualRoutingMode {
		log.Fatalf("unsupported ROUTING_MODE %q", routingMode)
	}
	ownerZoneID := routing.DefaultZoneID
	if routingMode == dualRoutingMode {
		ownerZoneID = environmentOr("OWNER_ZONE_ID", "zone-a")
	}
	listenAddress := environmentOr("ZONE_HTTP_ADDRESS", defaultListenAddress)
	if err := requireLoopbackListenAddress(listenAddress); err != nil {
		log.Fatal(err)
	}
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	logger, err := logging.New("zone-"+ownerZoneID, "development", "info")
	if err != nil {
		log.Fatal(err)
	}

	var runtime *player.Runtime
	if dsn == "" {
		runtime = player.NewRuntime()
		logger.Warn("using development-only lazy in-memory player state")
	} else {
		db, openErr := database.OpenMySQL(context.Background(), dsn)
		if openErr != nil {
			log.Fatal(openErr)
		}
		defer db.Close()
		runtime, err = player.NewRuntimeWithLoader(&player.MySQLCheckpointLoader{
			DB: db, OwnerZoneID: ownerZoneID,
		})
		if err != nil {
			log.Fatal(err)
		}
		logger.Info("using MySQL Player checkpoint activation")
	}
	defer runtime.Close()

	ctx, cancel := shutdown.SignalContext(context.Background())
	defer cancel()
	gates := &shardExecutionGates{}
	var authorization ownerAuthorization = localAuthorization{}
	var lifecycle *lifecycleHandler
	var table *routing.AuthorizationTable
	if routingMode == dualRoutingMode {
		table, err = routing.NewAuthorizationTable(ownerZoneID)
		if err != nil {
			log.Fatal(err)
		}
		authorization = table
	}
	coordinatorURL := environmentOr("COORDINATOR_URL", "http://127.0.0.1:8083")
	routeConfig := coordinatorclient.Config{
		BaseURL: coordinatorURL,
		Client:  &http.Client{Timeout: 32 * time.Second},
	}
	if table != nil {
		routeConfig.OnSnapshot = table.Replace
	}
	routeClient, err := coordinatorclient.New(routeConfig)
	if err != nil {
		log.Fatal(err)
	}
	if err := routeClient.Start(ctx); err != nil {
		log.Fatalf("start Coordinator route client: %v", err)
	}
	defer routeClient.Close()
	if table != nil {
		lifecycle = &lifecycleHandler{
			runtime: runtime, authorization: table, gates: gates, now: time.Now,
			refresh: func() error {
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 3*time.Second)
				defer refreshCancel()
				return routeClient.ForceResync(refreshCtx)
			},
		}
	}

	visitRegistry := visit.NewRegistry(time.Now)
	pushEndpoint := os.Getenv("GATE_PUSH_URL")
	if pushEndpoint == "" {
		pushEndpoint = "http://127.0.0.1:8081/internal/v1/player-state-changes"
	}
	pushForwarder, err := player.NewHTTPPushForwarder(
		&http.Client{Timeout: 2 * time.Second},
		pushEndpoint,
	)
	if err != nil {
		log.Fatal(err)
	}
	if err := runtime.SetPushForwarder(pushForwarder); err != nil {
		log.Fatal(err)
	}
	farmDispatcher := visit.NewFarmChangeDispatcher(
		visitRegistry, pushForwarder, logger,
	)
	if farmDispatcher == nil {
		log.Fatal("create farm change dispatcher")
	}
	if err := runtime.SetFarmChangeForwarder(farmDispatcher); err != nil {
		log.Fatal(err)
	}
	defer func() {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer drainCancel()
		farmDispatcher.Close(drainCtx)
	}()

	mux := http.NewServeMux()
	commandHandler := newOwnedCommandHandlerWithGates(runtime, authorization, gates, time.Now)
	visitHTTPClient := &visit.HTTPClients{
		Client:    &http.Client{Timeout: 5 * time.Second},
		Routes:    routeClient,
		FriendURL: environmentOr("FRIEND_URL", "http://127.0.0.1:8085"),
	}
	visitService, err := visit.NewService(
		visitRegistry, visitHTTPClient, visitHTTPClient, runtime, time.Now,
	)
	if err != nil {
		log.Fatal(err)
	}
	commandHandler.visit = visitService
	friendVisitHandler := &friendVisitHTTPHandler{
		runtime: runtime, registry: visitRegistry,
		authorization: authorization, gates: gates, now: time.Now,
	}
	mux.Handle("POST /internal/v1/command", commandHandler)
	mux.HandleFunc("POST /internal/v1/players/{player_id}/friend-task-credit",
		commandHandler.friendTaskCredit)
	mux.HandleFunc("POST /internal/v1/friend-visits/enter", friendVisitHandler.enter)
	mux.HandleFunc("POST /internal/v1/friend-visits/heartbeat", friendVisitHandler.heartbeat)
	mux.HandleFunc("POST /internal/v1/friend-visits/exit", friendVisitHandler.exit)
	mux.HandleFunc("POST /internal/v1/friend-visits/apply-steal", friendVisitHandler.applySteal)
	mux.HandleFunc("POST /internal/v1/friend-visits/apply-pest", friendVisitHandler.applyPest)
	mux.HandleFunc("POST /internal/v1/friend-visits/catch-pest", friendVisitHandler.catchPest)
	if lifecycle != nil {
		mux.HandleFunc("POST /internal/v1/shards/{shard_id}/drain", lifecycle.drain)
		mux.HandleFunc("POST /internal/v1/shards/{shard_id}/drain-complete", lifecycle.completeDrain)
		mux.HandleFunc("POST /internal/v1/shards/{shard_id}/prepare", lifecycle.prepareMigration)
		mux.HandleFunc("POST /internal/v1/shards/{shard_id}/resume", lifecycle.resume)
		mux.HandleFunc("POST /internal/v1/ownership/refresh", lifecycle.refreshOwnership)
	}
	healthHandler := health.NewHandler()
	mux.Handle("GET /livez", healthHandler)
	mux.Handle("GET /readyz", healthHandler)

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	logger.Info("zone listening",
		"address", listenAddress,
		"owner_zone_id", ownerZoneID,
		"owner_epoch", player.LocalOwnerEpoch,
		"routing_mode", routingMode,
		"gate_push_url", pushEndpoint,
		"state_adapter", func() string {
			if os.Getenv("MYSQL_DSN") == "" {
				return "lazy-in-memory-development-only"
			}
			return "mysql-checkpoint"
		}(),
	)
	if err := shutdown.Serve(ctx, server, 5*time.Second, logger); err != nil {
		logger.Error("zone stopped", "error", err)
	}
}

func environmentOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func requireLoopbackListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid Zone HTTP address %q: %w", address, err)
	}
	if port == "" {
		return errors.New("Zone HTTP port is required")
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") &&
		(ip == nil || !ip.IsLoopback()) {
		return errors.New("development ZoneSvr must bind an explicit loopback address")
	}
	return nil
}
