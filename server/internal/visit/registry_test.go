package visit

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegistryVisitLifecycleAndReplay(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	registry := NewRegistry(func() time.Time { return now })

	visitID, expiresAtMS, err := registry.EnterOwner(20, 10, "request-1")
	if err != nil || len(visitID) != VisitIDSize {
		t.Fatalf("EnterOwner() id=%x err=%v", visitID, err)
	}
	if expiresAtMS != now.Add(VisitTTL).UnixMilli() {
		t.Fatalf("expires_at_ms=%d", expiresAtMS)
	}
	if err := registry.SetVisitor(10, 20, visitID); err != nil {
		t.Fatal(err)
	}
	replayedID, _, err := registry.EnterOwner(20, 10, "request-1")
	if err != nil || string(replayedID) != string(visitID) {
		t.Fatalf("replay id=%x err=%v", replayedID, err)
	}
	if err := registry.ValidateVisitor(10, 20, visitID); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateOwner(20, 10, visitID); err != nil {
		t.Fatal(err)
	}

	now = now.Add(30 * time.Second)
	refreshed, err := registry.RefreshOwner(20, 10, visitID)
	if err != nil || refreshed != now.Add(VisitTTL).UnixMilli() {
		t.Fatalf("RefreshOwner() expires=%d err=%v", refreshed, err)
	}
	if err := registry.ExitOwner(20, 10, visitID); err != nil {
		t.Fatal(err)
	}
	if err := registry.ClearVisitor(10, 20, visitID); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateOwner(20, 10, visitID); !errors.Is(err, ErrVisitNotFound) {
		t.Fatalf("ValidateOwner() err=%v", err)
	}
}

func TestRegistryRejectsExpiredAndForgedVisits(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	registry := NewRegistry(func() time.Time { return now })
	visitID, _, err := registry.EnterOwner(20, 10, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SetVisitor(10, 20, visitID); err != nil {
		t.Fatal(err)
	}
	forged := append([]byte(nil), visitID...)
	forged[0] ^= 0xff
	if err := registry.ValidateOwner(20, 10, forged); !errors.Is(err, ErrVisitNotFound) {
		t.Fatalf("forged ValidateOwner() err=%v", err)
	}

	now = now.Add(VisitTTL)
	if err := registry.ValidateOwner(20, 10, visitID); !errors.Is(err, ErrVisitExpired) {
		t.Fatalf("expired ValidateOwner() err=%v", err)
	}
}

func TestRegistryListVisitorsPrunesExpiredAndCopiesVisitIDs(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	registry := NewRegistry(func() time.Time { return now })
	activeID, _, _ := registry.EnterOwner(20, 10, "active")
	expiredID, _, _ := registry.EnterOwner(20, 11, "expired")
	now = now.Add(VisitTTL / 2)
	if _, err := registry.RefreshOwner(20, 10, activeID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(VisitTTL / 2)

	records := registry.ListVisitors(20)
	if len(records) != 1 || records[0].VisitorPlayerID != 10 ||
		string(records[0].VisitID) != string(activeID) {
		t.Fatalf("records=%+v", records)
	}
	records[0].VisitID[0] ^= 0xff
	if err := registry.ValidateOwner(20, 10, activeID); err != nil {
		t.Fatalf("returned visit ID aliased registry storage: %v", err)
	}
	if err := registry.ValidateOwner(20, 11, expiredID); !errors.Is(err, ErrVisitNotFound) {
		t.Fatalf("expired record was not pruned: %v", err)
	}
}

func TestRegistryListVisitorsConcurrentAccess(t *testing.T) {
	registry := NewRegistry(time.Now)
	var wg sync.WaitGroup
	for visitorID := uint64(1); visitorID <= 32; visitorID++ {
		visitorID := visitorID
		wg.Add(1)
		go func() {
			defer wg.Done()
			visitID, _, err := registry.EnterOwner(100, visitorID, "request")
			if err != nil {
				t.Errorf("EnterOwner(%d): %v", visitorID, err)
				return
			}
			if err := registry.ValidateOwner(100, visitorID, visitID); err != nil {
				t.Errorf("ValidateOwner(%d): %v", visitorID, err)
			}
			_ = registry.ListVisitors(100)
		}()
	}
	wg.Wait()
	if records := registry.ListVisitors(100); len(records) != 32 {
		t.Fatalf("visitor count=%d, want 32", len(records))
	}
}

func TestRegistryCancelOwnerDoesNotDeleteReplacement(t *testing.T) {
	registry := NewRegistry(time.Now)
	oldID, _, err := registry.EnterOwner(20, 10, "old-request")
	if err != nil {
		t.Fatal(err)
	}
	replacementID, _, err := registry.EnterOwner(20, 10, "new-request")
	if err != nil {
		t.Fatal(err)
	}
	if registry.CancelOwner(20, 10, oldID) {
		t.Fatal("stale cancellation removed a replacement visit")
	}
	if err := registry.ValidateOwner(20, 10, replacementID); err != nil {
		t.Fatalf("replacement visit was not preserved: %v", err)
	}
	if !registry.CancelOwner(20, 10, replacementID) {
		t.Fatal("exact visit cancellation did not remove the visit")
	}
	if err := registry.ValidateOwner(20, 10, replacementID); !errors.Is(err, ErrVisitNotFound) {
		t.Fatalf("cancelled visit validation error = %v", err)
	}
}
