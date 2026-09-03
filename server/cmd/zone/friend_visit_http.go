package main

import (
	"errors"
	"io"
	"net/http"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
	"github.com/Wriosley/supernova-classic-farm/server/internal/player"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"github.com/Wriosley/supernova-classic-farm/server/internal/visit"
	"google.golang.org/protobuf/proto"
)

type friendVisitHTTPHandler struct {
	runtime       *player.Runtime
	registry      *visit.Registry
	authorization ownerAuthorization
	gates         *shardExecutionGates
	now           func() time.Time
}

func (h *friendVisitHTTPHandler) enter(w http.ResponseWriter, r *http.Request) {
	request := &zonev1.EnterVisitorRequest{}
	if !h.decode(w, r, request) {
		return
	}
	if request.RequestId == "" || request.OwnerPlayerId == 0 ||
		request.VisitorPlayerId == 0 ||
		request.OwnerPlayerId == request.VisitorPlayerId {
		writeError(w, http.StatusBadRequest, "INVALID_VISIT")
		return
	}
	unlock, ok := h.authorize(w, r, request.OwnerPlayerId)
	if !ok {
		return
	}
	defer unlock()
	visitID, expiresAtMS, err := h.registry.EnterOwner(
		request.OwnerPlayerId, request.VisitorPlayerId, request.RequestId,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "VISIT_CREATE_FAILED")
		return
	}
	// Register first so every farm change concurrent with snapshot creation is
	// either represented by a Push or covered by the later authoritative
	// mailbox snapshot. The H5 may ignore a pre-response Push, which is safe
	// because the snapshot is captured after this registration.
	snapshot, err := h.runtime.BuildPublicFarmSnapshot(
		r.Context(), request.OwnerPlayerId, mustUintHeader(r, "X-Owner-Epoch"),
	)
	if errors.Is(err, player.ErrNotOwner) {
		h.registry.CancelOwner(request.OwnerPlayerId, request.VisitorPlayerId, visitID)
		writeNotOwner(w, h.authorization, routing.ShardForPlayer(request.OwnerPlayerId))
		return
	}
	if err != nil {
		// Exact-ID cancellation cannot remove a newer replacement visit.
		h.registry.CancelOwner(request.OwnerPlayerId, request.VisitorPlayerId, visitID)
		writeError(w, http.StatusServiceUnavailable, "OWNER_SNAPSHOT_UNAVAILABLE")
		return
	}
	h.writeProto(w, &zonev1.EnterVisitorResponse{
		VisitId: visitID, ExpiresAtMs: expiresAtMS, Snapshot: snapshot,
	})
}

func (h *friendVisitHTTPHandler) heartbeat(w http.ResponseWriter, r *http.Request) {
	request := &zonev1.HeartbeatVisitorRequest{}
	if !h.decode(w, r, request) {
		return
	}
	if request.OwnerPlayerId == 0 ||
		request.VisitorPlayerId == 0 || len(request.VisitId) != visit.VisitIDSize {
		writeError(w, http.StatusBadRequest, "INVALID_VISIT")
		return
	}
	unlock, ok := h.authorize(w, r, request.OwnerPlayerId)
	if !ok {
		return
	}
	defer unlock()
	expiresAtMS, err := h.registry.RefreshOwner(
		request.OwnerPlayerId, request.VisitorPlayerId, request.VisitId,
	)
	response := &zonev1.HeartbeatVisitorResponse{ExpiresAtMs: expiresAtMS}
	if err != nil {
		response.ExpiresAtMs = 0
		response.Error = visitError(err)
	}
	h.writeProto(w, response)
}

func (h *friendVisitHTTPHandler) exit(w http.ResponseWriter, r *http.Request) {
	request := &zonev1.ExitVisitorRequest{}
	if !h.decode(w, r, request) {
		return
	}
	if request.OwnerPlayerId == 0 ||
		request.VisitorPlayerId == 0 || len(request.VisitId) != visit.VisitIDSize {
		writeError(w, http.StatusBadRequest, "INVALID_VISIT")
		return
	}
	unlock, ok := h.authorize(w, r, request.OwnerPlayerId)
	if !ok {
		return
	}
	defer unlock()
	response := &zonev1.ExitVisitorResponse{}
	if err := h.registry.ExitOwner(
		request.OwnerPlayerId, request.VisitorPlayerId, request.VisitId,
	); err != nil {
		response.Error = visitError(err)
	}
	h.writeProto(w, response)
}

func (h *friendVisitHTTPHandler) applySteal(w http.ResponseWriter, r *http.Request) {
	request := &zonev1.ApplyStealRequest{}
	if !h.decode(w, r, request) {
		return
	}
	if request.RequestId == "" || request.OwnerPlayerId == 0 ||
		request.VisitorPlayerId == 0 || request.OwnerPlayerId == request.VisitorPlayerId ||
		len(request.VisitId) != visit.VisitIDSize || request.PlotId == 0 ||
		request.ExpectedCropItemId == 0 || request.ExpectedPlantedAtMs <= 0 ||
		request.ExpectedStealQuantity == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_STEAL")
		return
	}
	unlock, ok := h.authorize(w, r, request.OwnerPlayerId)
	if !ok {
		return
	}
	defer unlock()
	if err := h.registry.ValidateOwner(
		request.OwnerPlayerId, request.VisitorPlayerId, request.VisitId,
	); err != nil {
		h.writeProto(w, &zonev1.ApplyStealResponse{Error: visitError(err)})
		return
	}
	response, err := h.runtime.ApplyStealOnOwner(
		r.Context(), request.OwnerPlayerId,
		mustUintHeader(r, "X-Owner-Epoch"), request,
	)
	if errors.Is(err, player.ErrNotOwner) {
		writeNotOwner(w, h.authorization, routing.ShardForPlayer(request.OwnerPlayerId))
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OWNER_STEAL_UNAVAILABLE")
		return
	}
	h.writeProto(w, response)
}

func (h *friendVisitHTTPHandler) applyPest(w http.ResponseWriter, r *http.Request) {
	request := &zonev1.ApplyPestRequest{}
	if !h.decode(w, r, request) {
		return
	}
	if request.RequestId == "" || request.OwnerPlayerId == 0 ||
		request.VisitorPlayerId == 0 || request.OwnerPlayerId == request.VisitorPlayerId ||
		len(request.VisitId) != visit.VisitIDSize || request.PlotId == 0 ||
		request.PestId == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PEST")
		return
	}
	unlock, ok := h.authorize(w, r, request.OwnerPlayerId)
	if !ok {
		return
	}
	defer unlock()
	if err := h.registry.ValidateOwner(
		request.OwnerPlayerId, request.VisitorPlayerId, request.VisitId,
	); err != nil {
		h.writeProto(w, &zonev1.ApplyPestResponse{Error: visitError(err)})
		return
	}
	response, err := h.runtime.ApplyPestOnOwner(
		r.Context(), request.OwnerPlayerId,
		mustUintHeader(r, "X-Owner-Epoch"), request,
	)
	if errors.Is(err, player.ErrNotOwner) {
		writeNotOwner(w, h.authorization, routing.ShardForPlayer(request.OwnerPlayerId))
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OWNER_PEST_UNAVAILABLE")
		return
	}
	h.writeProto(w, response)
}

func (h *friendVisitHTTPHandler) catchPest(w http.ResponseWriter, r *http.Request) {
	request := &zonev1.CatchPestRequest{}
	if !h.decode(w, r, request) {
		return
	}
	if request.RequestId == "" || request.OwnerPlayerId == 0 ||
		request.VisitorPlayerId == 0 || request.OwnerPlayerId == request.VisitorPlayerId ||
		len(request.VisitId) != visit.VisitIDSize || request.PlotId == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PEST")
		return
	}
	unlock, ok := h.authorize(w, r, request.OwnerPlayerId)
	if !ok {
		return
	}
	defer unlock()
	if err := h.registry.ValidateOwner(
		request.OwnerPlayerId, request.VisitorPlayerId, request.VisitId,
	); err != nil {
		h.writeProto(w, &zonev1.CatchPestResponse{Error: visitError(err)})
		return
	}
	response, err := h.runtime.CatchPestOnOwner(
		r.Context(), request.OwnerPlayerId,
		mustUintHeader(r, "X-Owner-Epoch"), request,
	)
	if errors.Is(err, player.ErrNotOwner) {
		writeNotOwner(w, h.authorization, routing.ShardForPlayer(request.OwnerPlayerId))
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OWNER_PEST_UNAVAILABLE")
		return
	}
	h.writeProto(w, response)
}

func (h *friendVisitHTTPHandler) decode(
	w http.ResponseWriter,
	r *http.Request,
	message proto.Message,
) bool {
	if !isLoopback(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "LOOPBACK_ONLY")
		return false
	}
	if r.Header.Get("Content-Type") != "application/x-protobuf" {
		writeError(w, http.StatusUnsupportedMediaType, "PROTOBUF_REQUIRED")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 || proto.Unmarshal(body, message) != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_PROTOBUF")
		return false
	}
	return true
}

func (h *friendVisitHTTPHandler) authorize(
	w http.ResponseWriter,
	r *http.Request,
	ownerPlayerID uint64,
) (func(), bool) {
	shardValue, err := parseRequiredUintHeader(r, "X-Shard-ID")
	if err != nil || shardValue >= uint64(routing.ShardCount) {
		writeError(w, http.StatusBadRequest, "INVALID_SHARD_ID")
		return nil, false
	}
	shardID := uint32(shardValue)
	if routing.ShardForPlayer(ownerPlayerID) != shardID {
		writeError(w, http.StatusBadRequest, "INVALID_SHARD_ID")
		return nil, false
	}
	ownerZoneID := r.Header.Get("X-Owner-Zone-ID")
	ownerEpoch, epochErr := parseRequiredUintHeader(r, "X-Owner-Epoch")
	routeVersion, routeErr := parseRequiredUintHeader(r, "X-Route-Version")
	if ownerZoneID == "" || epochErr != nil || ownerEpoch == 0 ||
		routeErr != nil || routeVersion == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ROUTE")
		return nil, false
	}
	if h.authorization == nil ||
		h.authorization.Validate(ownerPlayerID, shardID, ownerZoneID, ownerEpoch, h.now()) != nil {
		writeNotOwner(w, h.authorization, shardID)
		return nil, false
	}
	return h.gates.readLock(shardID), true
}

func (h *friendVisitHTTPHandler) writeProto(w http.ResponseWriter, message proto.Message) {
	body, err := proto.Marshal(message)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCODE_FAILED")
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func visitError(err error) *wsv1.Error {
	switch {
	case errors.Is(err, visit.ErrVisitExpired):
		return &wsv1.Error{Code: wsv1.ErrorCode_VISIT_EXPIRED}
	default:
		return &wsv1.Error{Code: wsv1.ErrorCode_VISIT_NOT_FOUND}
	}
}

func mustUintHeader(r *http.Request, name string) uint64 {
	value, _ := parseRequiredUintHeader(r, name)
	return value
}
