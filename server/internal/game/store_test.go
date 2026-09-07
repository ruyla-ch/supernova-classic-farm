package game

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryMailboxWelcomeAndOwnership(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	playerA := Account{ID: "player-a", Username: "alice"}
	playerB := Account{ID: "player-b", Username: "bob"}
	if err := store.Create(ctx, playerA, NewState(playerA.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, playerB, NewState(playerB.ID)); err != nil {
		t.Fatal(err)
	}

	mails, err := store.ListMails(ctx, playerA.ID)
	if err != nil || len(mails) != 1 || mails[0].Title != WelcomeMailTitle || mails[0].Content != WelcomeMailContent || mails[0].IsRead {
		t.Fatalf("welcome mail mismatch: %+v %v", mails, err)
	}
	updated, err := store.MarkMailRead(ctx, playerA.ID, mails[0].ID)
	if err != nil || len(updated) != 1 || !updated[0].IsRead {
		t.Fatalf("mark read mismatch: %+v %v", updated, err)
	}
	if _, err := store.MarkMailRead(ctx, playerB.ID, mails[0].ID); !errors.Is(err, ErrMailNotFound) {
		t.Fatalf("cross-player read returned %v", err)
	}
}

func TestMemoryMailboxReturnsCopy(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	account := Account{ID: "player-a", Username: "alice"}
	if err := store.Create(ctx, account, NewState(account.ID)); err != nil {
		t.Fatal(err)
	}
	mails, err := store.ListMails(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	mails[0].Title = "被客户端修改"
	again, err := store.ListMails(ctx, account.ID)
	if err != nil || again[0].Title != WelcomeMailTitle {
		t.Fatalf("stored mail was mutated: %+v %v", again, err)
	}
}
