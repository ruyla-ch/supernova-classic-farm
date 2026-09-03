package coordinatorclient

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

func TestClientInitialWatchResolveAndForceResync(t *testing.T) {
	now := time.Now().UTC()
	routes, err := routing.NewLocalMap(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(routing.NewHTTPHandler(routes, time.Now))
	defer server.Close()

	var mu sync.Mutex
	var callbackVersions []uint64
	client, err := New(Config{
		BaseURL: server.URL, Client: server.Client(),
		WatchTimeout: 100 * time.Millisecond,
		MinBackoff:   5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
		OnSnapshot: func(snapshot routing.Snapshot) error {
			mu.Lock()
			callbackVersions = append(callbackVersions, snapshot.MapVersion)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	entry, err := client.ResolvePlayer(42)
	if err != nil || entry.ShardID != routing.ShardForPlayer(42) {
		t.Fatalf("ResolvePlayer() = %+v, %v", entry, err)
	}
	copy := client.Snapshot()
	copy.Entries[0].OwnerZoneID = "mutated"
	if client.Snapshot().Entries[0].OwnerZoneID == "mutated" {
		t.Fatal("Snapshot returned mutable cached entries")
	}

	if _, err := routes.RenewOwnedLeases(routing.DefaultZoneID, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	waitForMapVersion(t, client, 2)

	if _, err := routes.RenewOwnedLeases(routing.DefaultZoneID, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := client.ForceResync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.MapVersion() < 3 {
		t.Fatalf("map version = %d, want at least 3", client.MapVersion())
	}

	response, err := server.Client().Get(server.URL + "/internal/v1/debug/route-lookups")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var stats struct {
		Snapshot uint64 `json:"snapshot"`
		Shard    uint64 `json:"shard"`
	}
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.Snapshot != 2 || stats.Shard != 0 {
		t.Fatalf("ordinary lookup reached Coordinator: %+v", stats)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(callbackVersions) < 3 || callbackVersions[0] != 1 {
		t.Fatalf("callback versions = %v", callbackVersions)
	}
}

func TestClientRejectsStaleInactiveAndExpiredRoutes(t *testing.T) {
	now := time.Now().UTC()
	routes, err := routing.NewLocalMap(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		BaseURL: "http://127.0.0.1:8083",
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	first := routes.Snapshot()
	first.MapVersion = 2
	if err := client.publish(first, false); err != nil {
		t.Fatal(err)
	}
	stale := routes.Snapshot()
	if err := client.publish(stale, false); err != ErrStaleSnapshot {
		t.Fatalf("stale publish error = %v", err)
	}

	inactive := first
	inactive.Entries = append([]routing.RouteEntry(nil), first.Entries...)
	inactive.MapVersion = 3
	inactive.Entries[7].State = routing.RouteStatePreparing
	inactive.Entries[7].TransitionID = "transition"
	if err := client.publish(inactive, false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResolveShard(7); err != ErrRouteUnavailable {
		t.Fatalf("inactive ResolveShard error = %v", err)
	}

	now = first.Entries[8].LeaseExpiresAt
	if _, err := client.ResolveShard(8); err != ErrRouteUnavailable {
		t.Fatalf("expired ResolveShard error = %v", err)
	}
}

func waitForMapVersion(t *testing.T, client *Client, want uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for client.MapVersion() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if client.MapVersion() < want {
		t.Fatalf("map version = %d, want >= %d", client.MapVersion(), want)
	}
}
