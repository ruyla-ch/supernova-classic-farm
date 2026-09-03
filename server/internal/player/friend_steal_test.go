package player

import (
	"context"
	"testing"
	"time"

	datav1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/data"
	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	chapterv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/chapter"
	plotv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/plot"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
)

func TestDirectStealCreditsVisitorAndReplaysExactlyOnce(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	now := time.UnixMilli(1_800_000_000_000)
	runtime.now = func() time.Time { return now }
	ctx := context.Background()
	const ownerID uint64 = 20
	const visitorID uint64 = 10
	farmChanges := &recordingFarmChangeForwarder{}
	if err := runtime.SetFarmChangeForwarder(farmChanges); err != nil {
		t.Fatal(err)
	}

	owner, err := runtime.actorFor(ctx, ownerID, LocalOwnerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.mailbox.Do(ctx, func() {
		owner.state.Plots[1] = matureStealablePlot()
	}); err != nil {
		t.Fatal(err)
	}
	visitor, err := runtime.actorFor(ctx, visitorID, LocalOwnerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := visitor.mailbox.Do(ctx, func() {
		visitor.state.ChapterID = developmentNextChapterID
		visitor.state.Chapter = chapterv1.ChapterStatus_IN_PROGRESS
		visitor.state.Tasks = []Task{{ID: StealCropTaskID, Target: 1}}
	}); err != nil {
		t.Fatal(err)
	}

	request := stealEnvelope(visitorID, ownerID)
	ownerCalls := 0
	callOwner := func(
		ctx context.Context,
		request *zonev1.ApplyStealRequest,
	) (*zonev1.ApplyStealResponse, error) {
		ownerCalls++
		return runtime.ApplyStealOnOwner(ctx, ownerID, LocalOwnerEpoch, request)
	}
	response, err := runtime.ExecuteFriendSteal(
		ctx, visitorID, LocalOwnerEpoch, request, callOwner,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := response.GetStealFriendCropResponse()
	if result == nil || result.StolenQuantity != 1 ||
		result.VisitorPatch == nil ||
		len(result.VisitorPatch.InventoryUpserts) != 1 ||
		result.VisitorPatch.InventoryUpserts[0].Quantity != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if ownerCalls != 1 {
		t.Fatalf("owner calls=%d", ownerCalls)
	}

	replay, err := runtime.ExecuteFriendSteal(
		ctx, visitorID, LocalOwnerEpoch, request, callOwner,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || ownerCalls != 1 {
		t.Fatalf("replay=%v owner calls=%d", replay.Replayed, ownerCalls)
	}
	farmChanges.mu.Lock()
	if len(farmChanges.events) != 1 ||
		farmChanges.events[0].OwnerPlayerID != ownerID ||
		farmChanges.events[0].OwnerPlayerSeq != 1 ||
		farmChanges.events[0].Reason != reasonv1.StateChangeReason_FRIEND_STEAL ||
		farmChanges.events[0].CausedByRequestID != request.RequestId ||
		len(farmChanges.events[0].OwnerPlotUpserts) != 1 ||
		farmChanges.events[0].OwnerPlotUpserts[0].GetHarvestableQuantity() != 2 ||
		len(farmChanges.events[0].PublicPlotUpserts) != 1 ||
		farmChanges.events[0].PublicPlotUpserts[0].GetStealCount() != 1 {
		t.Fatalf("owner farm changes=%+v", farmChanges.events)
	}
	farmChanges.mu.Unlock()

	var checkpoint *datav1.PlayerCheckpointV1
	var checkpointErr error
	if err := owner.mailbox.Do(ctx, func() {
		plot := owner.state.Plots[1]
		if plot.StolenQuantity != 1 || plot.StealCount != 1 ||
			len(plot.StolenVisitorPlayerIDs) != 1 {
			t.Fatalf("owner plot=%+v", plot)
		}
		checkpoint, checkpointErr = owner.state.Checkpoint()
	}); err != nil {
		t.Fatal(err)
	}
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	recovered, err := StateFromCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	recoveredPlot := recovered.Plots[1]
	if recoveredPlot.StealQuantity != developmentStealQuantity ||
		recoveredPlot.MaxStealTimes != developmentMaxStealTimes ||
		recoveredPlot.ProtectedOwnerYield != developmentProtectedOwnerYield ||
		recoveredPlot.StolenVisitorPlayerIDs[0] != visitorID {
		t.Fatalf("recovered plot=%+v", recoveredPlot)
	}
	if err := visitor.mailbox.Do(ctx, func() {
		if visitor.state.Inventory[developmentCropItemID] != 1 ||
			visitor.state.Tasks[0].Current != 1 {
			t.Fatalf("visitor inventory=%v tasks=%v",
				visitor.state.Inventory, visitor.state.Tasks)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyStealOnOwnerRejectsSameVisitorTwice(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	runtime.now = func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	ctx := context.Background()
	owner, err := runtime.actorFor(ctx, 20, LocalOwnerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.mailbox.Do(ctx, func() {
		owner.state.Plots[1] = matureStealablePlot()
	}); err != nil {
		t.Fatal(err)
	}
	first := ownerStealRequest(10, 20, "00112233-4455-6677-8899-aabbccddeeff")
	if response, err := runtime.ApplyStealOnOwner(
		ctx, 20, LocalOwnerEpoch, first,
	); err != nil || response.Error != nil {
		t.Fatalf("first response=%+v err=%v", response, err)
	}
	second := ownerStealRequest(10, 20, "10112233-4455-6677-8899-aabbccddeeff")
	response, err := runtime.ApplyStealOnOwner(ctx, 20, LocalOwnerEpoch, second)
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil ||
		response.Error.Code != wsv1.ErrorCode_STEAL_NOT_AVAILABLE {
		t.Fatalf("second response=%+v", response)
	}
}

func matureStealablePlot() *Plot {
	return &Plot{
		ID: 1, State: plotv1.PlotState_MATURE,
		CropID: developmentCropID, CropItemID: developmentCropItemID,
		CropConfigVersion:         ServerConfigVersion,
		PlantedAtMS:               1_799_999_000_000,
		MaturityValueScaled9:      developmentMaturityScaled9,
		BaseGrowthRateScaled6:     developmentGrowthRateScaled6,
		BaseYield:                 developmentBaseYield,
		SettledGrowthValueScaled9: developmentMaturityScaled9,
		LastSettledAtMS:           1_800_000_000_000,
		StealQuantity:             developmentStealQuantity,
		MaxStealTimes:             developmentMaxStealTimes,
		ProtectedOwnerYield:       developmentProtectedOwnerYield,
	}
}

func stealEnvelope(visitorID, ownerID uint64) *wsv1.WsEnvelope {
	return &wsv1.WsEnvelope{
		ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_REQUEST,
		Action:         wsv1.Action_STEAL_FRIEND_CROP,
		RequestId:      "00112233-4455-6677-8899-aabbccddeeff",
		TargetPlayerId: visitorID,
		Payload: &wsv1.WsEnvelope_StealFriendCropRequest{
			StealFriendCropRequest: &wsv1.StealFriendCropRequest{
				OwnerPlayerId: ownerID, VisitId: make([]byte, 16), PlotId: 1,
				ExpectedCropItemId:    developmentCropItemID,
				ExpectedPlantedAtMs:   1_799_999_000_000,
				ExpectedStealQuantity: developmentStealQuantity,
			},
		},
	}
}

func ownerStealRequest(visitorID, ownerID uint64, requestID string) *zonev1.ApplyStealRequest {
	return &zonev1.ApplyStealRequest{
		RequestId: requestID, OwnerPlayerId: ownerID, VisitorPlayerId: visitorID,
		VisitId: make([]byte, 16), PlotId: 1,
		ExpectedCropItemId:    developmentCropItemID,
		ExpectedPlantedAtMs:   1_799_999_000_000,
		ExpectedStealQuantity: developmentStealQuantity,
	}
}
