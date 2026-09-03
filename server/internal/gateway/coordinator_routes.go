package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

type CoordinatorRouteClient interface {
	ResolveShard(uint32) (routing.RouteEntry, error)
	MapVersion() uint64
	ForceResync(context.Context) error
}

// CoordinatorRoutes adapts the shared Coordinator SDK cache to Gate routing.
type CoordinatorRoutes struct {
	Client CoordinatorRouteClient
	Now    func() time.Time
}

func (r *CoordinatorRoutes) Resolve(_ context.Context, shardID uint32) (Route, error) {
	if r == nil || r.Client == nil {
		return Route{}, errors.New("Coordinator route client is required")
	}
	entry, err := r.Client.ResolveShard(shardID)
	if err != nil {
		return Route{}, err
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	route := Route{
		ShardID: entry.ShardID, OwnerZoneID: entry.OwnerZoneID,
		OwnerEpoch: entry.OwnerEpoch, RouteVersion: entry.RouteVersion,
		MapVersion: r.Client.MapVersion(), LeaseExpiresAt: entry.LeaseExpiresAt,
		OwnerEndpoint: entry.OwnerEndpoint,
	}
	if !route.usable(now().UTC()) {
		return Route{}, errors.New("cached Coordinator route is not usable")
	}
	return route, nil
}

func (r *CoordinatorRoutes) Refresh(ctx context.Context) error {
	if r == nil || r.Client == nil {
		return errors.New("Coordinator route client is required")
	}
	return r.Client.ForceResync(ctx)
}

func (r Route) usable(now time.Time) bool {
	return r.ShardID < routing.ShardCount &&
		r.OwnerZoneID != "" &&
		r.OwnerEndpoint != "" &&
		r.OwnerEpoch > 0 &&
		r.RouteVersion > 0 &&
		r.MapVersion > 0 &&
		now.Before(r.LeaseExpiresAt)
}
