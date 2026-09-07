package game

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestOwnerLoopAndReplay(t *testing.T) {
	s := NewMemoryStore()
	account := Account{ID: "1", Username: "alice", PasswordHash: "test"}
	if err := s.Create(context.Background(), account, NewState("1")); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	engine := Engine{Store: s, Now: func() time.Time { return now }}
	run := func(id, action string, data Args) State {
		t.Helper()
		state, err := engine.Execute(context.Background(), "1", Command{RequestID: id, Action: action, Data: data})
		if err != nil {
			t.Fatal(action, err)
		}
		return state
	}
	bought := run("request-0001", "BUY_SEEDS", Args{Quantity: 3})
	if bought.Coins != 4 || bought.Seeds != 3 {
		t.Fatalf("bad buy: %+v", bought)
	}
	replay := run("request-0001", "BUY_SEEDS", Args{Quantity: 3})
	if replay.Coins != 4 {
		t.Fatal("duplicate purchase charged twice")
	}
	_, err := engine.Execute(context.Background(), "1", Command{RequestID: "request-0001", Action: "BUY_SEEDS", Data: Args{Quantity: 1}})
	if !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("conflict: %v", err)
	}
	run("request-0002", "PLANT", Args{PlotID: 1})
	run("request-0003", "APPLY_FERTILIZER", Args{PlotID: 1})
	_, err = engine.Execute(context.Background(), "1", Command{RequestID: "early-harvest", Action: "HARVEST", Data: Args{PlotID: 1}})
	if !errors.Is(err, ErrNotMature) {
		t.Fatalf("early harvest: %v", err)
	}
	now = now.Add(71 * time.Second)
	run("request-0004", "HARVEST", Args{PlotID: 1})
	run("request-0005", "SELL_CROP", Args{Quantity: 3})
	run("request-0006", "CLAIM_CHAPTER_REWARD", Args{})
	final := run("request-0007", "CLEAN_PLOT", Args{PlotID: 1})
	if final.Coins != 29 || final.Seeds != 5 || final.Chapter != 2 || final.Plots[0].Status != "EMPTY" {
		t.Fatalf("bad final state: %+v", final)
	}
}

func TestConcurrentPurchasesCannotOverspend(t *testing.T) {
	store := NewMemoryStore()
	_ = store.Create(context.Background(), Account{ID: "1", Username: "alice"}, NewState("1"))
	engine := Engine{Store: store, Now: time.Now}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = engine.Execute(context.Background(), "1", Command{RequestID: fmt.Sprintf("purchase-%04d", i), Action: "BUY_SEEDS", Data: Args{Quantity: 1}})
		}(i)
	}
	wg.Wait()
	state, err := engine.Execute(context.Background(), "1", Command{RequestID: "snapshot-1", Action: "GET_PLAYER_SNAPSHOT"})
	if err != nil || state.Coins != 0 || state.Seeds != 5 {
		t.Fatalf("overspent: %+v %v", state, err)
	}
}

func TestFailedOperationDoesNotMutateState(t *testing.T) {
	s := NewState("1")
	original := s
	err := apply(&s, "BUY_SEEDS", Args{Quantity: 6}, time.Now())
	if !errors.Is(err, ErrCoins) || s.Coins != original.Coins || s.Seeds != original.Seeds {
		t.Fatalf("mutated failed buy: %+v %v", s, err)
	}
	s.Crops = 199
	s.Plots[0] = Plot{ID: 1, Status: "GROWING", MatureAtMS: 1}
	err = apply(&s, "HARVEST", Args{PlotID: 1}, time.Now())
	if !errors.Is(err, ErrCapacity) || s.Plots[0].Status != "GROWING" {
		t.Fatal("partial harvest", err)
	}
}

func TestCredentials(t *testing.T) {
	if ValidateCredentials("a", "123") == nil {
		t.Fatal("accepted invalid credentials")
	}
	hash, err := HashPassword("student-password")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "student-password" || !VerifyPassword(hash, "student-password") || VerifyPassword(hash, "wrong-password") {
		t.Fatal("password verification failed")
	}
	if VerifyPassword("invalid", "student-password") {
		t.Fatal("accepted malformed hash")
	}
}
