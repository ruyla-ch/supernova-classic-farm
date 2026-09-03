package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
	"github.com/Wriosley/supernova-classic-farm/server/internal/player"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"github.com/Wriosley/supernova-classic-farm/server/internal/visit"
	"google.golang.org/protobuf/proto"
)

func enterVisitorHTTPRequest(t *testing.T, ownerID, visitorID uint64) *http.Request {
	t.Helper()
	body, err := proto.Marshal(&zonev1.EnterVisitorRequest{
		RequestId:       "enter-request",
		OwnerPlayerId:   ownerID,
		VisitorPlayerId: visitorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost, "/internal/v1/friend-visits/enter", bytes.NewReader(body),
	)
	request.RemoteAddr = "127.0.0.1:45678"
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set(
		"X-Shard-ID", strconv.FormatUint(uint64(routing.ShardForPlayer(ownerID)), 10),
	)
	request.Header.Set("X-Owner-Zone-ID", routing.DefaultZoneID)
	request.Header.Set("X-Owner-Epoch", "1")
	request.Header.Set("X-Route-Version", "1")
	return request
}

func TestEnterRegistersBeforeSnapshotAndCancelsFailedSnapshot(t *testing.T) {
	const (
		ownerID   = uint64(20)
		visitorID = uint64(10)
	)
	registry := visit.NewRegistry(time.Now)
	registrationObserved := false
	runtime, err := player.NewRuntimeWithLoader(checkpointLoaderFunc(
		func(context.Context, uint64) (*player.State, error) {
			records := registry.ListVisitors(ownerID)
			registrationObserved = len(records) == 1 &&
				records[0].VisitorPlayerID == visitorID
			return nil, errors.New("snapshot load failed")
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handler := &friendVisitHTTPHandler{
		runtime: runtime, registry: registry,
		authorization: localAuthorization{}, gates: &shardExecutionGates{}, now: time.Now,
	}
	recorder := httptest.NewRecorder()

	handler.enter(recorder, enterVisitorHTTPRequest(t, ownerID, visitorID))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !registrationObserved {
		t.Fatal("owner visit was not registered before snapshot construction")
	}
	if records := registry.ListVisitors(ownerID); len(records) != 0 {
		t.Fatalf("failed snapshot left owner visit registered: %+v", records)
	}
}

func TestApplyPestRequiresOwnerVisitBeforeActorActivation(t *testing.T) {
	const ownerID, visitorID = uint64(20), uint64(10)
	runtime := player.NewRuntime()
	defer runtime.Close()
	registry := visit.NewRegistry(time.Now)
	handler := &friendVisitHTTPHandler{
		runtime: runtime, registry: registry,
		authorization: localAuthorization{}, gates: &shardExecutionGates{}, now: time.Now,
	}
	body, err := proto.Marshal(&zonev1.ApplyPestRequest{
		RequestId:     "00112233-4455-6677-8899-aabbccddeeff",
		OwnerPlayerId: ownerID, VisitorPlayerId: visitorID,
		VisitId: make([]byte, visit.VisitIDSize), PlotId: 1, PestId: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost, "/internal/v1/friend-visits/apply-pest", bytes.NewReader(body),
	)
	request.RemoteAddr = "127.0.0.1:45678"
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Shard-ID", strconv.FormatUint(uint64(routing.ShardForPlayer(ownerID)), 10))
	request.Header.Set("X-Owner-Zone-ID", routing.DefaultZoneID)
	request.Header.Set("X-Owner-Epoch", "1")
	request.Header.Set("X-Route-Version", "1")
	recorder := httptest.NewRecorder()
	handler.applyPest(recorder, request)
	response := &zonev1.ApplyPestResponse{}
	if recorder.Code != http.StatusOK || proto.Unmarshal(recorder.Body.Bytes(), response) != nil ||
		response.Error.GetCode() != 705 {
		t.Fatalf("status=%d response=%+v body=%s", recorder.Code, response, recorder.Body.String())
	}
	if runtime.HasActiveActorsForShard(routing.ShardForPlayer(ownerID)) {
		t.Fatal("unauthorized pest request activated owner Actor")
	}
}

type checkpointLoaderFunc func(context.Context, uint64) (*player.State, error)

func (f checkpointLoaderFunc) Load(ctx context.Context, playerID uint64) (*player.State, error) {
	return f(ctx, playerID)
}
