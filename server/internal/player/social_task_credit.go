package player

import (
	"context"
	"errors"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

// ApplyFriendTaskCredit advances chapter-two "add friend" progress at most once.
// It is safe to retry: missing task, completed task, or chapter one are no-ops.
func (r *Runtime) ApplyFriendTaskCredit(
	ctx context.Context,
	playerID, ownerEpoch uint64,
) error {
	if playerID == 0 || ownerEpoch == 0 {
		return errors.New("player and owner epoch are required")
	}
	shardID := routing.ShardForPlayer(playerID)
	r.shardLocks[shardID].RLock()
	defer r.shardLocks[shardID].RUnlock()
	a, err := r.actorFor(ctx, playerID, ownerEpoch)
	if err != nil {
		return err
	}
	var dirty bool
	var dirtyRevision uint64
	err = a.mailbox.Do(ctx, func() {
		now := r.now().UTC()
		advanced := false
		for index := range a.state.Tasks {
			if a.state.Tasks[index].ID != AddFriendTaskID {
				continue
			}
			if a.state.Tasks[index].Current >= a.state.Tasks[index].Target {
				return
			}
			a.state.Tasks[index].Current++
			advanced = true
			break
		}
		if !advanced {
			return
		}
		a.state.CheckpointRevision++
		a.state.UpdatedAtMS = now.UnixMilli()
		dirty = true
		dirtyRevision = a.state.CheckpointRevision
	})
	if err != nil {
		return err
	}
	if dirty {
		r.markDirty(playerID, dirtyRevision)
	}
	return nil
}
