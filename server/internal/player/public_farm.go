package player

import (
	"context"
	"errors"
	"sort"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	plotv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/plot"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

func (r *Runtime) BuildPublicFarmSnapshot(
	ctx context.Context,
	ownerPlayerID, ownerEpoch uint64,
) (*wsv1.FarmVisitSnapshot, error) {
	if ownerPlayerID == 0 || ownerEpoch == 0 {
		return nil, errors.New("owner player and epoch are required")
	}
	shardID := routing.ShardForPlayer(ownerPlayerID)
	r.shardLocks[shardID].RLock()
	defer r.shardLocks[shardID].RUnlock()
	a, err := r.actorFor(ctx, ownerPlayerID, ownerEpoch)
	if err != nil {
		return nil, err
	}
	var snapshot *wsv1.FarmVisitSnapshot
	var events []MaturityEvent
	var farmEvent *FarmChangeEvent
	var revision uint64
	var snapshotErr error
	err = a.mailbox.Do(ctx, func() {
		events, snapshotErr = a.state.materializeDueMaturities(r.now())
		if snapshotErr != nil {
			return
		}
		revision = a.state.CheckpointRevision
		if len(events) > 0 {
			plotIDs := make(map[uint32]struct{}, len(events))
			for _, event := range events {
				plotIDs[event.Plot.GetPlotId()] = struct{}{}
			}
			farmEvent = captureFarmChange(
				a.state, r.now(), reasonv1.StateChangeReason_MATURED, "",
				plotIDs, plotIDs,
			)
		}
		plotIDs := make([]uint32, 0, len(a.state.Plots))
		for plotID := range a.state.Plots {
			plotIDs = append(plotIDs, plotID)
		}
		sort.Slice(plotIDs, func(i, j int) bool { return plotIDs[i] < plotIDs[j] })
		snapshot = &wsv1.FarmVisitSnapshot{
			OwnerPlayerId: ownerPlayerID,
			Plots:         make([]*wsv1.PublicPlotView, 0, len(plotIDs)),
			OwnerStateVersion: &wsv1.StateVersion{
				OwnerEpoch: a.state.OwnerEpoch,
				PlayerSeq:  a.state.PlayerSeq,
			},
		}
		for _, plotID := range plotIDs {
			snapshot.Plots = append(snapshot.Plots, publicPlotView(a.state.Plots[plotID]))
		}
	})
	if err != nil {
		return nil, err
	}
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	if len(events) > 0 {
		r.markDirty(ownerPlayerID, revision)
		r.forwardFarmChange(farmEvent)
		if err := r.forwardMaturityEvents(ctx, events); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func publicPlotView(plot *Plot) *wsv1.PublicPlotView {
	if plot == nil {
		return &wsv1.PublicPlotView{}
	}
	view := &wsv1.PublicPlotView{
		PlotId:        plot.ID,
		PlotState:     plot.State,
		CropId:        plot.CropID,
		CropItemId:    plot.CropItemID,
		PlantedAtMs:   plot.PlantedAtMS,
		StealCount:    plot.StealCount,
		CanSteal:      CanSteal(plot),
		StealQuantity: 0,
		PestActive:    plot.PestEffect != nil,
	}
	if plot.EstimatedMatureAtMS != nil {
		view.EstimatedMatureAtMs = *plot.EstimatedMatureAtMS
	}
	if plot.BaseYield >= plot.StolenQuantity {
		view.HarvestableQuantity = plot.BaseYield - plot.StolenQuantity
	}
	if view.CanSteal {
		view.StealQuantity = plot.StealQuantity
	}
	return view
}

func CanSteal(plot *Plot) bool {
	if plot == nil || plot.State != plotv1.PlotState_MATURE ||
		plot.StealQuantity == 0 || plot.MaxStealTimes == 0 ||
		plot.ProtectedOwnerYield == 0 ||
		plot.StealCount >= plot.MaxStealTimes {
		return false
	}
	required := uint64(plot.StolenQuantity) + uint64(plot.StealQuantity) +
		uint64(plot.ProtectedOwnerYield)
	return required <= uint64(plot.BaseYield)
}
