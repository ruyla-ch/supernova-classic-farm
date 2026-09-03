package visit

import (
	"context"
	"errors"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
)

type FriendChecker interface {
	CheckMutual(context.Context, uint64, uint64) (bool, error)
}

type OwnerClient interface {
	EnterOwner(context.Context, *zonev1.EnterVisitorRequest) (*zonev1.EnterVisitorResponse, error)
	HeartbeatOwner(context.Context, *zonev1.HeartbeatVisitorRequest) (*zonev1.HeartbeatVisitorResponse, error)
	ExitOwner(context.Context, *zonev1.ExitVisitorRequest) (*zonev1.ExitVisitorResponse, error)
	ApplySteal(context.Context, *zonev1.ApplyStealRequest) (*zonev1.ApplyStealResponse, error)
	ApplyPest(context.Context, *zonev1.ApplyPestRequest) (*zonev1.ApplyPestResponse, error)
	CatchPest(context.Context, *zonev1.CatchPestRequest) (*zonev1.CatchPestResponse, error)
}

type StealExecutor interface {
	ExecuteFriendSteal(
		context.Context,
		uint64,
		uint64,
		*wsv1.WsEnvelope,
		func(context.Context, *zonev1.ApplyStealRequest) (*zonev1.ApplyStealResponse, error),
	) (*wsv1.WsEnvelope, error)
}

type Service struct {
	registry *Registry
	friends  FriendChecker
	owners   OwnerClient
	steals   StealExecutor
	now      func() time.Time
}

func NewService(
	registry *Registry,
	friends FriendChecker,
	owners OwnerClient,
	steals StealExecutor,
	now func() time.Time,
) (*Service, error) {
	if registry == nil || friends == nil || owners == nil || steals == nil {
		return nil, errors.New("visit dependencies are required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		registry: registry, friends: friends, owners: owners, steals: steals, now: now,
	}, nil
}

func (s *Service) Handle(
	ctx context.Context,
	visitorPlayerID uint64,
	ownerEpoch uint64,
	request *wsv1.WsEnvelope,
) (*wsv1.WsEnvelope, error) {
	if request == nil || visitorPlayerID == 0 ||
		request.TargetPlayerId != visitorPlayerID {
		return nil, errors.New("invalid visit request")
	}
	switch request.Action {
	case wsv1.Action_ENTER_FRIEND_FARM:
		return s.enter(ctx, visitorPlayerID, request), nil
	case wsv1.Action_FARM_HEARTBEAT:
		return s.heartbeat(ctx, visitorPlayerID, request), nil
	case wsv1.Action_EXIT_FRIEND_FARM:
		return s.exit(ctx, visitorPlayerID, request), nil
	case wsv1.Action_STEAL_FRIEND_CROP:
		return s.steal(ctx, visitorPlayerID, ownerEpoch, request)
	case wsv1.Action_APPLY_PEST_TO_FRIEND:
		return s.applyPest(ctx, visitorPlayerID, request), nil
	case wsv1.Action_CATCH_PEST_FOR_FRIEND:
		return s.catchPest(ctx, visitorPlayerID, request), nil
	default:
		return nil, errors.New("unsupported visit action")
	}
}

func (s *Service) validateFriendPest(
	ctx context.Context,
	visitorPlayerID, ownerPlayerID uint64,
	visitID []byte,
) *wsv1.Error {
	if ownerPlayerID == 0 || ownerPlayerID == visitorPlayerID ||
		s.registry.ValidateVisitor(visitorPlayerID, ownerPlayerID, visitID) != nil {
		return &wsv1.Error{Code: wsv1.ErrorCode_VISIT_NOT_FOUND}
	}
	mutual, err := s.friends.CheckMutual(ctx, visitorPlayerID, ownerPlayerID)
	if err != nil {
		return &wsv1.Error{Code: wsv1.ErrorCode_SERVICE_UNAVAILABLE, Retryable: true}
	}
	if !mutual {
		return &wsv1.Error{Code: wsv1.ErrorCode_NOT_MUTUAL_FRIEND}
	}
	return nil
}

func (s *Service) applyPest(
	ctx context.Context,
	visitorPlayerID uint64,
	request *wsv1.WsEnvelope,
) *wsv1.WsEnvelope {
	payload := request.GetApplyPestToFriendRequest()
	if payload == nil || payload.PlotId == 0 || payload.PestId == 0 {
		return s.errorResponse(request, wsv1.ErrorCode_INVALID_ARGUMENT, false)
	}
	if failure := s.validateFriendPest(
		ctx, visitorPlayerID, payload.OwnerPlayerId, payload.VisitId,
	); failure != nil {
		return s.ownerErrorResponse(request, failure)
	}
	owner, err := s.owners.ApplyPest(ctx, &zonev1.ApplyPestRequest{
		RequestId: request.RequestId, OwnerPlayerId: payload.OwnerPlayerId,
		VisitorPlayerId: visitorPlayerID, VisitId: payload.VisitId,
		PlotId: payload.PlotId, PestId: payload.PestId,
	})
	if err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, true)
	}
	if owner == nil {
		return s.errorResponse(request, wsv1.ErrorCode_SERVICE_UNAVAILABLE, true)
	}
	response := s.baseResponse(request)
	response.Replayed = owner.Replayed
	if owner.Error != nil {
		response.Error = owner.Error
		return response
	}
	response.Payload = &wsv1.WsEnvelope_ApplyPestToFriendResponse{
		ApplyPestToFriendResponse: &wsv1.ApplyPestToFriendResponse{OwnerPlot: owner.OwnerPlot},
	}
	return response
}

func (s *Service) catchPest(
	ctx context.Context,
	visitorPlayerID uint64,
	request *wsv1.WsEnvelope,
) *wsv1.WsEnvelope {
	payload := request.GetCatchPestForFriendRequest()
	if payload == nil || payload.PlotId == 0 {
		return s.errorResponse(request, wsv1.ErrorCode_INVALID_ARGUMENT, false)
	}
	if failure := s.validateFriendPest(
		ctx, visitorPlayerID, payload.OwnerPlayerId, payload.VisitId,
	); failure != nil {
		return s.ownerErrorResponse(request, failure)
	}
	owner, err := s.owners.CatchPest(ctx, &zonev1.CatchPestRequest{
		RequestId: request.RequestId, OwnerPlayerId: payload.OwnerPlayerId,
		VisitorPlayerId: visitorPlayerID, VisitId: payload.VisitId,
		PlotId: payload.PlotId,
	})
	if err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, true)
	}
	if owner == nil {
		return s.errorResponse(request, wsv1.ErrorCode_SERVICE_UNAVAILABLE, true)
	}
	response := s.baseResponse(request)
	response.Replayed = owner.Replayed
	if owner.Error != nil {
		response.Error = owner.Error
		return response
	}
	response.Payload = &wsv1.WsEnvelope_CatchPestForFriendResponse{
		CatchPestForFriendResponse: &wsv1.CatchPestForFriendResponse{OwnerPlot: owner.OwnerPlot},
	}
	return response
}

func (s *Service) steal(
	ctx context.Context,
	visitorPlayerID, ownerEpoch uint64,
	request *wsv1.WsEnvelope,
) (*wsv1.WsEnvelope, error) {
	payload := request.GetStealFriendCropRequest()
	if payload == nil || payload.OwnerPlayerId == 0 ||
		payload.PlotId == 0 || payload.ExpectedCropItemId == 0 ||
		payload.ExpectedPlantedAtMs <= 0 || payload.ExpectedStealQuantity == 0 ||
		s.registry.ValidateVisitor(
			visitorPlayerID, payload.GetOwnerPlayerId(), payload.GetVisitId(),
		) != nil {
		return s.errorResponse(request, wsv1.ErrorCode_VISIT_NOT_FOUND, false), nil
	}
	mutual, err := s.friends.CheckMutual(
		ctx, visitorPlayerID, payload.OwnerPlayerId,
	)
	if err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_SERVICE_UNAVAILABLE, true), nil
	}
	if !mutual {
		return s.errorResponse(request, wsv1.ErrorCode_NOT_MUTUAL_FRIEND, false), nil
	}
	return s.steals.ExecuteFriendSteal(
		ctx, visitorPlayerID, ownerEpoch, request, s.owners.ApplySteal,
	)
}

func (s *Service) enter(
	ctx context.Context,
	visitorPlayerID uint64,
	request *wsv1.WsEnvelope,
) *wsv1.WsEnvelope {
	payload := request.GetEnterFriendFarmRequest()
	if payload == nil || payload.OwnerPlayerId == 0 ||
		payload.OwnerPlayerId == visitorPlayerID {
		return s.errorResponse(request, wsv1.ErrorCode_INVALID_ARGUMENT, false)
	}
	mutual, err := s.friends.CheckMutual(
		ctx, visitorPlayerID, payload.OwnerPlayerId,
	)
	if err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_SERVICE_UNAVAILABLE, true)
	}
	if !mutual {
		return s.errorResponse(request, wsv1.ErrorCode_NOT_MUTUAL_FRIEND, false)
	}
	if oldOwnerID, oldVisitID, ok := s.registry.CurrentVisitor(visitorPlayerID); ok &&
		oldOwnerID != payload.OwnerPlayerId {
		_, _ = s.owners.ExitOwner(ctx, &zonev1.ExitVisitorRequest{
			OwnerPlayerId: oldOwnerID, VisitorPlayerId: visitorPlayerID,
			VisitId: oldVisitID,
		})
		_ = s.registry.ClearVisitor(visitorPlayerID, oldOwnerID, oldVisitID)
	}
	ownerResponse, err := s.owners.EnterOwner(ctx, &zonev1.EnterVisitorRequest{
		RequestId: request.RequestId, OwnerPlayerId: payload.OwnerPlayerId,
		VisitorPlayerId: visitorPlayerID,
	})
	if err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, true)
	}
	if ownerResponse.Error != nil {
		return s.ownerErrorResponse(request, ownerResponse.Error)
	}
	if len(ownerResponse.VisitId) != VisitIDSize ||
		ownerResponse.ExpiresAtMs <= s.now().UnixMilli() ||
		ownerResponse.Snapshot == nil {
		return s.errorResponse(request, wsv1.ErrorCode_SERVICE_UNAVAILABLE, true)
	}
	if err := s.registry.SetVisitor(
		visitorPlayerID, payload.OwnerPlayerId, ownerResponse.VisitId,
	); err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_SERVICE_UNAVAILABLE, true)
	}
	response := s.baseResponse(request)
	response.Payload = &wsv1.WsEnvelope_EnterFriendFarmResponse{
		EnterFriendFarmResponse: &wsv1.EnterFriendFarmResponse{
			VisitId: ownerResponse.VisitId, ExpiresAtMs: ownerResponse.ExpiresAtMs,
			Snapshot: ownerResponse.Snapshot,
		},
	}
	return response
}

func (s *Service) heartbeat(
	ctx context.Context,
	visitorPlayerID uint64,
	request *wsv1.WsEnvelope,
) *wsv1.WsEnvelope {
	payload := request.GetFarmHeartbeatRequest()
	if payload == nil || s.registry.ValidateVisitor(
		visitorPlayerID, payload.GetOwnerPlayerId(), payload.GetVisitId(),
	) != nil {
		return s.errorResponse(request, wsv1.ErrorCode_VISIT_NOT_FOUND, false)
	}
	ownerResponse, err := s.owners.HeartbeatOwner(ctx, &zonev1.HeartbeatVisitorRequest{
		OwnerPlayerId: payload.OwnerPlayerId, VisitorPlayerId: visitorPlayerID,
		VisitId: payload.VisitId,
	})
	if err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, true)
	}
	if ownerResponse.Error != nil {
		if visitGone(ownerResponse.Error.Code) {
			_ = s.registry.ClearVisitor(visitorPlayerID, payload.OwnerPlayerId, payload.VisitId)
		}
		return s.ownerErrorResponse(request, ownerResponse.Error)
	}
	response := s.baseResponse(request)
	response.Payload = &wsv1.WsEnvelope_FarmHeartbeatResponse{
		FarmHeartbeatResponse: &wsv1.FarmHeartbeatResponse{
			ExpiresAtMs: ownerResponse.ExpiresAtMs,
		},
	}
	return response
}

func (s *Service) exit(
	ctx context.Context,
	visitorPlayerID uint64,
	request *wsv1.WsEnvelope,
) *wsv1.WsEnvelope {
	payload := request.GetExitFriendFarmRequest()
	if payload == nil || s.registry.ValidateVisitor(
		visitorPlayerID, payload.GetOwnerPlayerId(), payload.GetVisitId(),
	) != nil {
		return s.errorResponse(request, wsv1.ErrorCode_VISIT_NOT_FOUND, false)
	}
	ownerResponse, err := s.owners.ExitOwner(ctx, &zonev1.ExitVisitorRequest{
		OwnerPlayerId: payload.OwnerPlayerId, VisitorPlayerId: visitorPlayerID,
		VisitId: payload.VisitId,
	})
	if err != nil {
		return s.errorResponse(request, wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN, true)
	}
	if ownerResponse.Error != nil && !visitGone(ownerResponse.Error.Code) {
		return s.ownerErrorResponse(request, ownerResponse.Error)
	}
	_ = s.registry.ClearVisitor(visitorPlayerID, payload.OwnerPlayerId, payload.VisitId)
	response := s.baseResponse(request)
	response.Payload = &wsv1.WsEnvelope_ExitFriendFarmResponse{
		ExitFriendFarmResponse: &wsv1.ExitFriendFarmResponse{},
	}
	return response
}

func (s *Service) baseResponse(request *wsv1.WsEnvelope) *wsv1.WsEnvelope {
	return &wsv1.WsEnvelope{
		ProtocolVersion: request.ProtocolVersion,
		MessageKind:     wsv1.MessageKind_RESPONSE,
		Action:          request.Action,
		RequestId:       request.RequestId,
		TargetPlayerId:  request.TargetPlayerId,
		ServerTimeMs:    s.now().UnixMilli(),
	}
}

func (s *Service) errorResponse(
	request *wsv1.WsEnvelope,
	code wsv1.ErrorCode,
	retryable bool,
) *wsv1.WsEnvelope {
	response := s.baseResponse(request)
	response.Error = &wsv1.Error{Code: code, Retryable: retryable}
	return response
}

func (s *Service) ownerErrorResponse(
	request *wsv1.WsEnvelope,
	ownerError *wsv1.Error,
) *wsv1.WsEnvelope {
	response := s.baseResponse(request)
	response.Error = ownerError
	return response
}

func visitGone(code wsv1.ErrorCode) bool {
	return code == wsv1.ErrorCode_VISIT_NOT_FOUND ||
		code == wsv1.ErrorCode_VISIT_EXPIRED
}
