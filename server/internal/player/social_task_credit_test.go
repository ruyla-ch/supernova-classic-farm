package player

import (
	"context"
	"testing"
)

func TestApplyFriendTaskCreditIsIdempotent(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	ctx := context.Background()
	a, err := runtime.actorFor(ctx, 42, LocalOwnerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.mailbox.Do(ctx, func() {
		a.state.Tasks = []Task{{ID: AddFriendTaskID, Target: 1}}
		a.state.CheckpointRevision = 3
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyFriendTaskCredit(ctx, 42, LocalOwnerEpoch); err != nil {
		t.Fatal(err)
	}
	var current, revision uint64
	if err := a.mailbox.Do(ctx, func() {
		current = uint64(a.state.Tasks[0].Current)
		revision = a.state.CheckpointRevision
	}); err != nil {
		t.Fatal(err)
	}
	if current != 1 || revision != 4 {
		t.Fatalf("current=%d revision=%d", current, revision)
	}
	if err := runtime.ApplyFriendTaskCredit(ctx, 42, LocalOwnerEpoch); err != nil {
		t.Fatal(err)
	}
	if err := a.mailbox.Do(ctx, func() {
		current = uint64(a.state.Tasks[0].Current)
		revision = a.state.CheckpointRevision
	}); err != nil {
		t.Fatal(err)
	}
	if current != 1 || revision != 4 {
		t.Fatalf("second credit mutated state current=%d revision=%d", current, revision)
	}
}

func TestApplyFriendTaskCreditNoopsOnChapterOne(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	if err := runtime.ApplyFriendTaskCredit(context.Background(), 7, LocalOwnerEpoch); err != nil {
		t.Fatal(err)
	}
}
