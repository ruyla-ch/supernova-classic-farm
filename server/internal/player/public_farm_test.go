package player

import (
	"context"
	"testing"
)

func TestBuildPublicFarmSnapshotCapturesMailboxStateVersion(t *testing.T) {
	const (
		ownerID    = uint64(42)
		ownerEpoch = uint64(7)
		playerSeq  = uint64(11)
	)
	state := NewDevelopmentState(ownerID)
	state.OwnerEpoch = ownerEpoch
	state.PlayerSeq = playerSeq
	runtime, err := NewRuntimeWithLoader(checkpointLoaderFunc(
		func(context.Context, uint64) (*State, error) {
			return state, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	snapshot, err := runtime.BuildPublicFarmSnapshot(
		context.Background(), ownerID, ownerEpoch,
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GetOwnerPlayerId() != ownerID ||
		snapshot.GetOwnerStateVersion().GetOwnerEpoch() != ownerEpoch ||
		snapshot.GetOwnerStateVersion().GetPlayerSeq() != playerSeq {
		t.Fatalf("snapshot version mismatch: %+v", snapshot)
	}
}
