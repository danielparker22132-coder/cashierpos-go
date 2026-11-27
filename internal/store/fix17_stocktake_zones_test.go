package store

import (
	"context"
	"strings"
	"testing"
)

// FIX-17 lane D — SW2-L03 (stocktake set/approve ignore IsActive) and
// SW2-L05 (zone duplicate check is case-sensitive).

func TestFix17SetStocktakeCountedRefusesInactive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "f17stcash", "cashier")

	in := b1ProductInput("مكرونة")
	in.Barcode = "1005"
	p, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	st, err := s.CreateStocktake(ctx, admin, "جرد fix17")
	if err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	// Control: set while active works, and scan while active works.
	if _, err := s.SetStocktakeCounted(ctx, cashier, st.ID, p.ID, 7000); err != nil {
		t.Fatalf("set while active: %v", err)
	}
	if _, err := s.ScanStocktakeLine(ctx, cashier, st.ID, "1005", nil); err != nil {
		t.Fatalf("scan while active: %v", err)
	}
	// Deactivate the product (DeleteProduct = IsActive=0).
	if err := s.DeleteProduct(ctx, admin, p.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	// Scan refuses the inactive product (pre-existing behaviour).
	if _, err := s.ScanStocktakeLine(ctx, cashier, st.ID, "1005", nil); err == nil {
		t.Fatal("scan of inactive product should be refused")
	}
	// Set must refuse it too, like scan does.
	if _, err := s.SetStocktakeCounted(ctx, cashier, st.ID, p.ID, 1000); err == nil {
		t.Fatal("set on inactive product should be refused")
	} else if !strings.Contains(err.Error(), "غير مفعّل") {
		t.Fatalf("set inactive err = %v, want a clear inactive-product message", err)
	}
	// No new line was created by the refused set.
	got, err := s.GetStocktake(ctx, admin, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 {
		t.Fatalf("lines after refused set = %d, want 1", len(got.Lines))
	}
}

func TestFix17ApproveStocktakeRefusesInactive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "f17apcash", "cashier")

	p := b1MustProduct(t, s, admin, "مكرونة")
	// b1MustProduct stock is 10000 (10 units); scale to the repro shape:
	// live 80 units, counted 70.
	if _, err := s.AdjustStock(ctx, admin, p.ID, 80000, "تهيئة جرد fix17"); err != nil {
		t.Fatalf("adjust to 80: %v", err)
	}
	st, err := s.CreateStocktake(ctx, admin, "جرد fix17")
	if err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	if _, err := s.SetStocktakeCounted(ctx, cashier, st.ID, p.ID, 70000); err != nil {
		t.Fatalf("set counted 70: %v", err)
	}
	if _, err := s.CloseStocktake(ctx, admin, st.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Deactivate after close, then approve must refuse the whole approval.
	if err := s.DeleteProduct(ctx, admin, p.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	// The setup AdjustStock above already wrote one adjust movement;
	// the refused approval must write none more.
	var n0 int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE MovementType='adjust'`).Scan(&n0)
	if _, err := s.ApproveStocktake(ctx, admin, st.ID, "اعتماد"); err == nil {
		t.Fatal("approve with inactive counted product should be refused")
	} else if !strings.Contains(err.Error(), p.Name) || !strings.Contains(err.Error(), "غير مفعّل") {
		t.Fatalf("approve inactive err = %v, want the product named as inactive", err)
	}
	// Nothing applied: qty unchanged at 80, session stays closed/unapproved,
	// no movement posted.
	got, _ := s.GetProduct(ctx, admin, p.ID)
	if got.Quantity != 80000 {
		t.Fatalf("qty after refused approve = %d, want 80000", got.Quantity)
	}
	gst, err := s.GetStocktake(ctx, admin, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gst.Status != "closed" || gst.ApprovedAt != "" {
		t.Fatalf("session after refused approve: status=%s approvedAt=%q, want closed/unapproved",
			gst.Status, gst.ApprovedAt)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE MovementType='adjust'`).Scan(&n)
	if n != n0 {
		t.Fatalf("adjust movements after refused approve = %d, want %d (none added)", n, n0)
	}
}

func TestFix17ZoneDuplicateCaseInsensitive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()

	// Within one save: TestZone + testzone is a duplicate.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "testzone", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("case-variant batch err = %v, want a duplicate refusal", err)
	}
	// Whitespace-trimmed case variant is a duplicate too.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "  testzone  ", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("trimmed case-variant batch err = %v, want a duplicate refusal", err)
	}
	// Exact-match duplicate still refused.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "TestZone", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("exact duplicate err = %v, want a duplicate refusal", err)
	}
	// Distinct names still save.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("distinct names save: %v", err)
	}
	// Case variant of an ALREADY-STORED zone is refused too.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "testzone", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("case-variant of stored zone err = %v, want a duplicate refusal", err)
	}
	// Stored zones are unchanged by the refused saves.
	zones := s.GetDeliveryZones()
	names := map[string]bool{}
	for _, z := range zones {
		names[z.Name] = true
	}
	if !names["TestZone"] || !names["OtherZone"] || names["testzone"] {
		t.Fatalf("stored zones after refusals = %+v", zones)
	}
	// Re-saving the stored list unchanged (exact names) still works —
	// an exact match is the same zone, not a duplicate.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "OtherZone", Fee: 500, Active: true},
	}); err != nil {
		t.Fatalf("re-save of stored zones: %v", err)
	}
	// A case variant of a stored name is refused even when every other
	// name in the batch matches its stored zone exactly.
	if err := s.SaveDeliveryZones(ctx, admin, []DeliveryZone{
		{Name: "TestZone", Fee: 950, Active: true},
		{Name: "otherzone", Fee: 500, Active: true},
	}); err == nil || !strings.Contains(err.Error(), "مكررة") {
		t.Fatalf("otherzone vs stored OtherZone err = %v, want a duplicate refusal", err)
	}
}
