package friend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

func TestHTTPTaskCreditorUsesCoordinatorOwnerEndpoint(t *testing.T) {
	const playerID uint64 = 42
	var gotPath, gotShard, gotZone, gotEpoch string
	zone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotShard = r.Header.Get("X-Shard-ID")
		gotZone = r.Header.Get("X-Owner-Zone-ID")
		gotEpoch = r.Header.Get("X-Owner-Epoch")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer zone.Close()

	shardID := routing.ShardForPlayer(playerID)
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/routes/"+strconv.FormatUint(uint64(shardID), 10) {
			t.Fatalf("unexpected route path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"shard_id":            shardID,
			"owner_zone_id":       routing.DefaultZoneID,
			"owner_endpoint":      zone.URL,
			"owner_epoch":         "1",
			"route_version":       "1",
			"state":               string(routing.RouteStateActive),
			"lease_term":          "1",
			"lease_id":            "lease-1",
			"lease_expires_at_ms": time.Now().Add(time.Minute).UnixMilli(),
			"updated_at_ms":       time.Now().UnixMilli(),
			"map_version":         "1",
			"routable":            true,
		})
	}))
	defer coordinator.Close()

	creditor := &HTTPTaskCreditor{
		Client:         &http.Client{Timeout: 2 * time.Second},
		CoordinatorURL: coordinator.URL,
	}
	if err := creditor.Credit(context.Background(), playerID); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/internal/v1/players/42/friend-task-credit" {
		t.Fatalf("path=%s", gotPath)
	}
	if gotShard != strconv.FormatUint(uint64(shardID), 10) ||
		gotZone != routing.DefaultZoneID ||
		gotEpoch != "1" {
		t.Fatalf("headers shard=%s zone=%s epoch=%s", gotShard, gotZone, gotEpoch)
	}
}
