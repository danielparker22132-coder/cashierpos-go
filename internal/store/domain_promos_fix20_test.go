package store

import (
	"context"
	"math"
	"strings"
	"testing"
)

// fix-20 lane B (R20-B-F2b): buy_x_get_y promo math — unchecked
// multiplications wrapped and panicked.
//
// This file uses ONLY symbols that exist in the pre-fix code, so the
// same file can be run against the pre-fix tree for mutation proof:
// every test here must FAIL on pre-fix code and PASS post-fix.

func fix20BxgyInput(pID int64, x, y int64) OfferInput {
	return OfferInput{Name: "عرض اختبار", Type: OfferBuyXGetY, BuyX: x, FreeY: y,
		IsActive: true, Targets: []OfferTarget{{TargetType: "product", TargetID: pID}}}
}

// TestFix20_BxgyCapValidation: BuyX/FreeY are capped at creation
// (MaxBxgyUnits = 100000 units = money.MaxQty per line).
func TestFix20_BxgyCapValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p := b1MustProduct(t, s, admin, "منتج حد")

	// Pure validation cases (no DB write).
	cases := []struct {
		x, y    int64
		wantErr bool
		want    string
	}{
		{100001, 1, true, "100000"},
		{1, 100001, true, "100000"},
		{1 << 61, 1, true, "100000"},
		{1, (1 << 61) + 500000, true, "100000"},
		{100000, 100000, false, ""}, // boundary: allowed, never refuses a grantable offer
		{2, 1, false, ""},           // normal buy 2 get 1
		{1, 1, false, ""},           // normal buy 1 get 1
		{0, 1, true, "أكبر من صفر"},
		{1, 0, true, "أكبر من صفر"},
	}
	for i, c := range cases {
		err := validateOfferInput(fix20BxgyInput(p.ID, c.x, c.y))
		if c.wantErr {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("case %d (x=%d y=%d): err = %v, want containing %q", i, c.x, c.y, err, c.want)
			}
		} else if err != nil {
			t.Fatalf("case %d (x=%d y=%d): unexpected err %v", i, c.x, c.y, err)
		}
	}

	// CreateOffer end to end: an absurd X is refused and stores nothing.
	if _, err := s.CreateOffer(ctx, admin, fix20BxgyInput(p.ID, 1<<61, 1)); err == nil {
		t.Fatal("CreateOffer with BuyX=2^61 should be refused")
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM Offers WHERE Name='عرض اختبار'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("absurd offer stored %d rows, want 0", n)
	}

	// UpdateOffer path: a sane offer cannot be edited into an absurd one.
	sane := b1CreateOffer(t, s, OfferInput{Name: "عرض سليم", Type: OfferBuyXGetY, BuyX: 2, FreeY: 1,
		IsActive: true, Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	absurd := fix20BxgyInput(p.ID, 1, (1<<61)+500000)
	absurd.Name = "عرض سليم"
	if _, err := s.UpdateOffer(ctx, admin, sane.ID, absurd); err == nil {
		t.Fatal("UpdateOffer to FreeY=2^61+500000 should be refused")
	}
	after, err := s.GetOffer(ctx, admin, sane.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.FreeQty != "1" {
		t.Fatalf("absurd update changed stored FreeQty to %q, want \"1\"", after.FreeQty)
	}
}

// fix20InsertAbsurdOffer inserts a buy_x_get_y offer straight into the
// DB, bypassing the creation cap — simulating an offer stored before
// the cap existed. Value = X, FreeQty = Y.
func fix20InsertAbsurdOffer(t *testing.T, s *Store, name string, pid, x, y int64) {
	t.Helper()
	res, err := s.DB.Exec(`INSERT INTO Offers(Name,Type,Value,Threshold,FreeQty,StartAt,EndAt,IsActive,CreatedBy,CreatedAt)
		VALUES(?, 'buy_x_get_y', ?, '', ?, '', '', 1, 'admin', date('now'))`, name, x, y)
	if err != nil {
		t.Fatalf("insert absurd offer: %v", err)
	}
	oid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO OfferTargets(OfferID,TargetType,TargetID,TargetName,Qty)
		VALUES(?,?,?,'', '1000')`, oid, "product", pid); err != nil {
		t.Fatalf("insert absurd offer target: %v", err)
	}
}

// TestFix20_BxgyAbsurdXUntargeted: X = 2^61 used to make x*1000 wrap
// to 0 and panic (integer divide by zero) on EVERY sale — even of a
// product the offer does not target. An untargeted sale must evaluate
// cleanly with no free grant.
func TestFix20_BxgyAbsurdXUntargeted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	target := b1MustProduct(t, s, admin, "منتج مستهدف")
	other := b1MustProduct(t, s, admin, "منتج آخر")
	fix20InsertAbsurdOffer(t, s, "عرض ذعر", target.ID, 1<<61, 1)

	res, err := s.EvaluateOffers(ctx, b1PromoLines(other.ID, 1000, 2000, 1000, ""))
	if err != nil {
		t.Fatalf("untargeted sale evaluation must not error: %v", err)
	}
	if len(res.FreeUnits) != 0 || len(res.Bxgy) != 0 {
		t.Fatalf("untargeted sale must grant nothing, got %+v", res)
	}
}

// TestFix20_BxgyAbsurdXTargeted: a sale of the targeted product with a
// stored X = 2^61 must be refused with an error — never panic, never
// store a grant.
func TestFix20_BxgyAbsurdXTargeted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	target := b1MustProduct(t, s, admin, "منتج مستهدف")
	fix20InsertAbsurdOffer(t, s, "عرض ذعر", target.ID, 1<<61, 1)

	if _, err := s.EvaluateOffers(ctx, b1PromoLines(target.ID, 1000, 2000, 1000, "")); err == nil {
		t.Fatal("targeted sale with BuyX=2^61 must be refused, got no error")
	}
}

// TestFix20_BxgyAbsurdYWrap: X = 1, Y = 2^61+500000 used to wrap
// groups*y*1000 to exactly 500000000 milliunits (500000 free units)
// stored on HTTP 200. The evaluation must refuse instead.
func TestFix20_BxgyAbsurdYWrap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	target := b1MustProduct(t, s, admin, "قهوة")
	fix20InsertAbsurdOffer(t, s, "عرض التفاف", target.ID, 1, (1<<61)+500000)

	res, err := s.EvaluateOffers(ctx, b1PromoLines(target.ID, 1000, 3000, 500, ""))
	if err == nil {
		t.Fatalf("absurd-Y evaluation must be refused, got free grant %+v", res.FreeUnits)
	}
	if res != nil {
		if got := res.FreeUnits[target.ID]; got == 500000000 {
			t.Fatalf("wrapped grant 500000000 milliunits must be impossible, got %+v", res.FreeUnits)
		}
	}
}

// TestFix20_BxgyNormalStillWorks: a sane buy-2-get-1 offer grants
// exactly the configured free units (regression guard for the fix).
func TestFix20_BxgyNormalStillWorks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p := b1MustProduct(t, s, admin, "منتج عادي")
	b1CreateOffer(t, s, OfferInput{Name: "اشترِ 2 واحصل على 1", Type: OfferBuyXGetY, BuyX: 2, FreeY: 1,
		IsActive: true, Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})

	// 1 unit: no group, no grant.
	res, err := s.EvaluateOffers(ctx, b1PromoLines(p.ID, 1000, 2000, 1000, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.FreeUnits) != 0 {
		t.Fatalf("1 unit must grant nothing, got %+v", res.FreeUnits)
	}
	// 2 units: exactly 1 free unit (1000 milliunits).
	res, err = s.EvaluateOffers(ctx, b1PromoLines(p.ID, 2000, 2000, 1000, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.FreeUnits[p.ID]; got != 1000 {
		t.Fatalf("buy 2 get 1 on 2 units: free = %d, want 1000", got)
	}
	if info := res.Bxgy[p.ID]; info.FreeQty != 1000 || info.BuyX != 2000 {
		t.Fatalf("BxgyInfo = %+v, want FreeQty 1000 BuyX 2000", info)
	}
	// 4 units: exactly 2 free units.
	res, err = s.EvaluateOffers(ctx, b1PromoLines(p.ID, 4000, 2000, 1000, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.FreeUnits[p.ID]; got != 2000 {
		t.Fatalf("buy 2 get 1 on 4 units: free = %d, want 2000", got)
	}
}

// TestFix20_EarnedFreeUnitsOverflowSafe: the legacy helper must never
// return a wrapped value — (2000 paid / 1000 X) * MaxInt64 wraps to -2
// unchecked, and must come back 0 now. Sane values are unchanged.
func TestFix20_EarnedFreeUnitsOverflowSafe(t *testing.T) {
	if got := EarnedFreeUnits(2000, 1000, math.MaxInt64); got != 0 {
		t.Fatalf("EarnedFreeUnits overflow: got %d, want 0 (no wrap)", got)
	}
	if got := EarnedFreeUnits(4000, 2000, 1000); got != 2000 {
		t.Fatalf("EarnedFreeUnits(4, 2, 1) = %d, want 2000", got)
	}
	if got := EarnedFreeUnits(2500, 2000, 1000); got != 1000 {
		t.Fatalf("EarnedFreeUnits(2.5, 2, 1) = %d, want 1000", got)
	}
	if got := EarnedFreeUnits(1000, 0, 1000); got != 0 {
		t.Fatalf("EarnedFreeUnits with X=0 = %d, want 0 (no divide by zero)", got)
	}
}
