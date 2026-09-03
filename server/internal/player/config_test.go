package player

import (
	"reflect"
	"testing"
)

func TestConfigSnapshotRejectsInvalidAndDuplicateShopEntries(t *testing.T) {
	tests := []struct {
		name    string
		version uint64
		entries []ShopEntry
	}{
		{name: "missing version"},
		{name: "invalid entry", version: 1, entries: []ShopEntry{{ShopEntryID: 1}}},
		{name: "duplicate entry", version: 1, entries: []ShopEntry{
			{ShopEntryID: 1, ItemID: 1, UnitPrice: 1, PriceVersion: 1},
			{ShopEntryID: 1, ItemID: 2, UnitPrice: 1, PriceVersion: 1},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewConfigSnapshot(test.version, test.entries); err == nil {
				t.Fatal("NewConfigSnapshot succeeded for invalid input")
			}
		})
	}
}

func TestConfigSnapshotRejectsInvalidAndDuplicateCropSeeds(t *testing.T) {
	valid := CropConfig{
		SeedItemID: 1, CropID: 2, CropItemID: 3, ConfigVersion: 1,
		MaturityValueScaled9: 100, BaseGrowthRateScaled6: 10, BaseYield: 1,
	}
	if _, err := NewConfigSnapshotWithCrops(1, nil, []CropConfig{{SeedItemID: 1}}); err == nil {
		t.Fatal("invalid crop config was accepted")
	}
	if _, err := NewConfigSnapshotWithCrops(1, nil, []CropConfig{valid, valid}); err == nil {
		t.Fatal("duplicate crop seed was accepted")
	}
}

func TestConfigSnapshotRejectsInvalidAndDuplicateFertilizers(t *testing.T) {
	valid := FertilizerConfig{
		ItemID: 1, ConfigVersion: 1, ModifierScaled6: 500_000, DurationMS: 60_000,
	}
	if _, err := NewConfigSnapshotWithContent(1, nil, nil, []FertilizerConfig{{ItemID: 1}}); err == nil {
		t.Fatal("invalid fertilizer config was accepted")
	}
	if _, err := NewConfigSnapshotWithContent(1, nil, nil, []FertilizerConfig{valid, valid}); err == nil {
		t.Fatal("duplicate fertilizer item was accepted")
	}
}

func TestConfigSnapshotRejectsInvalidAndDuplicateSellRules(t *testing.T) {
	valid := SellRule{
		ShopEntryID: 2, ItemID: 1002, UnitPrice: 5, PriceVersion: 9,
	}
	if _, err := NewConfigSnapshotWithEconomy(
		1, nil, nil, nil, []SellRule{{ShopEntryID: 2, ItemID: 1002}},
	); err == nil {
		t.Fatal("invalid sell rule was accepted")
	}
	if _, err := NewConfigSnapshotWithEconomy(
		1, nil, nil, nil, []SellRule{valid, valid},
	); err == nil {
		t.Fatal("duplicate sell item was accepted")
	}
	if _, err := NewConfigSnapshotWithEconomy(
		1,
		[]ShopEntry{{ShopEntryID: 2, ItemID: 1001, UnitPrice: 2, PriceVersion: 8}},
		nil, nil, []SellRule{valid},
	); err == nil {
		t.Fatal("duplicate buy/sell shop entry ID was accepted")
	}
}

func TestDevelopmentCropCatalogAndShopHaveExactStableOrdering(t *testing.T) {
	config := NewDevelopmentConfigSnapshot()
	catalog := config.ActiveCropCatalog()
	if len(catalog) != 11 {
		t.Fatalf("crop catalog length = %d, want 11", len(catalog))
	}
	wantNames := []string{
		"演示作物", "胡萝卜", "白萝卜", "玉米", "番茄", "土豆",
		"茄子", "草莓", "南瓜", "西瓜", "葡萄",
	}
	wantMaturitySeconds := []uint64{100, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150}
	wantBaseYields := []uint32{3, 3, 3, 3, 4, 4, 5, 4, 5, 5, 6}
	wantSeedPrices := []int64{2, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5}
	for index, crop := range catalog {
		wantCropID := uint32(2001 + index)
		wantSeedItemID := uint32(1001)
		wantCropItemID := uint32(1002)
		wantSeedShopEntryID := uint32(5001)
		wantSeedPriceVersion := uint64(8)
		if index > 0 {
			wantSeedItemID = uint32(1004 + index)
			wantCropItemID = uint32(1014 + index)
			wantSeedShopEntryID = uint32(5004 + index)
			wantSeedPriceVersion = 12
		}
		if crop.GetCropId() != wantCropID ||
			crop.GetName() != wantNames[index] ||
			crop.GetSeedItemId() != wantSeedItemID ||
			crop.GetCropItemId() != wantCropItemID ||
			crop.GetMaturitySeconds() != wantMaturitySeconds[index] ||
			crop.GetBaseYield() != wantBaseYields[index] ||
			crop.GetSeedUnitPrice() != wantSeedPrices[index] ||
			crop.GetSeedPriceVersion() != wantSeedPriceVersion ||
			crop.GetSeedShopEntryId() != wantSeedShopEntryID ||
			crop.GetSellUnitPrice() != developmentCropSellUnitPrice ||
			crop.GetSellPriceVersion() != developmentCropSellPriceVersion {
			t.Fatalf("crop catalog entry %d = %+v", index, crop)
		}
	}

	entries := config.ActiveShopEntries()
	gotEntryIDs := make([]uint32, len(entries))
	for index, entry := range entries {
		gotEntryIDs[index] = entry.GetShopEntryId()
	}
	wantEntryIDs := []uint32{
		5001, 5002, 5003,
		5005, 5006, 5007, 5008, 5009, 5010, 5011, 5012, 5013, 5014,
		5015, 5016, 5017, 5018, 5019, 5020, 5021, 5022, 5023, 5024,
	}
	if !reflect.DeepEqual(gotEntryIDs, wantEntryIDs) {
		t.Fatalf("shop entry IDs = %v, want %v", gotEntryIDs, wantEntryIDs)
	}
}

func TestDevelopmentCropStealValuesAreDerivedFromBaseYield(t *testing.T) {
	config := NewDevelopmentConfigSnapshot()
	for _, view := range config.ActiveCropCatalog() {
		crop, exists := config.CropForSeed(view.GetSeedItemId())
		if !exists {
			t.Fatalf("crop %d missing by seed", view.GetCropId())
		}
		wantProtected := (crop.BaseYield + 1) / 2
		wantMaxStealTimes := crop.BaseYield - wantProtected
		if crop.StealQuantity != 1 ||
			crop.ProtectedOwnerYield != wantProtected ||
			crop.MaxStealTimes != wantMaxStealTimes {
			t.Fatalf("crop %d steal values = (%d,%d,%d), want (1,%d,%d)",
				crop.CropID, crop.StealQuantity, crop.ProtectedOwnerYield,
				crop.MaxStealTimes, wantProtected, wantMaxStealTimes)
		}
	}
}

func TestConfigSnapshotRejectsInvalidChapterGraph(t *testing.T) {
	if _, err := NewConfigSnapshotWithChapters(
		1, nil, nil, nil, nil,
		[]ChapterConfig{{ChapterID: 1, ConfigVersion: 1, NextChapterID: 2}},
	); err == nil {
		t.Fatal("missing next chapter was accepted")
	}
	if _, err := NewConfigSnapshotWithChapters(
		1, nil, nil, nil, nil,
		[]ChapterConfig{{
			ChapterID: 1, ConfigVersion: 1,
			Tasks: []Task{{ID: 1, Target: 1}, {ID: 1, Target: 2}},
		}},
	); err == nil {
		t.Fatal("duplicate chapter task was accepted")
	}
	if _, err := NewConfigSnapshotWithChapters(
		1, nil, nil, nil, nil,
		[]ChapterConfig{{
			ChapterID: 1, ConfigVersion: 1,
			RewardItems: []RewardItem{{ItemID: 1, Quantity: 1}, {ItemID: 1, Quantity: 2}},
		}},
	); err == nil {
		t.Fatal("duplicate chapter reward item was accepted")
	}
}
