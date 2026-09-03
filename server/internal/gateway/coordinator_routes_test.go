package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"google.golang.org/protobuf/proto"
)

type refreshingCoordinator struct {
	now       time.Time
	version   atomic.Uint64
	refreshes atomic.Int32
}

func (c *refreshingCoordinator) ResolveShard(shardID uint32) (routing.RouteEntry, error) {
	version := c.version.Load()
	return routing.RouteEntry{
		ShardID: shardID, OwnerZoneID: "zone-a",
		OwnerEndpoint: "http://127.0.0.1:8082",
		OwnerEpoch:    1, RouteVersion: version,
		State: routing.RouteStateActive, LeaseTerm: 1, LeaseID: "lease",
		LeaseExpiresAt: c.now.Add(time.Minute), UpdatedAt: c.now,
	}, nil
}

func (c *refreshingCoordinator) MapVersion() uint64 {
	return c.version.Load()
}

func (c *refreshingCoordinator) ForceResync(context.Context) error {
	c.refreshes.Add(1)
	c.version.Store(2)
	return nil
}

func TestCoordinatorRoutesSynchronouslyRefreshesBeforeNotOwnerRetry(t *testing.T) {
	now := time.Now().UTC()
	client := &refreshingCoordinator{now: now}
	client.version.Store(1)
	routes := &CoordinatorRoutes{Client: client, Now: func() time.Time { return now }}
	var zoneCalls atomic.Int32
	var routeVersions []uint64
	var mu sync.Mutex
	zone := zoneFunc(func(
		_ context.Context, route Route, caller uint64, body []byte,
	) ([]byte, error) {
		request := decodeEnvelope(t, body)
		mu.Lock()
		routeVersions = append(routeVersions, route.RouteVersion)
		mu.Unlock()
		if zoneCalls.Add(1) == 1 {
			return nil, ErrNotOwner
		}
		return proto.Marshal(snapshotResponse(request, caller))
	})
	conn, closeServer := authenticatedConnection(t, zone, routes)
	defer closeServer()
	defer conn.CloseNow()

	writeEnvelope(t, conn, snapshotRequest("sdk-same-id", 42))
	if response := readEnvelope(t, conn); response.RequestId != "sdk-same-id" {
		t.Fatalf("response request_id = %q", response.RequestId)
	}
	if client.refreshes.Load() != 1 || zoneCalls.Load() != 2 {
		t.Fatalf("refresh/Zone calls = %d/%d", client.refreshes.Load(), zoneCalls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(routeVersions) != 2 || routeVersions[0] != 1 || routeVersions[1] != 2 {
		t.Fatalf("route versions = %v", routeVersions)
	}
}
