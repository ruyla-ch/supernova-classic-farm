package visit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	"github.com/Wriosley/supernova-classic-farm/server/internal/player"
	"google.golang.org/protobuf/proto"
)

const (
	defaultFarmChangeQueueSize = 256
	defaultFarmChangeWorkers   = 2
	defaultFarmChangeTimeout   = 2 * time.Second
)

type PushPublisher interface {
	Forward(context.Context, *wsv1.WsEnvelope) error
}

// FarmChangeDispatcher is the bounded local-prototype fanout path. There is
// exactly one configured loopback Gate push endpoint, so every visitor
// envelope is POSTed to that endpoint; cross-Gate routing is intentionally
// outside this slice.
type FarmChangeDispatcher struct {
	registry  *Registry
	publisher PushPublisher
	logger    *slog.Logger
	timeout   time.Duration
	events    chan player.FarmChangeEvent

	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
	wg     sync.WaitGroup

	published atomic.Uint64
	failed    atomic.Uint64
	dropped   atomic.Uint64
}

func NewFarmChangeDispatcher(
	registry *Registry,
	publisher PushPublisher,
	logger *slog.Logger,
) *FarmChangeDispatcher {
	return newFarmChangeDispatcher(
		registry, publisher, logger,
		defaultFarmChangeQueueSize, defaultFarmChangeWorkers, defaultFarmChangeTimeout,
	)
}

func newFarmChangeDispatcher(
	registry *Registry,
	publisher PushPublisher,
	logger *slog.Logger,
	queueSize, workers int,
	timeout time.Duration,
) *FarmChangeDispatcher {
	if registry == nil || publisher == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if queueSize <= 0 {
		queueSize = defaultFarmChangeQueueSize
	}
	if workers <= 0 {
		workers = defaultFarmChangeWorkers
	}
	if timeout <= 0 {
		timeout = defaultFarmChangeTimeout
	}
	background, cancel := context.WithCancel(context.Background())
	dispatcher := &FarmChangeDispatcher{
		registry: registry, publisher: publisher, logger: logger,
		timeout: timeout, events: make(chan player.FarmChangeEvent, queueSize),
		cancel: cancel,
	}
	for range workers {
		dispatcher.wg.Add(1)
		go dispatcher.worker(background)
	}
	return dispatcher
}

// ForwardFarmChange copies and attempts to enqueue an immutable event. Queue
// saturation drops the newest event and never blocks the Actor command path.
func (d *FarmChangeDispatcher) ForwardFarmChange(event player.FarmChangeEvent) {
	if d == nil || event.OwnerPlayerID == 0 || len(event.PublicPlotUpserts) == 0 {
		return
	}
	event.OwnerPlotUpserts = cloneOwnerPlotViews(event.OwnerPlotUpserts)
	event.PublicPlotUpserts = clonePublicPlotViews(event.PublicPlotUpserts)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		d.dropped.Add(1)
		return
	}
	select {
	case d.events <- event:
	default:
		d.dropped.Add(1)
		d.logger.Warn("farm change queue full; dropping newest event",
			"owner_player_id", event.OwnerPlayerID,
			"queue_capacity", cap(d.events),
		)
	}
}

func (d *FarmChangeDispatcher) worker(background context.Context) {
	defer d.wg.Done()
	for event := range d.events {
		d.deliver(background, event)
	}
}

func (d *FarmChangeDispatcher) deliver(
	background context.Context,
	event player.FarmChangeEvent,
) {
	if event.Reason != reasonv1.StateChangeReason_MATURED &&
		len(event.OwnerPlotUpserts) > 0 {
		push := &wsv1.PlayerStateChangedPush{
			Reason: event.Reason,
			Patch: &wsv1.PlayerStatePatch{
				PlotUpserts: cloneOwnerPlotViews(event.OwnerPlotUpserts),
			},
		}
		if event.CausedByRequestID != "" {
			causedByRequestID := event.CausedByRequestID
			push.CausedByRequestId = &causedByRequestID
		}
		d.publish(background, &wsv1.WsEnvelope{
			ProtocolVersion: player.ProtocolVersion,
			MessageKind:     wsv1.MessageKind_PUSH,
			Action:          wsv1.Action_PLAYER_STATE_CHANGED,
			TargetPlayerId:  event.OwnerPlayerID,
			StateVersion: &wsv1.StateVersion{
				OwnerEpoch: event.OwnerEpoch,
				PlayerSeq:  event.OwnerPlayerSeq,
			},
			ServerTimeMs: event.ServerTimeMS,
			Payload: &wsv1.WsEnvelope_PlayerStateChangedPush{
				PlayerStateChangedPush: push,
			},
		}, event.OwnerPlayerID, 0)
	}
	for _, record := range d.registry.ListVisitors(event.OwnerPlayerID) {
		if record.VisitorPlayerID == 0 || record.VisitorPlayerID == event.OwnerPlayerID {
			continue
		}
		envelope := &wsv1.WsEnvelope{
			ProtocolVersion: player.ProtocolVersion,
			MessageKind:     wsv1.MessageKind_PUSH,
			Action:          wsv1.Action_FRIEND_FARM_CHANGED,
			TargetPlayerId:  record.VisitorPlayerID,
			ServerTimeMs:    event.ServerTimeMS,
			Payload: &wsv1.WsEnvelope_FriendFarmChangedPush{
				FriendFarmChangedPush: &wsv1.FriendFarmChangedPush{
					OwnerPlayerId: event.OwnerPlayerID,
					VisitId:       append([]byte(nil), record.VisitID...),
					OwnerStateVersion: &wsv1.StateVersion{
						OwnerEpoch: event.OwnerEpoch,
						PlayerSeq:  event.OwnerPlayerSeq,
					},
					PlotUpserts: clonePublicPlotViews(event.PublicPlotUpserts),
				},
			},
		}
		d.publish(background, envelope, event.OwnerPlayerID, record.VisitorPlayerID)
	}
}

func (d *FarmChangeDispatcher) publish(
	background context.Context,
	envelope *wsv1.WsEnvelope,
	ownerPlayerID, visitorPlayerID uint64,
) {
	ctx, cancel := context.WithTimeout(background, d.timeout)
	err := d.publisher.Forward(ctx, envelope)
	cancel()
	if err != nil {
		d.failed.Add(1)
		d.logger.Warn("farm change push failed",
			"owner_player_id", ownerPlayerID,
			"visitor_player_id", visitorPlayerID,
			"action", envelope.Action.String(),
			"error", err,
		)
		return
	}
	d.published.Add(1)
}

func cloneOwnerPlotViews(plots []*wsv1.PlotView) []*wsv1.PlotView {
	cloned := make([]*wsv1.PlotView, 0, len(plots))
	for _, plot := range plots {
		if plot == nil {
			continue
		}
		cloned = append(cloned, proto.Clone(plot).(*wsv1.PlotView))
	}
	return cloned
}

func clonePublicPlotViews(plots []*wsv1.PublicPlotView) []*wsv1.PublicPlotView {
	cloned := make([]*wsv1.PublicPlotView, 0, len(plots))
	for _, plot := range plots {
		if plot == nil {
			continue
		}
		cloned = append(cloned, proto.Clone(plot).(*wsv1.PublicPlotView))
	}
	return cloned
}

// Close stops producers, drains queued events until ctx expires, then cancels
// in-flight HTTP and waits for all workers to exit.
func (d *FarmChangeDispatcher) Close(ctx context.Context) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.events)
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		d.cancel()
		<-done
	}
	d.cancel()
}

func (d *FarmChangeDispatcher) Stats() (published, failed, dropped uint64) {
	if d == nil {
		return 0, 0, 0
	}
	return d.published.Load(), d.failed.Load(), d.dropped.Load()
}
