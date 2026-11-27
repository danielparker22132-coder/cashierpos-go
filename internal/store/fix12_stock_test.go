package store

import (
	"context"
	"testing"
)

// FIX-12 lane G (stock) regression tests.
//
// SW-L04: ExpiringProducts must agree with the hub counts — only
// expiry-tracked products raise an expiry alert.
// SW-L05: a zero-difference AdjustStock must write no movement row and
// no audit row, mirroring ApproveStocktake's delta-0 skip.

func TestFix12_ExpiringProductsExcludesUntracked(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)

	yesterday := addDays(Today(), -1)
	soon := addDays(Today(), 3)

	// Tracked expired — must appear in both lists.
	in := b1ProductInput("منتهي متتبع")
	in.ExpiryDate = yesterday
	in.ExpiryAlertDays = 7
	in.IsExpiryTracked = true
	trackedExpired, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create tracked expired: %v", err)
	}
	// Untracked expired — must appear in neither list.
	in = b1ProductInput("منتهي غير متتبع")
	in.ExpiryDate = yesterday
	in.ExpiryAlertDays = 7
	in.IsExpiryTracked = false
	untrackedExpired, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create untracked expired: %v", err)
	}
	// Tracked expiring soon — must appear in both lists.
	in = b1ProductInput("قريب متتبع")
	in.ExpiryDate = soon
	in.ExpiryAlertDays = 7
	in.IsExpiryTracked = true
	trackedSoon, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create tracked soon: %v", err)
	}
	// Untracked expiring soon — must appear in neither list.
	in = b1ProductInput("قريب غير متتبع")
	in.ExpiryDate = soon
	in.ExpiryAlertDays = 7
	in.IsExpiryTracked = false
	untrackedSoon, err := s.CreateProduct(ctx, admin, in)
	if err != nil {
		t.Fatalf("create untracked soon: %v", err)
	}

	// /products surface: ExpiringProducts.
	exp, err := s.ExpiringProducts(ctx, admin)
	if err != nil {
		t.Fatalf("ExpiringProducts: %v", err)
	}
	seen := map[int64]bool{}
	for _, p := range exp {
		seen[p.ID] = true
	}
	if !seen[trackedExpired.ID] {
		t.Fatalf("tracked expired product %d missing from ExpiringProducts: %+v", trackedExpired.ID, exp)
	}
	if !seen[trackedSoon.ID] {
		t.Fatalf("tracked near-expiry product %d missing from ExpiringProducts: %+v", trackedSoon.ID, exp)
	}
	if seen[untrackedExpired.ID] {
		t.Fatalf("untracked expired product %d must NOT appear in ExpiringProducts", untrackedExpired.ID)
	}
	if seen[untrackedSoon.ID] {
		t.Fatalf("untracked near-expiry product %d must NOT appear in ExpiringProducts", untrackedSoon.ID)
	}

	// Hub surface: the exact H-6 counts from internal/web/routes.go.
	today := Today()
	var hubExpired, hubNear int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Products
		WHERE IsActive=1 AND IsExpiryTracked=1 AND ExpiryDate IS NOT NULL AND ExpiryDate<>'' AND ExpiryDate<?`,
		today).Scan(&hubExpired); err != nil {
		t.Fatalf("hub expired count: %v", err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Products
		WHERE IsActive=1 AND IsExpiryTracked=1 AND ExpiryDate IS NOT NULL AND ExpiryDate<>'' AND ExpiryDate>=? AND ExpiryDate<=date(?,'+7 days')`,
		today, today).Scan(&hubNear); err != nil {
		t.Fatalf("hub near-expiry count: %v", err)
	}
	// Only the tracked expired product is expired; only the tracked
	// soon product is in the 7-day window.
	if hubExpired != 1 {
		t.Fatalf("hub expired count = %d, want 1 (tracked expired only)", hubExpired)
	}
	if hubNear != 1 {
		t.Fatalf("hub near-expiry count = %d, want 1 (tracked soon only)", hubNear)
	}
	// The two surfaces agree: ExpiringProducts lists exactly the
	// tracked expired + tracked soon products.
	if len(exp) != hubExpired+hubNear {
		t.Fatalf("ExpiringProducts len = %d, hub expired+near = %d — surfaces disagree",
			len(exp), hubExpired+hubNear)
	}
}

func TestFix12_ZeroDiffAdjustWritesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)

	p := b1MustProduct(t, s, admin, "أرز")
	if _, err := s.AdjustStock(ctx, admin, p.ID, 90000, "ضبط أولي"); err != nil {
		t.Fatalf("baseline adjust: %v", err)
	}
	movements := func() int {
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE ProductID=?`, p.ID).Scan(&n); err != nil {
			t.Fatalf("count movements: %v", err)
		}
		return n
	}
	audits := func() int {
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='تعديل مخزون'`).Scan(&n); err != nil {
			t.Fatalf("count audits: %v", err)
		}
		return n
	}
	movBefore, audBefore := movements(), audits()

	// Zero-difference adjust: same quantity as current (90000).
	got, err := s.AdjustStock(ctx, admin, p.ID, 90000, "تعديل بلا فرق")
	if err != nil {
		t.Fatalf("zero-diff adjust should succeed: %v", err)
	}
	if got.Quantity != 90000 {
		t.Fatalf("qty after zero-diff adjust = %d, want 90000", got.Quantity)
	}
	if movements() != movBefore {
		t.Fatalf("zero-diff adjust wrote a movement row (before %d, after %d)", movBefore, movements())
	}
	if audits() != audBefore {
		t.Fatalf("zero-diff adjust wrote an audit row (before %d, after %d)", audBefore, audits())
	}

	// Non-zero adjust still writes both a movement and an audit row.
	got, err = s.AdjustStock(ctx, admin, p.ID, 80000, "تصحيح")
	if err != nil {
		t.Fatalf("non-zero adjust: %v", err)
	}
	if got.Quantity != 80000 {
		t.Fatalf("qty after non-zero adjust = %d, want 80000", got.Quantity)
	}
	if movements() != movBefore+1 {
		t.Fatalf("non-zero adjust movements = %d, want %d", movements(), movBefore+1)
	}
	if audits() != audBefore+1 {
		t.Fatalf("non-zero adjust audits = %d, want %d", audits(), audBefore+1)
	}
	// The new movement is an adjust of |diff| = 10000.
	var movType string
	var movQty int64
	if err := s.DB.QueryRow(`SELECT MovementType, Quantity FROM StockMovements
		WHERE ProductID=? ORDER BY MovementID DESC LIMIT 1`, p.ID).Scan(&movType, &movQty); err != nil {
		t.Fatalf("latest movement: %v", err)
	}
	if movType != "adjust" || movQty != 10000 {
		t.Fatalf("latest movement = %s/%d, want adjust/10000", movType, movQty)
	}
}
