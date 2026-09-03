package visit

import (
	"context"
	"testing"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
)

type pestFriendChecker struct {
	mutual bool
	calls  int
}

func (c *pestFriendChecker) CheckMutual(context.Context, uint64, uint64) (bool, error) {
	c.calls++
	return c.mutual, nil
}

type pestOwnerClient struct {
	applyCalls int
	catchCalls int
}

func (*pestOwnerClient) EnterOwner(context.Context, *zonev1.EnterVisitorRequest) (*zonev1.EnterVisitorResponse, error) {
	return &zonev1.EnterVisitorResponse{}, nil
}
func (*pestOwnerClient) HeartbeatOwner(context.Context, *zonev1.HeartbeatVisitorRequest) (*zonev1.HeartbeatVisitorResponse, error) {
	return &zonev1.HeartbeatVisitorResponse{}, nil
}
func (*pestOwnerClient) ExitOwner(context.Context, *zonev1.ExitVisitorRequest) (*zonev1.ExitVisitorResponse, error) {
	return &zonev1.ExitVisitorResponse{}, nil
}
func (*pestOwnerClient) ApplySteal(context.Context, *zonev1.ApplyStealRequest) (*zonev1.ApplyStealResponse, error) {
	return &zonev1.ApplyStealResponse{}, nil
}
func (c *pestOwnerClient) ApplyPest(_ context.Context, request *zonev1.ApplyPestRequest) (*zonev1.ApplyPestResponse, error) {
	c.applyCalls++
	return &zonev1.ApplyPestResponse{
		OwnerPlot: &wsv1.PublicPlotView{PlotId: request.PlotId, PestActive: true},
	}, nil
}
func (c *pestOwnerClient) CatchPest(_ context.Context, request *zonev1.CatchPestRequest) (*zonev1.CatchPestResponse, error) {
	c.catchCalls++
	return &zonev1.CatchPestResponse{
		OwnerPlot: &wsv1.PublicPlotView{PlotId: request.PlotId},
	}, nil
}

type unusedStealExecutor struct{}

func (unusedStealExecutor) ExecuteFriendSteal(
	context.Context,
	uint64,
	uint64,
	*wsv1.WsEnvelope,
	func(context.Context, *zonev1.ApplyStealRequest) (*zonev1.ApplyStealResponse, error),
) (*wsv1.WsEnvelope, error) {
	return nil, nil
}

func TestPestVisitRequiresActiveVisitAndMutualFriend(t *testing.T) {
	registry := NewRegistry(time.Now)
	friends := &pestFriendChecker{mutual: true}
	owners := &pestOwnerClient{}
	service, err := NewService(registry, friends, owners, unusedStealExecutor{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request := applyPestEnvelope(10, 20, make([]byte, VisitIDSize))
	response, err := service.Handle(context.Background(), 10, 1, request)
	if err != nil || response.Error.GetCode() != wsv1.ErrorCode_VISIT_NOT_FOUND ||
		friends.calls != 0 || owners.applyCalls != 0 {
		t.Fatalf("inactive response=%+v err=%v calls=%d/%d",
			response, err, friends.calls, owners.applyCalls)
	}
	visitID := []byte("0123456789abcdef")
	if err := registry.SetVisitor(10, 20, visitID); err != nil {
		t.Fatal(err)
	}
	friends.mutual = false
	request = applyPestEnvelope(10, 20, visitID)
	response, err = service.Handle(context.Background(), 10, 1, request)
	if err != nil || response.Error.GetCode() != wsv1.ErrorCode_NOT_MUTUAL_FRIEND ||
		owners.applyCalls != 0 {
		t.Fatalf("non-mutual response=%+v err=%v calls=%d", response, err, owners.applyCalls)
	}
	friends.mutual = true
	response, err = service.Handle(context.Background(), 10, 1, request)
	if err != nil || response.Error != nil ||
		!response.GetApplyPestToFriendResponse().GetOwnerPlot().GetPestActive() ||
		owners.applyCalls != 1 {
		t.Fatalf("apply response=%+v err=%v calls=%d", response, err, owners.applyCalls)
	}
}

func applyPestEnvelope(visitorID, ownerID uint64, visitID []byte) *wsv1.WsEnvelope {
	return &wsv1.WsEnvelope{
		ProtocolVersion: 1, MessageKind: wsv1.MessageKind_REQUEST,
		Action:    wsv1.Action_APPLY_PEST_TO_FRIEND,
		RequestId: "00112233-4455-6677-8899-aabbccddeeff", TargetPlayerId: visitorID,
		Payload: &wsv1.WsEnvelope_ApplyPestToFriendRequest{
			ApplyPestToFriendRequest: &wsv1.ApplyPestToFriendRequest{
				OwnerPlayerId: ownerID, VisitId: visitID, PlotId: 1, PestId: 1,
			},
		},
	}
}
