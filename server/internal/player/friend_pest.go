package player

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"

	datav1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/data"
	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	plotv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/plot"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"google.golang.org/protobuf/proto"
)

func (r *Runtime) ApplyPestOnOwner(
	ctx context.Context,
	ownerPlayerID, ownerEpoch uint64,
	request *zonev1.ApplyPestRequest,
) (*zonev1.ApplyPestResponse, error) {
	if request == nil || request.PestId == 0 {
		return nil, errors.New("invalid owner apply pest request")
	}
	result, replayed, failure, err := r.mutateFriendPest(
		ctx, ownerPlayerID, ownerEpoch, request.VisitorPlayerId,
		request.RequestId, request.PlotId, request.PestId, true,
	)
	if err != nil {
		return nil, err
	}
	return &zonev1.ApplyPestResponse{OwnerPlot: result, Replayed: replayed, Error: failure}, nil
}

func (r *Runtime) CatchPestOnOwner(
	ctx context.Context,
	ownerPlayerID, ownerEpoch uint64,
	request *zonev1.CatchPestRequest,
) (*zonev1.CatchPestResponse, error) {
	if request == nil {
		return nil, errors.New("invalid owner catch pest request")
	}
	result, replayed, failure, err := r.mutateFriendPest(
		ctx, ownerPlayerID, ownerEpoch, request.VisitorPlayerId,
		request.RequestId, request.PlotId, 0, false,
	)
	if err != nil {
		return nil, err
	}
	return &zonev1.CatchPestResponse{OwnerPlot: result, Replayed: replayed, Error: failure}, nil
}

func (r *Runtime) mutateFriendPest(
	ctx context.Context,
	ownerPlayerID, ownerEpoch, visitorPlayerID uint64,
	requestIDText string,
	plotID, pestID uint32,
	apply bool,
) (*wsv1.PublicPlotView, bool, *wsv1.Error, error) {
	if ownerPlayerID == 0 || ownerEpoch == 0 || visitorPlayerID == 0 ||
		visitorPlayerID == ownerPlayerID || requestIDText == "" || plotID == 0 {
		return nil, false, nil, errors.New("invalid friend pest request")
	}
	requestID, err := parseRequestID(requestIDText)
	if err != nil {
		return nil, false, &wsv1.Error{Code: wsv1.ErrorCode_INVALID_ARGUMENT}, nil
	}
	action := wsv1.Action_CATCH_PEST_FOR_FRIEND
	reason := reasonv1.StateChangeReason_CATCH_PEST_FOR_FRIEND
	if apply {
		action = wsv1.Action_APPLY_PEST_TO_FRIEND
		reason = reasonv1.StateChangeReason_APPLY_PEST_TO_FRIEND
	}
	fingerprint := friendPestFingerprint(action, visitorPlayerID, ownerPlayerID, plotID, pestID)
	config := r.config.Load()
	if config == nil {
		return nil, false, nil, errors.New("Zone configuration is unavailable")
	}
	shardID := routing.ShardForPlayer(ownerPlayerID)
	r.shardLocks[shardID].RLock()
	defer r.shardLocks[shardID].RUnlock()
	a, err := r.actorFor(ctx, ownerPlayerID, ownerEpoch)
	if err != nil {
		return nil, false, nil, err
	}
	var ownerPlot *wsv1.PublicPlotView
	var responseError *wsv1.Error
	var replayed bool
	var dirty bool
	var revision uint64
	var maturityEvents []MaturityEvent
	var farmEvent *FarmChangeEvent
	var executionErr error
	now := r.now()
	err = a.mailbox.Do(ctx, func() {
		maturityEvents, executionErr = a.state.materializeDueMaturities(now)
		if executionErr != nil {
			return
		}
		maturityIDs := make(map[uint32]struct{}, len(maturityEvents))
		for _, event := range maturityEvents {
			maturityIDs[event.Plot.GetPlotId()] = struct{}{}
		}
		if len(maturityEvents) > 0 {
			dirty = true
			revision = a.state.CheckpointRevision
			farmEvent = captureFarmChange(
				a.state, now, reasonv1.StateChangeReason_MATURED, "",
				maturityIDs, maturityIDs,
			)
		}
		for _, stored := range a.state.RecentResults {
			if stored.CallerPlayerId != visitorPlayerID || !bytes.Equal(stored.RequestId, requestID) {
				continue
			}
			if stored.FingerprintSchemaVersion != idempotencyFingerprintSchemaVersion ||
				stored.ProtocolVersion != ProtocolVersion ||
				stored.Action != uint32(action) ||
				stored.TargetPlayerId != ownerPlayerID ||
				!bytes.Equal(stored.PayloadFingerprintSha256, fingerprint[:]) {
				responseError = &wsv1.Error{Code: wsv1.ErrorCode_REQUEST_ID_CONFLICT}
				return
			}
			replayed = true
			if stored.Success {
				ownerPlot = &wsv1.PublicPlotView{}
				if proto.Unmarshal(stored.ResponsePayload, ownerPlot) != nil {
					ownerPlot = nil
					responseError = &wsv1.Error{Code: wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, Retryable: true}
				}
			} else {
				responseError = &wsv1.Error{}
				if proto.Unmarshal(stored.ErrorPayload, responseError) != nil {
					responseError = &wsv1.Error{Code: wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, Retryable: true}
				}
			}
			return
		}
		storeFailure := func(failure *wsv1.Error) {
			body, _ := proto.MarshalOptions{Deterministic: true}.Marshal(failure)
			a.state.CheckpointRevision++
			a.state.ConfigVersion = config.Version()
			a.state.UpdatedAtMS = now.UnixMilli()
			a.state.appendResult(&datav1.IdempotencyResultRecord{
				CallerPlayerId: visitorPlayerID, RequestId: requestID,
				FingerprintSchemaVersion: idempotencyFingerprintSchemaVersion,
				ProtocolVersion:          ProtocolVersion, Action: uint32(action),
				TargetPlayerId: ownerPlayerID, PayloadFingerprintSha256: fingerprint[:],
				CompletedAtMs: now.UnixMilli(), Success: false,
				ResultOwnerEpoch: a.state.OwnerEpoch, ResultPlayerSeq: a.state.PlayerSeq,
				ResponsePayloadType: uint32(action), ErrorPayload: body,
			}, now)
			responseError = failure
			dirty = true
			revision = a.state.CheckpointRevision
		}
		plot := a.state.Plots[plotID]
		if plot == nil {
			storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_PLOT_NOT_FOUND})
			return
		}
		if plot.State != plotv1.PlotState_GROWING {
			storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_PLOT_STATE_CONFLICT})
			return
		}
		if apply && plot.PestEffect != nil {
			storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_PEST_ALREADY_ACTIVE})
			return
		}
		if !apply && plot.PestEffect == nil {
			storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_PEST_NOT_ACTIVE})
			return
		}
		if !apply && plot.PestEffect.SourcePlayerId != nil &&
			plot.PestEffect.GetSourcePlayerId() == visitorPlayerID {
			storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_PEST_SOURCE_FORBIDDEN})
			return
		}
		var pest PestConfig
		if apply {
			var ok bool
			pest, ok = config.Pest(pestID)
			if !ok {
				storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_CONFIG_UNAVAILABLE, Retryable: true})
				return
			}
			if !pest.Enabled {
				storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_CONFIG_ENTRY_DISABLED})
				return
			}
			if now.UnixMilli() > math.MaxInt64-pest.DurationMS {
				storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_CONFIG_UNAVAILABLE, Retryable: true})
				return
			}
		}
		before := clonePlot(plot)
		matured, settleErr := settleGrowingPlot(plot, now.UnixMilli())
		if settleErr != nil || matured || (!apply && plot.PestEffect == nil) {
			*plot = *before
			storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_PEST_NOT_ACTIVE})
			return
		}
		if apply {
			source := visitorPlayerID
			plot.PestEffect = &datav1.TimedEffectRecord{
				EffectInstanceId: pestEffectID(visitorPlayerID, requestID),
				EffectKind:       datav1.EffectKind_PEST, EffectItemOrPestId: pest.PestID,
				SourcePlayerId: &source, ConfigVersion: pest.ConfigVersion,
				Modifier:  &datav1.RateDecimal6{ScaledValue: pest.ModifierScaled6},
				StartAtMs: now.UnixMilli(), EndAtMs: now.UnixMilli() + pest.DurationMS,
			}
		} else {
			plot.PestEffect = nil
		}
		estimate, estimateErr := estimatePlotMatureAtMS(plot, now.UnixMilli())
		if estimateErr != nil {
			*plot = *before
			storeFailure(&wsv1.Error{Code: wsv1.ErrorCode_CONFIG_UNAVAILABLE, Retryable: true})
			return
		}
		plot.EstimatedMatureAtMS = &estimate
		a.state.PlayerSeq++
		a.state.CheckpointRevision++
		a.state.ConfigVersion = config.Version()
		a.state.UpdatedAtMS = now.UnixMilli()
		ownerPlot = publicPlotView(plot)
		body, _ := proto.MarshalOptions{Deterministic: true}.Marshal(ownerPlot)
		a.state.appendResult(&datav1.IdempotencyResultRecord{
			CallerPlayerId: visitorPlayerID, RequestId: requestID,
			FingerprintSchemaVersion: idempotencyFingerprintSchemaVersion,
			ProtocolVersion:          ProtocolVersion, Action: uint32(action),
			TargetPlayerId: ownerPlayerID, PayloadFingerprintSha256: fingerprint[:],
			CompletedAtMs: now.UnixMilli(), Success: true,
			ResultOwnerEpoch: a.state.OwnerEpoch, ResultPlayerSeq: a.state.PlayerSeq,
			ResponsePayloadType: uint32(action), ResponsePayload: body,
		}, now)
		dirty = true
		revision = a.state.CheckpointRevision
		changed := map[uint32]struct{}{plotID: {}}
		farmEvent = captureFarmChange(a.state, now, reason, requestIDText, changed, changed)
	})
	if err != nil {
		return nil, false, nil, err
	}
	if executionErr != nil {
		return nil, false, nil, executionErr
	}
	if dirty {
		r.markDirty(ownerPlayerID, revision)
	}
	if len(maturityEvents) > 0 {
		_ = r.forwardMaturityEvents(ctx, maturityEvents)
	}
	r.forwardFarmChange(farmEvent)
	return ownerPlot, replayed, responseError, nil
}

func friendPestFingerprint(action wsv1.Action, visitorPlayerID, ownerPlayerID uint64, plotID, pestID uint32) [sha256.Size]byte {
	body := make([]byte, 0, 36)
	body = binary.BigEndian.AppendUint32(body, idempotencyFingerprintSchemaVersion)
	body = binary.BigEndian.AppendUint32(body, uint32(action))
	body = binary.BigEndian.AppendUint64(body, visitorPlayerID)
	body = binary.BigEndian.AppendUint64(body, ownerPlayerID)
	body = binary.BigEndian.AppendUint32(body, plotID)
	body = binary.BigEndian.AppendUint32(body, pestID)
	return sha256.Sum256(body)
}

func pestEffectID(playerID uint64, requestID []byte) []byte {
	body := append([]byte("pest-effect:"), binary.BigEndian.AppendUint64(nil, playerID)...)
	body = append(body, requestID...)
	sum := sha256.Sum256(body)
	id := append([]byte(nil), sum[:16]...)
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}
