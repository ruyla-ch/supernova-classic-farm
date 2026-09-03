package visit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	plotv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/plot"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	"github.com/Wriosley/supernova-classic-farm/server/internal/player"
)

type recordingPublisher struct {
	mu        sync.Mutex
	envelopes []*wsv1.WsEnvelope
	delivered chan struct{}
}

func (p *recordingPublisher) Forward(_ context.Context, envelope *wsv1.WsEnvelope) error {
	p.mu.Lock()
	p.envelopes = append(p.envelopes, envelope)
	p.mu.Unlock()
	select {
	case p.delivered <- struct{}{}:
	default:
	}
	return nil
}

func TestFarmChangeDispatcherPushesOwnerAndOnlyActiveVisitors(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	registry := NewRegistry(func() time.Time { return now })
	activeID, _, _ := registry.EnterOwner(20, 10, "active")
	expiredID, _, _ := registry.EnterOwner(20, 11, "expired")
	exitedID, _, _ := registry.EnterOwner(20, 12, "exited")
	now = now.Add(VisitTTL / 2)
	if _, err := registry.RefreshOwner(20, 10, activeID); err != nil {
		t.Fatal(err)
	}
	if err := registry.ExitOwner(20, 12, exitedID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(VisitTTL / 2)

	publisher := &recordingPublisher{delivered: make(chan struct{}, 1)}
	dispatcher := newFarmChangeDispatcher(registry, publisher, nil, 4, 1, time.Second)
	originalOwnerPlot := &wsv1.PlotView{
		PlotId: 3, PlotState: plotv1.PlotState_MATURE, HarvestableQuantity: 2,
	}
	originalPublicPlot := &wsv1.PublicPlotView{
		PlotId: 3, PlotState: plotv1.PlotState_MATURE, HarvestableQuantity: 2,
	}
	dispatcher.ForwardFarmChange(player.FarmChangeEvent{
		OwnerPlayerID:     20,
		OwnerEpoch:        2,
		OwnerPlayerSeq:    9,
		ServerTimeMS:      now.UnixMilli(),
		Reason:            reasonv1.StateChangeReason_FRIEND_STEAL,
		CausedByRequestID: "steal-request",
		OwnerPlotUpserts:  []*wsv1.PlotView{originalOwnerPlot},
		PublicPlotUpserts: []*wsv1.PublicPlotView{originalPublicPlot},
	})
	originalOwnerPlot.PlotId = 98
	originalPublicPlot.PlotId = 99
	select {
	case <-publisher.delivered:
	case <-time.After(time.Second):
		t.Fatal("active visitor did not receive push")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)

	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if len(publisher.envelopes) != 2 {
		t.Fatalf("push count=%d, want 2", len(publisher.envelopes))
	}
	ownerEnvelope := publisher.envelopes[0]
	ownerPush := ownerEnvelope.GetPlayerStateChangedPush()
	if ownerEnvelope.TargetPlayerId != 20 ||
		ownerEnvelope.GetStateVersion().GetOwnerEpoch() != 2 ||
		ownerEnvelope.GetStateVersion().GetPlayerSeq() != 9 ||
		ownerPush == nil ||
		ownerPush.GetReason() != reasonv1.StateChangeReason_FRIEND_STEAL ||
		ownerPush.GetCausedByRequestId() != "steal-request" ||
		len(ownerPush.GetPatch().GetPlotUpserts()) != 1 ||
		ownerPush.GetPatch().GetPlotUpserts()[0].GetPlotId() != 3 ||
		ownerPush.GetPatch().GetPlotUpserts()[0].GetHarvestableQuantity() != 2 ||
		ownerEnvelope.GetFriendFarmChangedPush() != nil {
		t.Fatalf("unexpected owner push: %+v", ownerEnvelope)
	}
	visitorEnvelope := publisher.envelopes[1]
	visitorPush := visitorEnvelope.GetFriendFarmChangedPush()
	if visitorEnvelope.TargetPlayerId != 10 || visitorEnvelope.StateVersion != nil ||
		visitorPush == nil || visitorPush.OwnerPlayerId != 20 ||
		string(visitorPush.VisitId) != string(activeID) ||
		visitorPush.OwnerStateVersion.GetOwnerEpoch() != 2 ||
		visitorPush.OwnerStateVersion.GetPlayerSeq() != 9 ||
		len(visitorPush.PlotUpserts) != 1 ||
		visitorPush.PlotUpserts[0].GetPlotId() != 3 ||
		visitorEnvelope.GetPlayerStateChangedPush() != nil {
		t.Fatalf("unexpected visitor push: %+v", visitorEnvelope)
	}
	if err := registry.ValidateOwner(20, 11, expiredID); !errors.Is(err, ErrVisitNotFound) {
		t.Fatalf("expired visitor was not pruned: %v", err)
	}
}

func TestFarmChangeDispatcherSkipsOwnerForMaturity(t *testing.T) {
	registry := NewRegistry(time.Now)
	visitID, _, err := registry.EnterOwner(20, 10, "active")
	if err != nil {
		t.Fatal(err)
	}
	publisher := &recordingPublisher{delivered: make(chan struct{}, 1)}
	dispatcher := newFarmChangeDispatcher(registry, publisher, nil, 4, 1, time.Second)
	dispatcher.ForwardFarmChange(player.FarmChangeEvent{
		OwnerPlayerID:     20,
		OwnerEpoch:        1,
		OwnerPlayerSeq:    3,
		ServerTimeMS:      time.Now().UnixMilli(),
		Reason:            reasonv1.StateChangeReason_MATURED,
		OwnerPlotUpserts:  []*wsv1.PlotView{{PlotId: 1, PlotState: plotv1.PlotState_MATURE}},
		PublicPlotUpserts: []*wsv1.PublicPlotView{{PlotId: 1, PlotState: plotv1.PlotState_MATURE}},
	})
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)

	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if len(publisher.envelopes) != 1 {
		t.Fatalf("maturity dispatcher pushes=%d, want visitor only", len(publisher.envelopes))
	}
	envelope := publisher.envelopes[0]
	if envelope.TargetPlayerId != 10 ||
		envelope.GetFriendFarmChangedPush() == nil ||
		string(envelope.GetFriendFarmChangedPush().GetVisitId()) != string(visitID) {
		t.Fatalf("unexpected maturity visitor push: %+v", envelope)
	}
}

func TestFarmChangeDispatcherFansRegularMutationToOwnerAndVisitor(t *testing.T) {
	registry := NewRegistry(time.Now)
	if _, _, err := registry.EnterOwner(20, 10, "active"); err != nil {
		t.Fatal(err)
	}
	publisher := &recordingPublisher{delivered: make(chan struct{}, 2)}
	dispatcher := newFarmChangeDispatcher(registry, publisher, nil, 4, 1, time.Second)
	dispatcher.ForwardFarmChange(player.FarmChangeEvent{
		OwnerPlayerID:     20,
		OwnerEpoch:        1,
		OwnerPlayerSeq:    2,
		ServerTimeMS:      time.Now().UnixMilli(),
		Reason:            reasonv1.StateChangeReason_PLANT,
		CausedByRequestID: "plant-request",
		OwnerPlotUpserts: []*wsv1.PlotView{{
			PlotId: 1, PlotState: plotv1.PlotState_GROWING,
		}},
		PublicPlotUpserts: []*wsv1.PublicPlotView{{
			PlotId: 1, PlotState: plotv1.PlotState_GROWING,
		}},
	})
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)

	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if len(publisher.envelopes) != 2 ||
		publisher.envelopes[0].TargetPlayerId != 20 ||
		publisher.envelopes[0].GetPlayerStateChangedPush().GetReason() !=
			reasonv1.StateChangeReason_PLANT ||
		publisher.envelopes[1].TargetPlayerId != 10 ||
		publisher.envelopes[1].GetFriendFarmChangedPush() == nil ||
		publisher.envelopes[0].GetStateVersion().GetPlayerSeq() !=
			publisher.envelopes[1].GetFriendFarmChangedPush().
				GetOwnerStateVersion().GetPlayerSeq() {
		t.Fatalf("regular mutation fanout=%+v", publisher.envelopes)
	}
}

type blockingPublisher struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingPublisher) Forward(ctx context.Context, _ *wsv1.WsEnvelope) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestFarmChangeDispatcherDropsNewestWhenQueueIsFull(t *testing.T) {
	registry := NewRegistry(time.Now)
	if _, _, err := registry.EnterOwner(20, 10, "active"); err != nil {
		t.Fatal(err)
	}
	publisher := &blockingPublisher{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	dispatcher := newFarmChangeDispatcher(registry, publisher, nil, 1, 1, time.Second)
	event := player.FarmChangeEvent{
		OwnerPlayerID:     20,
		OwnerEpoch:        1,
		OwnerPlayerSeq:    1,
		ServerTimeMS:      time.Now().UnixMilli(),
		Reason:            reasonv1.StateChangeReason_PLANT,
		OwnerPlotUpserts:  []*wsv1.PlotView{{PlotId: 1}},
		PublicPlotUpserts: []*wsv1.PublicPlotView{{PlotId: 1}},
	}
	dispatcher.ForwardFarmChange(event)
	<-publisher.started
	dispatcher.ForwardFarmChange(event)
	dispatcher.ForwardFarmChange(event)
	if _, _, dropped := dispatcher.Stats(); dropped != 1 {
		t.Fatalf("dropped=%d, want 1", dropped)
	}
	close(publisher.release)
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)
}
