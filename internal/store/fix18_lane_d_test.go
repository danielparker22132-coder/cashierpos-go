package store

import (
	"context"
	"strings"
	"testing"
)

// FIX-18 lane D — R18-D1 (poisoned stored zones).
//
// A stored delivery_zones value holding two names that fold equal
// (e.g. ["TestZone","testzone"]) can only exist via direct DB edit or a
// pre-fix-17 backup — the app itself blocks creating such a pair.
// Pre-fix, SaveDeliveryZones built stored as map[folded]raw
// (last-writer-wins), so re-saving either colliding name exactly was
// refused as a duplicate and the settings save was wedged. The fix
// keeps stored as map[folded][]raw and the exact-match exemption
// applies when the incoming name equals ANY raw stored name with that
// fold. A save of exact stored names therefore succeeds and heals the
// stored list (whole-setting replace drops the colliding twin); a
// genuinely new case variant is still refused.

func fix18PoisonZones(t *testing.T, s *Store, raw string) {
	t.Helper()
	if err := s.SetSetting("delivery_zones", raw); err != nil {
		t.Fatalf("poison delivery_zones: %v", err)
	}
}

func fix18ZoneNames(s *Store) []string {
	var names []string
	for _, z := range s.GetDeliveryZones() {
		names = append(names, z.Name)
	}
	return names
}

func fix18HasFoldDuplicate(names []string) bool {
	seen := map[string]bool{}
	for _, n := range names {
		f := strings.ToLower(n)
		if seen[f] {
			return true
		}
		seen[f] = true
	}
	return false
}

func TestFix18PoisonedZonesExactMatchSucceedsAndHeals(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()

	// (a) Poisoned DB + save [TestZone, OtherZone] succeeds …
	fix18PoisonZones(t, s, `[{"name":"TestZone","fee":950,"active":true},{"name":"testzone","fee":500,"active":true},{"name":"ناجي حمادي","fee":0,"active":true}]`)
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("poisoned save [TestZone, OtherZone]: %v, want success", err)
	}
	// (b) … and the stored zones are sane: no fold-duplicate remains,
	// the colliding twin is gone, Nag Hammadi is kept fee 0.
	names := fix18ZoneNames(s)
	if fix18HasFoldDuplicate(names) {
		t.Fatalf("stored zones after heal still fold-duplicate: %v", names)
	}
	want := map[string]bool{"TestZone": true, "OtherZone": true, "ناجي حمادي": true}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("stored zones after heal = %v, want TestZone/OtherZone/ناجي حمادي", names)
		}
	}
	if len(names) != 3 {
		t.Fatalf("stored zones after heal = %v, want exactly 3", names)
	}
	// (c) Exact-match re-save of the healed list still works.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("exact-match re-save: %v, want success", err)
	}
	// (d) A genuinely new case variant of a stored zone is still refused.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TESTZONE", Fee: 950, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("TESTZONE vs stored TestZone err = %v, want a duplicate refusal", err)
	}
	// Stored zones unchanged by the refused save.
	if got := fix18ZoneNames(s); len(got) != 3 || fix18HasFoldDuplicate(got) {
		t.Fatalf("stored zones after refused save = %v", got)
	}
}

func TestFix18PoisonedZonesOtherRawMatchAndBatchStillGuarded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()

	// The exemption applies to ANY raw stored name, not just the last
	// one written: saving the other twin exactly succeeds too.
	fix18PoisonZones(t, s, `[{"name":"TestZone","fee":950,"active":true},{"name":"testzone","fee":500,"active":true},{"name":"ناجي حمادي","fee":0,"active":true}]`)
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "testzone", Fee: 500, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("poisoned save [testzone, OtherZone]: %v, want success", err)
	}
	if got := fix18ZoneNames(s); fix18HasFoldDuplicate(got) {
		t.Fatalf("stored zones after heal = %v, want no fold-duplicate", got)
	}

	// Reversed injection: stored [testzone, TestZone] — pre-fix,
	// re-saving "testzone" was refused (last-writer-wins kept "TestZone").
	fix18PoisonZones(t, s, `[{"name":"testzone","fee":500,"active":true},{"name":"TestZone","fee":950,"active":true},{"name":"ناجي حمادي","fee":0,"active":true}]`)
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "testzone", Fee: 500, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("reversed poison save [testzone, OtherZone]: %v, want success", err)
	}

	// A genuinely new case variant against a POISONED base is still
	// refused: TESTZONE equals neither raw stored name.
	fix18PoisonZones(t, s, `[{"name":"TestZone","fee":950,"active":true},{"name":"testzone","fee":500,"active":true},{"name":"ناجي حمادي","fee":0,"active":true}]`)
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TESTZONE", Fee: 950, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("TESTZONE vs poisoned base err = %v, want a duplicate refusal", err)
	}

	// The within-batch check is untouched: [TestZone, testzone] in one
	// request is still refused even on a poisoned base.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "testzone", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("batch [TestZone, testzone] err = %v, want a duplicate refusal", err)
	}
}

func TestFix18PoisonedZonesRecoveryPathStillWorks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()

	// Review-18 recovery: saving a list omitting BOTH colliding names
	// succeeds (whole-setting replace) and leaves a clean base.
	fix18PoisonZones(t, s, `[{"name":"TestZone","fee":950,"active":true},{"name":"testzone","fee":500,"active":true},{"name":"ناجي حمادي","fee":0,"active":true}]`)
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("recovery save omitting both colliding names: %v, want success", err)
	}
	names := fix18ZoneNames(s)
	if fix18HasFoldDuplicate(names) {
		t.Fatalf("stored zones after recovery = %v, want no fold-duplicate", names)
	}
	// The clean base restores: [TestZone, OtherZone] saves fine after it.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("clean-base restore after recovery: %v, want success", err)
	}
}
