// Package store — expiry rules (jony's order, 28 Nov 2026): expiry is
// never required. A purchase line with no from/to saves fine, a product
// with no expiry_date saves fine (create AND edit), and an offer with an
// empty start/end window saves fine. A to-date posted with no from-date
// (JS bypassed / direct POST) also saves without error and stores
// ValidFrom empty — the browser fills from=today, the server does not
// invent one (pinned by TestPurchaseForm_EmptyValidFromStoredEmpty).
package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestExpiryRules_PurchaseEmptyExpirySaves(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد الصلاحية"})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	prod := b1MustProduct(t, s, admin, "سكر صلاحية")

	// Rule 1: no from, no to — saves fine, details empty, no batch row.
	pid, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid, Paid: 0, PaymentMethod: "Visa",
		Lines: []PurchaseLineInput{{ProductID: prod.ID, Qty: 1000, PurchasePrice: 500}},
	})
	if err != nil {
		t.Fatalf("CreatePurchase with no expiry at all: %v", err)
	}
	d, err := s.GetPurchase(ctx, pid)
	if err != nil {
		t.Fatalf("GetPurchase: %v", err)
	}
	if len(d.Lines) != 1 || d.Lines[0].ExpiryDate != "" || d.Lines[0].ValidFrom != "" {
		t.Fatalf("purchase lines = %+v, want one line with empty ExpiryDate and ValidFrom", d.Lines)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM ProductBatches WHERE PurchaseID=?`, pid).Scan(&n); err != nil {
		t.Fatalf("batch count query: %v", err)
	}
	if n != 0 {
		t.Fatalf("ProductBatches rows = %d, want 0 (no batch/expiry/from on the line)", n)
	}

	// Direct POST with to-date only (JS bypassed): saves without error,
	// ValidFrom stays empty — the server does not invent a from-date.
	pid2, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid, Paid: 0, PaymentMethod: "Visa",
		Lines: []PurchaseLineInput{{ProductID: prod.ID, Qty: 1000, PurchasePrice: 500,
			BatchNumber: "E-1", ExpiryDate: "2027-01-15"}},
	})
	if err != nil {
		t.Fatalf("CreatePurchase with expiry but no valid_from: %v", err)
	}
	d2, err := s.GetPurchase(ctx, pid2)
	if err != nil {
		t.Fatalf("GetPurchase: %v", err)
	}
	if len(d2.Lines) != 1 || d2.Lines[0].ExpiryDate != "2027-01-15" || d2.Lines[0].ValidFrom != "" {
		t.Fatalf("purchase lines = %+v, want ExpiryDate 2027-01-15 with empty ValidFrom", d2.Lines)
	}
	var batchVF sql.NullString
	if err := s.DB.QueryRow(`SELECT ValidFrom FROM ProductBatches WHERE PurchaseID=?`, pid2).Scan(&batchVF); err != nil {
		t.Fatalf("batch ValidFrom query: %v", err)
	}
	if batchVF.String != "" {
		t.Fatalf("ProductBatches.ValidFrom = %q, want empty", batchVF.String)
	}
}

func TestExpiryRules_ProductEmptyExpirySavesCreateAndEdit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)

	// Rule 1: create with no expiry_date saves fine.
	in := b1ProductInput("منتج بلا صلاحية")
	in.ExpiryDate = ""
	p, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("CreateProduct with empty expiry: %v", err)
	}
	got, err := s.GetProduct(ctx, admin, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if got.ExpiryDate != "" {
		t.Fatalf("stored ExpiryDate = %q, want empty", got.ExpiryDate)
	}

	// Edit: setting an expiry stores it; clearing it back to empty
	// stores empty again (no forced date, no validation error).
	in.ExpiryDate = "2027-05-01"
	if _, err := s.UpdateProduct(ctx, admin, p.ID, in); err != nil {
		t.Fatalf("UpdateProduct setting expiry: %v", err)
	}
	got, _ = s.GetProduct(ctx, admin, p.ID)
	if got.ExpiryDate != "2027-05-01" {
		t.Fatalf("stored ExpiryDate = %q, want 2027-05-01", got.ExpiryDate)
	}
	in.ExpiryDate = ""
	if _, err := s.UpdateProduct(ctx, admin, p.ID, in); err != nil {
		t.Fatalf("UpdateProduct clearing expiry: %v", err)
	}
	got, _ = s.GetProduct(ctx, admin, p.ID)
	if got.ExpiryDate != "" {
		t.Fatalf("stored ExpiryDate after clear = %q, want empty", got.ExpiryDate)
	}
}

func TestExpiryRules_OfferEmptyWindowSaves(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p := b1MustProduct(t, s, admin, "سكر عرض")

	// Rule 1: an offer with no start/end window saves fine.
	o, err := s.CreateOffer(ctx, admin, OfferInput{Name: "عرض بلا مدة", Type: OfferProductPct,
		Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	if err != nil {
		t.Fatalf("CreateOffer with empty window: %v", err)
	}
	var startAt, endAt sql.NullString
	if err := s.DB.QueryRow(`SELECT StartAt, EndAt FROM Offers WHERE OfferID=?`, o.ID).Scan(&startAt, &endAt); err != nil {
		t.Fatalf("offer window query: %v", err)
	}
	if startAt.String != "" || endAt.String != "" {
		t.Fatalf("stored window = %q..%q, want empty..empty", startAt.String, endAt.String)
	}

	// End-only (direct POST, JS bypassed) also saves without error and
	// keeps StartAt empty.
	o2, err := s.CreateOffer(ctx, admin, OfferInput{Name: "عرض نهاية فقط", Type: OfferProductPct,
		Percent: 10, EndAt: "2027-01-15", IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	if err != nil {
		t.Fatalf("CreateOffer with end only: %v", err)
	}
	if err := s.DB.QueryRow(`SELECT StartAt, EndAt FROM Offers WHERE OfferID=?`, o2.ID).Scan(&startAt, &endAt); err != nil {
		t.Fatalf("offer window query: %v", err)
	}
	// EndAt is stored normalized to end-of-day (23:59:59).
	if startAt.String != "" || !strings.HasPrefix(endAt.String, "2027-01-15") {
		t.Fatalf("stored window = %q..%q, want empty..2027-01-15 23:59:59", startAt.String, endAt.String)
	}
}

// TestExpiryRules_PurchaseEmptyExpiryLeavesProductExpiryAlone pins that a
// no-expiry purchase line does not touch the product's own ExpiryDate
// (the batch branch that overwrites Products.ExpiryDate only runs when
// a batch number, expiry or from-date is present).
func TestExpiryRules_PurchaseEmptyExpiryLeavesProductExpiryAlone(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد صلاحية 2"})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	in := b1ProductInput("منتج صلاحية محفوظة")
	in.ExpiryDate = "2027-06-01"
	p, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	if _, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid, Paid: 0, PaymentMethod: "Visa",
		Lines: []PurchaseLineInput{{ProductID: p.ID, Qty: 1000, PurchasePrice: 500}},
	}); err != nil {
		t.Fatalf("CreatePurchase with no expiry: %v", err)
	}
	got, err := s.GetProduct(ctx, admin, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if got.ExpiryDate != "2027-06-01" {
		t.Fatalf("product ExpiryDate after no-expiry purchase = %q, want 2027-06-01", got.ExpiryDate)
	}
}

// Fix-round-2 F1 (server F1 = integration M2): a partial purchase line
// must not wipe the product fields it does not supply. Only the fields
// present on the line overwrite Products; the ProductBatches row still
// records the line's own (possibly empty) values.
func TestExpiryRules_PurchasePartialLinePreservesProductFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد حفظ جزئي"})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	newProduct := func(name string) *Product {
		t.Helper()
		in := b1ProductInput(name)
		in.ExpiryDate = "2027-06-01"
		in.BatchNumber = "B-OLD"
		p, err := s.CreateProduct(ctx, admin, in)
		if err != nil {
			t.Fatalf("create product %s: %v", name, err)
		}
		return p
	}
	buy := func(p *Product, ln PurchaseLineInput) int64 {
		t.Helper()
		ln.ProductID = p.ID
		ln.Qty = 1000
		ln.PurchasePrice = 500
		pid, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
			VendorID: vid, Paid: 0, PaymentMethod: "Visa",
			Lines: []PurchaseLineInput{ln},
		})
		if err != nil {
			t.Fatalf("CreatePurchase %+v: %v", ln, err)
		}
		return pid
	}
	checkProduct := func(p *Product, wantExp, wantBatch string) {
		t.Helper()
		got, err := s.GetProduct(ctx, admin, p.ID)
		if err != nil {
			t.Fatalf("GetProduct: %v", err)
		}
		if got.ExpiryDate != wantExp || got.BatchNumber != wantBatch {
			t.Fatalf("product after purchase = expiry %q batch %q, want expiry %q batch %q",
				got.ExpiryDate, got.BatchNumber, wantExp, wantBatch)
		}
	}
	checkBatchRow := func(pid int64, wantBatch, wantExp, wantVF string) {
		t.Helper()
		var b, e, vf sql.NullString
		if err := s.DB.QueryRow(`SELECT BatchNumber, ExpiryDate, ValidFrom FROM ProductBatches WHERE PurchaseID=?`, pid).Scan(&b, &e, &vf); err != nil {
			t.Fatalf("batch row query: %v", err)
		}
		if b.String != wantBatch || e.String != wantExp || vf.String != wantVF {
			t.Fatalf("ProductBatches row = batch %q expiry %q from %q, want batch %q expiry %q from %q",
				b.String, e.String, vf.String, wantBatch, wantExp, wantVF)
		}
	}

	// From-only line: product expiry AND batch both preserved.
	p := newProduct("منتج من فقط")
	pid := buy(p, PurchaseLineInput{ValidFrom: "2026-11-01"})
	checkProduct(p, "2027-06-01", "B-OLD")
	checkBatchRow(pid, "", "", "2026-11-01")

	// Batch-only line: batch updated, product expiry preserved.
	p = newProduct("منتج تشغيلة فقط")
	pid = buy(p, PurchaseLineInput{BatchNumber: "B-NEW"})
	checkProduct(p, "2027-06-01", "B-NEW")
	checkBatchRow(pid, "B-NEW", "", "")

	// Expiry-only line: expiry updated, batch preserved.
	p = newProduct("منتج صلاحية فقط")
	pid = buy(p, PurchaseLineInput{ExpiryDate: "2028-01-01"})
	checkProduct(p, "2028-01-01", "B-OLD")
	checkBatchRow(pid, "", "2028-01-01", "")

	// All three supplied: all three updated.
	p = newProduct("منتج الكل")
	pid = buy(p, PurchaseLineInput{BatchNumber: "B-ALL", ExpiryDate: "2028-02-02", ValidFrom: "2027-01-01"})
	checkProduct(p, "2028-02-02", "B-ALL")
	checkBatchRow(pid, "B-ALL", "2028-02-02", "2027-01-01")
}

// Fix-round-2 F2 (server F2 = integration L1): CreatePurchase rejects
// malformed ExpiryDate/ValidFrom as real YYYY-MM-DD calendar dates
// (same rule as products). Empty stays accepted; valid dates save.
func TestExpiryRules_PurchaseMalformedExpiryDatesRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد تواريخ"})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	prod := b1MustProduct(t, s, admin, "سكر تواريخ")
	buy := func(ln PurchaseLineInput) error {
		ln.ProductID = prod.ID
		ln.Qty = 1000
		ln.PurchasePrice = 500
		_, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
			VendorID: vid, Paid: 0, PaymentMethod: "Visa",
			Lines: []PurchaseLineInput{ln},
		})
		return err
	}
	for _, tc := range []struct {
		name string
		ln   PurchaseLineInput
		want string
	}{
		{"malformed expiry", PurchaseLineInput{ExpiryDate: "15/01/2026"}, "تاريخ الصلاحية"},
		{"malformed validFrom", PurchaseLineInput{ValidFrom: "01-11-2026"}, "تاريخ بداية الصلاحية"},
		{"impossible expiry day", PurchaseLineInput{ExpiryDate: "2026-02-30"}, "تاريخ الصلاحية"},
		{"impossible validFrom day", PurchaseLineInput{ValidFrom: "2026-02-30"}, "تاريخ بداية الصلاحية"},
	} {
		err := buy(tc.ln)
		if err == nil {
			t.Fatalf("%s: CreatePurchase succeeded, want rejection", tc.name)
		}
		if !strings.Contains(err.Error(), "سطر 1") || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error = %q, want it to name line 1 and %q", tc.name, err.Error(), tc.want)
		}
	}

	// Empty dates still accepted, and valid dates still save.
	pid, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid, Paid: 0, PaymentMethod: "Visa",
		Lines: []PurchaseLineInput{{ProductID: prod.ID, Qty: 1000, PurchasePrice: 500,
			ExpiryDate: "2027-06-01", ValidFrom: "2026-11-01"}},
	})
	if err != nil {
		t.Fatalf("CreatePurchase with valid dates: %v", err)
	}
	d, err := s.GetPurchase(ctx, pid)
	if err != nil {
		t.Fatalf("GetPurchase: %v", err)
	}
	if len(d.Lines) != 1 || d.Lines[0].ExpiryDate != "2027-06-01" || d.Lines[0].ValidFrom != "2026-11-01" {
		t.Fatalf("purchase lines = %+v, want ExpiryDate 2027-06-01 ValidFrom 2026-11-01", d.Lines)
	}
}

// Fix-round-2 M1 (integration M1): setting an expiry date implies
// tracking — server-side, in create AND update. Clearing the expiry
// leaves the tracking flag exactly as the caller set it.
func TestExpiryRules_ProductExpiryImpliesTracking(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	nearExpiry := time.Now().AddDate(0, 0, 10).Format("2006-01-02")
	inExpiring := func(list []*Product, id int64) bool {
		for _, p := range list {
			if p.ID == id {
				return true
			}
		}
		return false
	}

	// Create with expiry + checkbox unchecked -> stored tracked=1 and
	// appears in ExpiringProducts (near expiry, inside the 30-day alert).
	in := b1ProductInput("منتج صلاحية وتتبع ضمني")
	in.ExpiryDate = nearExpiry
	in.IsExpiryTracked = false
	p, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("CreateProduct with expiry: %v", err)
	}
	got, err := s.GetProduct(ctx, admin, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if !got.IsExpiryTracked {
		t.Fatalf("product with expiry stored IsExpiryTracked=false, want true")
	}
	list, err := s.ExpiringProducts(ctx, admin)
	if err != nil {
		t.Fatalf("ExpiringProducts: %v", err)
	}
	if !inExpiring(list, p.ID) {
		t.Fatalf("product with expiry not in ExpiringProducts (want it listed)")
	}

	// Create with no expiry + unchecked stays untracked and unlisted.
	in2 := b1ProductInput("منتج بلا صلاحية وبلا تتبع")
	in2.ExpiryDate = ""
	in2.IsExpiryTracked = false
	p2, err := s.CreateProduct(ctx, admin, in2)
	if err != nil {
		t.Fatalf("CreateProduct without expiry: %v", err)
	}
	got2, _ := s.GetProduct(ctx, admin, p2.ID)
	if got2.IsExpiryTracked {
		t.Fatalf("product without expiry stored IsExpiryTracked=true, want false")
	}
	list, _ = s.ExpiringProducts(ctx, admin)
	if inExpiring(list, p2.ID) {
		t.Fatalf("product without expiry is in ExpiringProducts, want it absent")
	}

	// Update: setting an expiry on an untracked product implies tracking.
	in2.ExpiryDate = nearExpiry
	in2.IsExpiryTracked = false
	if _, err := s.UpdateProduct(ctx, admin, p2.ID, in2); err != nil {
		t.Fatalf("UpdateProduct setting expiry: %v", err)
	}
	got2, _ = s.GetProduct(ctx, admin, p2.ID)
	if !got2.IsExpiryTracked {
		t.Fatalf("product gained expiry via update but stored IsExpiryTracked=false, want true")
	}

	// Clearing the expiry keeps the tracking flag as the caller set it:
	// true stays true, and an explicit false is honoured (not forced).
	in2.ExpiryDate = ""
	in2.IsExpiryTracked = true
	if _, err := s.UpdateProduct(ctx, admin, p2.ID, in2); err != nil {
		t.Fatalf("UpdateProduct clearing expiry (tracked): %v", err)
	}
	got2, _ = s.GetProduct(ctx, admin, p2.ID)
	if got2.ExpiryDate != "" || !got2.IsExpiryTracked {
		t.Fatalf("after clearing expiry with tracked=true: expiry=%q tracked=%v, want expiry empty tracked true",
			got2.ExpiryDate, got2.IsExpiryTracked)
	}
	in2.IsExpiryTracked = false
	if _, err := s.UpdateProduct(ctx, admin, p2.ID, in2); err != nil {
		t.Fatalf("UpdateProduct clearing expiry (untracked): %v", err)
	}
	got2, _ = s.GetProduct(ctx, admin, p2.ID)
	if got2.IsExpiryTracked {
		t.Fatalf("after clearing expiry with tracked=false: tracked=true, want the caller's false honoured")
	}
}
