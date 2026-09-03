package player

import (
	"context"
	"testing"
	"time"

	datav1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/data"
	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
)

func TestDevelopmentPestConfigAndValidation(t *testing.T) {
	config := NewDevelopmentConfigSnapshot()
	pest, ok := config.Pest(1)
	if !ok || !pest.Enabled || pest.ConfigVersion != ServerConfigVersion ||
		pest.ModifierScaled6 != -300_000 || pest.DurationMS != 120_000 {
		t.Fatalf("development pest = %+v, exists=%v", pest, ok)
	}
	valid := PestConfig{
		PestID: 1, ConfigVersion: 1, ModifierScaled6: -300_000,
		DurationMS: 120_000, Enabled: true,
	}
	for _, pests := range [][]PestConfig{
		{{PestID: 1, ConfigVersion: 1, ModifierScaled6: 1, DurationMS: 1}},
		{{PestID: 1, ConfigVersion: 1, ModifierScaled6: -1_000_000, DurationMS: 1}},
		{valid, valid},
	} {
		if _, err := NewConfigSnapshotWithPests(1, nil, nil, nil, pests, nil, nil); err == nil {
			t.Fatalf("invalid pests accepted: %+v", pests)
		}
	}
}

func TestGrowthSplitsOverlappingFertilizerAndPestExactly(t *testing.T) {
	start := time.UnixMilli(1_800_000_000_000)
	plot := growingPlotAt(start)
	plot.FertilizerEffect = &datav1.TimedEffectRecord{
		EffectKind: datav1.EffectKind_FERTILIZER,
		Modifier:   &datav1.RateDecimal6{ScaledValue: 500_000},
		StartAtMs:  start.UnixMilli(), EndAtMs: start.Add(60 * time.Second).UnixMilli(),
	}
	source := uint64(10)
	plot.PestEffect = &datav1.TimedEffectRecord{
		EffectKind: datav1.EffectKind_PEST, SourcePlayerId: &source,
		Modifier:  &datav1.RateDecimal6{ScaledValue: -300_000},
		StartAtMs: start.Add(20 * time.Second).UnixMilli(),
		EndAtMs:   start.Add(80 * time.Second).UnixMilli(),
	}
	delta, err := growthBetween(plot, start.UnixMilli(), start.Add(80*time.Second).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	// 20s*1.5 + 40s*1.2 + 20s*0.7 = 92 growth units.
	if delta.Int64() != 92_000_000_000 {
		t.Fatalf("overlap growth = %d", delta.Int64())
	}
}

func TestFriendPestSourceRestrictionCheckpointAndPushReplay(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	now := time.UnixMilli(1_800_000_000_000)
	runtime.now = func() time.Time { return now }
	events := &recordingFarmChangeForwarder{}
	if err := runtime.SetFarmChangeForwarder(events); err != nil {
		t.Fatal(err)
	}
	const ownerID, sourceID, otherID = uint64(20), uint64(10), uint64(11)
	actor, err := runtime.actorFor(context.Background(), ownerID, LocalOwnerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := actor.mailbox.Do(context.Background(), func() {
		plot := growingPlotAt(now.Add(-10 * time.Second))
		actor.state.Plots[1] = plot
	}); err != nil {
		t.Fatal(err)
	}
	apply := &zonev1.ApplyPestRequest{
		RequestId:     "00112233-4455-6677-8899-aabbccddeeff",
		OwnerPlayerId: ownerID, VisitorPlayerId: sourceID,
		VisitId: make([]byte, 16), PlotId: 1, PestId: 1,
	}
	first, err := runtime.ApplyPestOnOwner(context.Background(), ownerID, LocalOwnerEpoch, apply)
	if err != nil || first.Error != nil || first.OwnerPlot == nil || !first.OwnerPlot.PestActive {
		t.Fatalf("apply response=%+v err=%v", first, err)
	}
	if first.OwnerPlot.GetEstimatedMatureAtMs() != now.Add(126*time.Second).UnixMilli() {
		t.Fatalf("pest maturity estimate=%d", first.OwnerPlot.GetEstimatedMatureAtMs())
	}
	replay, err := runtime.ApplyPestOnOwner(context.Background(), ownerID, LocalOwnerEpoch, apply)
	if err != nil || !replay.Replayed {
		t.Fatalf("apply replay=%+v err=%v", replay, err)
	}
	if err := actor.mailbox.Do(context.Background(), func() {
		checkpoint, checkpointErr := actor.state.Checkpoint()
		if checkpointErr != nil {
			t.Fatal(checkpointErr)
		}
		recovered, recoverErr := StateFromCheckpoint(checkpoint)
		if recoverErr != nil {
			t.Fatal(recoverErr)
		}
		effect := recovered.Plots[1].PestEffect
		if effect == nil || effect.GetSourcePlayerId() != sourceID ||
			effect.GetEffectItemOrPestId() != 1 {
			t.Fatalf("recovered pest=%+v", effect)
		}
	}); err != nil {
		t.Fatal(err)
	}
	blocked, err := runtime.CatchPestOnOwner(context.Background(), ownerID, LocalOwnerEpoch,
		&zonev1.CatchPestRequest{
			RequestId:     "10112233-4455-6677-8899-aabbccddeeff",
			OwnerPlayerId: ownerID, VisitorPlayerId: sourceID,
			VisitId: make([]byte, 16), PlotId: 1,
		})
	if err != nil || blocked.Error.GetCode() != wsv1.ErrorCode_PEST_SOURCE_FORBIDDEN {
		t.Fatalf("source catch=%+v err=%v", blocked, err)
	}
	caught, err := runtime.CatchPestOnOwner(context.Background(), ownerID, LocalOwnerEpoch,
		&zonev1.CatchPestRequest{
			RequestId:     "20112233-4455-6677-8899-aabbccddeeff",
			OwnerPlayerId: ownerID, VisitorPlayerId: otherID,
			VisitId: make([]byte, 16), PlotId: 1,
		})
	if err != nil || caught.Error != nil || caught.OwnerPlot.GetPestActive() {
		t.Fatalf("other catch=%+v err=%v", caught, err)
	}
	if caught.OwnerPlot.GetEstimatedMatureAtMs() != now.Add(90*time.Second).UnixMilli() {
		t.Fatalf("catch maturity estimate=%d", caught.OwnerPlot.GetEstimatedMatureAtMs())
	}
	events.mu.Lock()
	if len(events.events) != 2 ||
		events.events[0].Reason != reasonv1.StateChangeReason_APPLY_PEST_TO_FRIEND ||
		events.events[1].Reason != reasonv1.StateChangeReason_CATCH_PEST_FOR_FRIEND {
		t.Fatalf("pest farm events=%+v", events.events)
	}
	events.mu.Unlock()
}

func TestOwnerFreeCatchPestRetainsSuccessAndFailure(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	now := time.UnixMilli(1_800_000_000_000)
	runtime.now = func() time.Time { return now }
	const ownerID = uint64(20)
	actor, err := runtime.actorFor(context.Background(), ownerID, LocalOwnerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	source := uint64(10)
	if err := actor.mailbox.Do(context.Background(), func() {
		plot := growingPlotAt(now.Add(-time.Second))
		plot.PestEffect = &datav1.TimedEffectRecord{
			EffectInstanceId: make([]byte, 16), EffectKind: datav1.EffectKind_PEST,
			EffectItemOrPestId: 1, SourcePlayerId: &source, ConfigVersion: 1,
			Modifier:  &datav1.RateDecimal6{ScaledValue: -300_000},
			StartAtMs: now.Add(-time.Second).UnixMilli(), EndAtMs: now.Add(time.Minute).UnixMilli(),
		}
		actor.state.Plots[1] = plot
	}); err != nil {
		t.Fatal(err)
	}
	request := catchPestEnvelope(ownerID, "30112233-4455-6677-8899-aabbccddeeff")
	response, err := runtime.Handle(context.Background(), ownerID, LocalOwnerEpoch, request)
	if err != nil || response.Error != nil || response.GetCatchPestResponse().GetPatch() == nil {
		t.Fatalf("catch response=%+v err=%v", response, err)
	}
	replay, err := runtime.Handle(context.Background(), ownerID, LocalOwnerEpoch, request)
	if err != nil || !replay.Replayed {
		t.Fatalf("catch replay=%+v err=%v", replay, err)
	}
	failureRequest := catchPestEnvelope(ownerID, "40112233-4455-6677-8899-aabbccddeeff")
	failure, err := runtime.Handle(context.Background(), ownerID, LocalOwnerEpoch, failureRequest)
	if err != nil || failure.Error.GetCode() != wsv1.ErrorCode_PEST_NOT_ACTIVE {
		t.Fatalf("catch failure=%+v err=%v", failure, err)
	}
	failureReplay, err := runtime.Handle(context.Background(), ownerID, LocalOwnerEpoch, failureRequest)
	if err != nil || !failureReplay.Replayed ||
		failureReplay.Error.GetCode() != wsv1.ErrorCode_PEST_NOT_ACTIVE {
		t.Fatalf("catch failure replay=%+v err=%v", failureReplay, err)
	}
}

func TestOwnerCatchAtPestEndBoundaryFailsWithoutPlayerMutation(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	now := time.UnixMilli(1_800_000_000_000)
	runtime.now = func() time.Time { return now }
	const ownerID = uint64(20)
	actor, err := runtime.actorFor(context.Background(), ownerID, LocalOwnerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	source := uint64(10)
	if err := actor.mailbox.Do(context.Background(), func() {
		plot := growingPlotAt(now.Add(-time.Second))
		plot.PestEffect = &datav1.TimedEffectRecord{
			EffectInstanceId: make([]byte, 16), EffectKind: datav1.EffectKind_PEST,
			EffectItemOrPestId: 1, SourcePlayerId: &source, ConfigVersion: 1,
			Modifier:  &datav1.RateDecimal6{ScaledValue: -300_000},
			StartAtMs: now.Add(-time.Second).UnixMilli(), EndAtMs: now.UnixMilli(),
		}
		actor.state.Plots[1] = plot
	}); err != nil {
		t.Fatal(err)
	}
	response, err := runtime.Handle(context.Background(), ownerID, LocalOwnerEpoch,
		catchPestEnvelope(ownerID, "50112233-4455-6677-8899-aabbccddeeff"))
	if err != nil || response.Error.GetCode() != wsv1.ErrorCode_PEST_NOT_ACTIVE ||
		response.StateVersion.GetPlayerSeq() != 0 {
		t.Fatalf("boundary response=%+v err=%v", response, err)
	}
}

func catchPestEnvelope(playerID uint64, requestID string) *wsv1.WsEnvelope {
	return &wsv1.WsEnvelope{
		ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_REQUEST,
		Action: wsv1.Action_CATCH_PEST, RequestId: requestID, TargetPlayerId: playerID,
		Payload: &wsv1.WsEnvelope_CatchPestRequest{
			CatchPestRequest: &wsv1.CatchPestRequest{PlotId: 1},
		},
	}
}
