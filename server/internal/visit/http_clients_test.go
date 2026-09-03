package visit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

func TestPostProtoAcceptsEmptyEncodedMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	clients := &HTTPClients{Client: server.Client()}
	if err := clients.postProto(
		context.Background(), server.URL,
		&zonev1.ExitVisitorRequest{OwnerPlayerId: 1, VisitorPlayerId: 2},
		&zonev1.ExitVisitorResponse{}, nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPestRoutesWithCommittedOwnerHeaders(t *testing.T) {
	const ownerID = uint64(20)
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		if r.URL.Path != "/internal/v1/friend-visits/apply-pest" ||
			r.Header.Get("Content-Type") != "application/x-protobuf" ||
			r.Header.Get("X-Shard-ID") != strconv.FormatUint(uint64(routing.ShardForPlayer(ownerID)), 10) ||
			r.Header.Get("X-Owner-Zone-ID") != "zone-a" ||
			r.Header.Get("X-Owner-Epoch") != "1" ||
			r.Header.Get("X-Route-Version") != "1" {
			t.Fatalf("unexpected pest request path=%s headers=%v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	clients := &HTTPClients{
		Client: server.Client(), Routes: &retryRouteResolver{endpoint: server.URL},
	}
	response, err := clients.ApplyPest(context.Background(), &zonev1.ApplyPestRequest{
		RequestId: "request", OwnerPlayerId: ownerID, VisitorPlayerId: 10,
		VisitId: make([]byte, 16), PlotId: 1, PestId: 1,
	})
	if err != nil || response == nil || !called.Load() {
		t.Fatalf("response=%+v err=%v called=%v", response, err, called.Load())
	}
}

type retryRouteResolver struct {
	endpoint string
	refresh  atomic.Int32
	resolve  atomic.Int32
}

func (r *retryRouteResolver) ResolvePlayer(playerID uint64) (routing.RouteEntry, error) {
	r.resolve.Add(1)
	return routing.RouteEntry{
		ShardID: routing.ShardForPlayer(playerID), OwnerZoneID: "zone-a",
		OwnerEndpoint: r.endpoint, OwnerEpoch: 1, RouteVersion: uint64(r.refresh.Load() + 1),
		State: routing.RouteStateActive, LeaseTerm: 1, LeaseID: "lease",
		LeaseExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func (r *retryRouteResolver) ForceResync(context.Context) error {
	r.refresh.Add(1)
	return nil
}

func TestOwnerConflictSynchronouslyRefreshesAndRetriesOnce(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	routes := &retryRouteResolver{endpoint: server.URL}
	clients := &HTTPClients{Client: server.Client(), Routes: routes}

	_, err := clients.ExitOwner(context.Background(), &zonev1.ExitVisitorRequest{
		OwnerPlayerId: 1, VisitorPlayerId: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || routes.refresh.Load() != 1 || routes.resolve.Load() != 2 {
		t.Fatalf("owner/refresh/resolve calls = %d/%d/%d",
			calls.Load(), routes.refresh.Load(), routes.resolve.Load())
	}
}
