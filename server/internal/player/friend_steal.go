package player

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	datav1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/data"
	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	chapterv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/chapter"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"google.golang.org/protobuf/proto"
)

func (r *Runtime) ApplyStealOnOwner(
	ctx context.Context,
	ownerPlayerID, ownerEpoch uint64,
	request *zonev1.ApplyStealRequest,
) (*zonev1.ApplyStealResponse, error) {
	if request == nil || ownerPlayerID == 0 || ownerEpoch == 0 ||
		request.OwnerPlayerId != ownerPlayerID ||
		request.VisitorPlayerId == 0 || request.RequestId == "" {
		return nil, errors.New("invalid owner steal request")
	}
	requestID, err := parseRequestID(request.RequestId)
	if err != nil {
		return &zonev1.ApplyStealResponse{
			Error: &wsv1.Error{Code: wsv1.ErrorCode_INVALID_ARGUMENT},
		}, nil
	}
	shardID := routing.ShardForPlayer(ownerPlayerID)
	r.shardLocks[shardID].RLock()
	defer r.shardLocks[shardID].RUnlock()
	a, err := r.actorFor(ctx, ownerPlayerID, ownerEpoch)
	if err != nil {
		return nil, err
	}
	fingerprint := stealFingerprint(
		request.VisitorPlayerId, request.OwnerPlayerId, request.RequestId,
		request.PlotId, request.ExpectedCropItemId,
		request.ExpectedPlantedAtMs, request.ExpectedStealQuantity,
	)
	var response *zonev1.ApplyStealResponse
	var dirty bool
	var revision uint64
	var executionErr error
	var maturityEvents []MaturityEvent
	var farmEvent *FarmChangeEvent
	err = a.mailbox.Do(ctx, func() {
		publicChangedPlotIDs := make(map[uint32]struct{})
		maturityPlotIDs := make(map[uint32]struct{})
		mutationPlotIDs := make(map[uint32]struct{})
		eventReason := reasonv1.StateChangeReason_STATE_CHANGE_REASON_UNSPECIFIED
		causedByRequestID := ""
		defer func() {
			ownerPlotIDs := maturityPlotIDs
			if len(mutationPlotIDs) > 0 {
				ownerPlotIDs = mutationPlotIDs
			}
			farmEvent = captureFarmChange(
				a.state, r.now(), eventReason, causedByRequestID,
				ownerPlotIDs, publicChangedPlotIDs,
			)
		}()
		var maturityErr error
		maturityEvents, maturityErr = a.state.materializeDueMaturities(r.now())
		if maturityErr != nil {
			executionErr = maturityErr
			return
		}
		if len(maturityEvents) > 0 {
			dirty = true
			revision = a.state.CheckpointRevision
			eventReason = reasonv1.StateChangeReason_MATURED
			for _, event := range maturityEvents {
				plotID := event.Plot.GetPlotId()
				maturityPlotIDs[plotID] = struct{}{}
				publicChangedPlotIDs[plotID] = struct{}{}
			}
		}
		for _, stored := range a.state.RecentResults {
			if stored.CallerPlayerId != request.VisitorPlayerId ||
				!bytes.Equal(stored.RequestId, requestID) {
				continue
			}
			if !matchesStealResult(stored, ownerPlayerID, fingerprint) {
				response = &zonev1.ApplyStealResponse{
					Error: &wsv1.Error{Code: wsv1.ErrorCode_REQUEST_ID_CONFLICT},
				}
				return
			}
			response = replayOwnerSteal(stored)
			response.Replayed = true
			return
		}
		plot := a.state.Plots[request.PlotId]
		if plot == nil {
			response = &zonev1.ApplyStealResponse{
				Error: &wsv1.Error{Code: wsv1.ErrorCode_PLOT_NOT_FOUND},
			}
			return
		}
		if !CanSteal(plot) ||
			plot.CropItemID != request.ExpectedCropItemId ||
			plot.PlantedAtMS != request.ExpectedPlantedAtMs ||
			plot.StealQuantity != request.ExpectedStealQuantity ||
			visitorAlreadyStole(plot, request.VisitorPlayerId) {
			response = &zonev1.ApplyStealResponse{
				Error: &wsv1.Error{Code: wsv1.ErrorCode_STEAL_NOT_AVAILABLE},
			}
			return
		}
		plot.StealCount++
		plot.StolenQuantity += plot.StealQuantity
		plot.StolenVisitorPlayerIDs = append(
			plot.StolenVisitorPlayerIDs, request.VisitorPlayerId,
		)
		a.state.PlayerSeq++
		a.state.CheckpointRevision++
		a.state.UpdatedAtMS = r.now().UnixMilli()
		mutationPlotIDs[plot.ID] = struct{}{}
		publicChangedPlotIDs[plot.ID] = struct{}{}
		eventReason = reasonv1.StateChangeReason_FRIEND_STEAL
		causedByRequestID = request.RequestId
		response = &zonev1.ApplyStealResponse{
			CropItemId: plot.CropItemID, StolenQuantity: plot.StealQuantity,
			OwnerPlot: publicPlotView(plot),
		}
		body, _ := proto.MarshalOptions{Deterministic: true}.Marshal(response)
		a.state.appendResult(&datav1.IdempotencyResultRecord{
			CallerPlayerId: request.VisitorPlayerId, RequestId: requestID,
			FingerprintSchemaVersion: idempotencyFingerprintSchemaVersion,
			ProtocolVersion:          ProtocolVersion, Action: uint32(wsv1.Action_STEAL_FRIEND_CROP),
			TargetPlayerId: ownerPlayerID, PayloadFingerprintSha256: fingerprint[:],
			CompletedAtMs: r.now().UnixMilli(), Success: true,
			ResultOwnerEpoch: a.state.OwnerEpoch, ResultPlayerSeq: a.state.PlayerSeq,
			ResponsePayloadType: uint32(wsv1.Action_STEAL_FRIEND_CROP),
			ResponsePayload:     body,
		}, r.now())
		dirty = true
		revision = a.state.CheckpointRevision
	})
	if err != nil {
		return nil, err
	}
	if executionErr != nil {
		return nil, executionErr
	}
	if dirty {
		r.markDirty(ownerPlayerID, revision)
	}
	if len(maturityEvents) > 0 {
		_ = r.forwardMaturityEvents(ctx, maturityEvents)
	}
	r.forwardFarmChange(farmEvent)
	return response, nil
}

func (r *Runtime) ExecuteFriendSteal(
	ctx context.Context,
	visitorPlayerID, ownerEpoch uint64,
	request *wsv1.WsEnvelope,
	callOwner func(
		context.Context,
		*zonev1.ApplyStealRequest,
	) (*zonev1.ApplyStealResponse, error),
) (*wsv1.WsEnvelope, error) {
	// The Zone command handler holds its shard execution read gate for this
	// whole call. Do not hold Runtime.shardLocks across the owner HTTP request:
	// a same-shard owner call could otherwise deadlock behind a queued writer.
	if request == nil || callOwner == nil ||
		request.TargetPlayerId != visitorPlayerID ||
		request.GetStealFriendCropRequest() == nil {
		return nil, errors.New("invalid visitor steal request")
	}
	requestID, err := parseRequestID(request.RequestId)
	if err != nil {
		return &wsv1.WsEnvelope{
			ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_RESPONSE,
			Action: request.Action, RequestId: request.RequestId,
			TargetPlayerId: request.TargetPlayerId, ServerTimeMs: r.now().UnixMilli(),
			Error: &wsv1.Error{Code: wsv1.ErrorCode_INVALID_ARGUMENT},
		}, nil
	}
	payload := request.GetStealFriendCropRequest()
	fingerprint := stealFingerprint(
		visitorPlayerID, payload.OwnerPlayerId, request.RequestId,
		payload.PlotId, payload.ExpectedCropItemId,
		payload.ExpectedPlantedAtMs, payload.ExpectedStealQuantity,
	)
	a, err := r.actorFor(ctx, visitorPlayerID, ownerEpoch)
	if err != nil {
		return nil, err
	}
	var response *wsv1.WsEnvelope
	var dirty bool
	var revision uint64
	var executionErr error
	err = a.mailbox.Do(ctx, func() {
		for _, stored := range a.state.RecentResults {
			if stored.CallerPlayerId != visitorPlayerID ||
				!bytes.Equal(stored.RequestId, requestID) {
				continue
			}
			if !matchesStealResult(stored, visitorPlayerID, fingerprint) {
				response = errorEnvelope(request, a.state, r.now(),
					&wsv1.Error{Code: wsv1.ErrorCode_REQUEST_ID_CONFLICT})
				return
			}
			response = replayVisitorSteal(request, stored, r.now())
			return
		}
		currentQuantity := a.state.Inventory[payload.ExpectedCropItemId]
		if currentQuantity == 0 && len(a.state.Inventory) >= inventoryTypeLimit {
			response = errorEnvelope(request, a.state, r.now(),
				&wsv1.Error{Code: wsv1.ErrorCode_INVENTORY_TYPE_LIMIT})
			return
		}
		if uint64(currentQuantity)+uint64(payload.ExpectedStealQuantity) >
			uint64(inventoryStackLimit) {
			response = errorEnvelope(request, a.state, r.now(),
				&wsv1.Error{Code: wsv1.ErrorCode_INVENTORY_STACK_LIMIT})
			return
		}
		ownerCtx, cancelOwner := context.WithTimeout(ctx, 2*time.Second)
		ownerResponse, callErr := callOwner(ownerCtx, &zonev1.ApplyStealRequest{
			RequestId:     request.RequestId,
			OwnerPlayerId: payload.OwnerPlayerId, VisitorPlayerId: visitorPlayerID,
			VisitId: payload.VisitId, PlotId: payload.PlotId,
			ExpectedCropItemId:    payload.ExpectedCropItemId,
			ExpectedPlantedAtMs:   payload.ExpectedPlantedAtMs,
			ExpectedStealQuantity: payload.ExpectedStealQuantity,
		})
		cancelOwner()
		if callErr != nil {
			executionErr = callErr
			return
		}
		if ownerResponse == nil {
			executionErr = errors.New("owner returned no steal response")
			return
		}
		if ownerResponse.Error != nil {
			response = errorEnvelope(request, a.state, r.now(), ownerResponse.Error)
			return
		}
		if ownerResponse.CropItemId != payload.ExpectedCropItemId ||
			ownerResponse.StolenQuantity != payload.ExpectedStealQuantity ||
			ownerResponse.OwnerPlot == nil {
			executionErr = errors.New("owner returned inconsistent steal result")
			return
		}
		newQuantity := currentQuantity + ownerResponse.StolenQuantity
		a.state.Inventory[ownerResponse.CropItemId] = newQuantity
		incrementStealTask(a.state)
		a.state.PlayerSeq++
		a.state.CheckpointRevision++
		a.state.UpdatedAtMS = r.now().UnixMilli()
		result := &wsv1.StealFriendCropResponse{
			CropItemId:     ownerResponse.CropItemId,
			StolenQuantity: ownerResponse.StolenQuantity,
			VisitorPatch: &wsv1.PlayerStatePatch{
				InventoryUpserts: []*wsv1.ItemStackView{{
					ItemId: ownerResponse.CropItemId, Quantity: newQuantity,
				}},
				CurrentChapter: a.state.Snapshot().CurrentChapter,
			},
			OwnerPlot: ownerResponse.OwnerPlot,
		}
		body, _ := proto.MarshalOptions{Deterministic: true}.Marshal(result)
		a.state.appendResult(&datav1.IdempotencyResultRecord{
			CallerPlayerId: visitorPlayerID, RequestId: requestID,
			FingerprintSchemaVersion: idempotencyFingerprintSchemaVersion,
			ProtocolVersion:          request.ProtocolVersion, Action: uint32(request.Action),
			TargetPlayerId: visitorPlayerID, PayloadFingerprintSha256: fingerprint[:],
			CompletedAtMs: r.now().UnixMilli(), Success: true,
			ResultOwnerEpoch: a.state.OwnerEpoch, ResultPlayerSeq: a.state.PlayerSeq,
			ResponsePayloadType: uint32(request.Action), ResponsePayload: body,
		}, r.now())
		response = &wsv1.WsEnvelope{
			ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_RESPONSE,
			Action: request.Action, RequestId: request.RequestId,
			TargetPlayerId: request.TargetPlayerId,
			StateVersion: &wsv1.StateVersion{
				OwnerEpoch: a.state.OwnerEpoch, PlayerSeq: a.state.PlayerSeq,
			},
			ServerTimeMs: r.now().UnixMilli(),
			Payload: &wsv1.WsEnvelope_StealFriendCropResponse{
				StealFriendCropResponse: result,
			},
		}
		dirty = true
		revision = a.state.CheckpointRevision
	})
	if err != nil {
		return nil, err
	}
	if executionErr != nil {
		return nil, executionErr
	}
	if dirty {
		r.markDirty(visitorPlayerID, revision)
	}
	return response, nil
}

func visitorAlreadyStole(plot *Plot, visitorPlayerID uint64) bool {
	for _, existing := range plot.StolenVisitorPlayerIDs {
		if existing == visitorPlayerID {
			return true
		}
	}
	return false
}

func incrementStealTask(state *State) {
	if state.ChapterID != developmentNextChapterID ||
		state.Chapter != chapterv1.ChapterStatus_IN_PROGRESS {
		return
	}
	for index := range state.Tasks {
		if state.Tasks[index].ID == StealCropTaskID &&
			state.Tasks[index].Current < state.Tasks[index].Target {
			state.Tasks[index].Current++
			return
		}
	}
}

func stealFingerprint(
	callerPlayerID, ownerPlayerID uint64,
	requestID string,
	plotID, cropItemID uint32,
	plantedAtMS int64,
	quantity uint32,
) [sha256.Size]byte {
	body := make([]byte, 0, 64+len(requestID))
	body = binary.BigEndian.AppendUint32(body, idempotencyFingerprintSchemaVersion)
	body = binary.BigEndian.AppendUint32(body, uint32(wsv1.Action_STEAL_FRIEND_CROP))
	body = binary.BigEndian.AppendUint64(body, callerPlayerID)
	body = binary.BigEndian.AppendUint64(body, ownerPlayerID)
	body = binary.BigEndian.AppendUint32(body, plotID)
	body = binary.BigEndian.AppendUint32(body, cropItemID)
	body = binary.BigEndian.AppendUint64(body, uint64(plantedAtMS))
	body = binary.BigEndian.AppendUint32(body, quantity)
	body = append(body, requestID...)
	return sha256.Sum256(body)
}

func matchesStealResult(
	stored *datav1.IdempotencyResultRecord,
	targetPlayerID uint64,
	fingerprint [sha256.Size]byte,
) bool {
	return stored.FingerprintSchemaVersion == idempotencyFingerprintSchemaVersion &&
		stored.ProtocolVersion == ProtocolVersion &&
		stored.Action == uint32(wsv1.Action_STEAL_FRIEND_CROP) &&
		stored.TargetPlayerId == targetPlayerID &&
		bytes.Equal(stored.PayloadFingerprintSha256, fingerprint[:])
}

func replayOwnerSteal(stored *datav1.IdempotencyResultRecord) *zonev1.ApplyStealResponse {
	response := &zonev1.ApplyStealResponse{}
	if stored.Success && proto.Unmarshal(stored.ResponsePayload, response) == nil {
		return response
	}
	return &zonev1.ApplyStealResponse{
		Error: &wsv1.Error{Code: wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, Retryable: true},
	}
}

func replayVisitorSteal(
	request *wsv1.WsEnvelope,
	stored *datav1.IdempotencyResultRecord,
	now time.Time,
) *wsv1.WsEnvelope {
	response := &wsv1.WsEnvelope{
		ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_RESPONSE,
		Action: request.Action, RequestId: request.RequestId,
		TargetPlayerId: request.TargetPlayerId, Replayed: true,
		StateVersion: &wsv1.StateVersion{
			OwnerEpoch: stored.ResultOwnerEpoch, PlayerSeq: stored.ResultPlayerSeq,
		},
		ServerTimeMs: now.UnixMilli(),
	}
	payload := &wsv1.StealFriendCropResponse{}
	if stored.Success && proto.Unmarshal(stored.ResponsePayload, payload) == nil {
		response.Payload = &wsv1.WsEnvelope_StealFriendCropResponse{
			StealFriendCropResponse: payload,
		}
		return response
	}
	response.Error = &wsv1.Error{
		Code: wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, Retryable: true,
	}
	return response
}
