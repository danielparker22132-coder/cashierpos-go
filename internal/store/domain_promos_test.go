package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

func b1CreateOffer(t *testing.T, s *Store, in OfferInput) *Offer {
	t.Helper()
	admin := b1Admin(t, s)
	o, err := s.CreateOffer(context.Background(), admin, in)
	if err != nil {
		t.Fatalf("create offer %+v: %v", in, err)
	}
	return o
}

// mustMulQty is MulQty for test constants that can never overflow.
func mustMulQty(unit money.Cents, qty money.Qty) money.Cents {
	v, err := money.MulQty(unit, qty)
	if err != nil {
		panic(err)
	}
	return v
}

func b1PromoLines(pid int64, qty money.Qty, unit, buy money.Cents, cat string) []PromoLine {
	total := mustMulQty(unit, qty)
	return []PromoLine{{ProductID: pid, Category: cat, Qty: qty, UnitPrice: unit,
		LineGross: total, ItemDiscount: 0, LineTotal: total, BuyPrice: buy}}
}

func TestB1OfferValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	cashier := b2TestUser(t, s, "b1ocash", "cashier")
	p := b1MustProduct(t, s, admin, "سكر")

	if _, err := s.CreateOffer(ctx, cashier, OfferInput{Name: "x", Type: OfferProductPct, Percent: 10,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}); err == nil {
		t.Fatal("cashier create offer should be denied")
	}
	bad := []struct {
		in   OfferInput
		want string
	}{
		{OfferInput{Name: "", Type: OfferProductPct, Percent: 10}, "اسم العرض"},
		{OfferInput{Name: "x", Type: "nope"}, "نوع العرض"},
		{OfferInput{Name: "x", Type: OfferProductPct, Percent: 0, Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}, "بين 1 و 100"},
		{OfferInput{Name: "x", Type: OfferProductPct, Percent: 101, Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}, "بين 1 و 100"},
		{OfferInput{Name: "x", Type: OfferProductPct, Percent: 10}, "منتجًا واحدًا"},
		{OfferInput{Name: "x", Type: OfferCategoryPct, Percent: 10}, "فئة واحدة"},
		{OfferInput{Name: "x", Type: OfferInvoiceFixed, Amount: 0}, "أكبر من صفر"},
		{OfferInput{Name: "x", Type: OfferThresholdPct, Percent: 10, Threshold: 0}, "الحد الأدنى"},
		{OfferInput{Name: "x", Type: OfferBuyXGetY, BuyX: 2, FreeY: 1}, "منتجًا واحدًا فقط"},
		{OfferInput{Name: "x", Type: OfferBundleFixed, Amount: 1000, Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID, Qty: 1000}}}, "منتجين على الأقل"},
		{OfferInput{Name: "x", Type: OfferProductPct, Percent: 10, StartAt: "2026-01-10", EndAt: "2026-01-01",
			Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}, "قبل تاريخ النهاية"},
		{OfferInput{Name: "x", Type: OfferProductPct, Percent: 10,
			Targets: []OfferTarget{{TargetType: "product", TargetID: 99999}}}, "غير موجود"},
		// product_price (R-P11): negative refused; >= current retail
		// (2000) refused at creation; no product target refused.
		{OfferInput{Name: "x", Type: OfferProductPrice, Amount: -100,
			Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}, "سالب"},
		{OfferInput{Name: "x", Type: OfferProductPrice, Amount: 2000,
			Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}, "أقل من سعر البيع"},
		{OfferInput{Name: "x", Type: OfferProductPrice, Amount: 2500,
			Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}, "أقل من سعر البيع"},
		{OfferInput{Name: "x", Type: OfferProductPrice, Amount: 1500}, "منتجًا واحدًا"},
	}
	for i, c := range bad {
		if _, err := s.CreateOffer(ctx, admin, c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("case %d: err = %v, want %q", i, err, c.want)
		}
	}
	// product_price 0 = free is allowed (below the 2000 retail price).
	if _, err := s.CreateOffer(ctx, admin, OfferInput{Name: "مجاني", Type: OfferProductPrice, Amount: 0, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}); err != nil {
		t.Fatalf("product_price 0 (free) should be allowed: %v", err)
	}
	// Cashier may not create a product_price offer either.
	if _, err := s.CreateOffer(ctx, cashier, OfferInput{Name: "x", Type: OfferProductPrice, Amount: 1500,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}}); err == nil {
		t.Fatal("cashier create product_price offer should be denied")
	}
}

func TestB1OfferEvaluateAllTypes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p1 := b1MustProduct(t, s, admin, "سكر") // retail 2000, buy 1000
	p2 := b1MustProduct(t, s, admin, "زيت") // retail 2000, buy 1000
	// Categories for category_pct.
	upd := b1ProductInput("سكر")
	upd.Category = "مواد غذائية"
	upd.Quantity = 10000
	upd.MinQuantity = 5000
	if _, err := s.UpdateProduct(ctx, admin, p1.ID, upd); err != nil {
		t.Fatalf("set category: %v", err)
	}

	// product_pct 10% on p1, 2 units: line 4000 -> 400 saved.
	b1CreateOffer(t, s, OfferInput{Name: "خصم سكر", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, err := s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, "مواد غذائية"))
	if err != nil {
		t.Fatal(err)
	}
	if res.LinePromoTotal != 400 || len(res.Applied) != 1 {
		t.Fatalf("product_pct: %+v", res)
	}

	// category_pct 20% beats product_pct 10% (best single, no stacking).
	b1CreateOffer(t, s, OfferInput{Name: "خصم فئة", Type: OfferCategoryPct, Percent: 20, IsActive: true,
		Targets: []OfferTarget{{TargetType: "category", TargetName: "مواد غذائية"}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, "مواد غذائية"))
	if res.LinePromoTotal != 800 {
		t.Fatalf("category_pct should win with 800, got %+v", res)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("no stacking: applied = %+v", res.Applied)
	}

	// product_fixed 500 per unit on a qty-2 line saves 500×2 = 1000,
	// which beats both pct offers (800) — per-unit (R-P10), not flat.
	b1CreateOffer(t, s, OfferInput{Name: "ثابت سكر", Type: OfferProductFixed, Amount: 500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, "مواد غذائية"))
	if res.LinePromoTotal != 1000 {
		t.Fatalf("product_fixed 500 per unit (×2) should win with 1000: %+v", res)
	}
	// product_fixed 1500 per unit wins over 800: 1500×2 = 3000.
	b1CreateOffer(t, s, OfferInput{Name: "ثابت كبير", Type: OfferProductFixed, Amount: 1500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, "مواد غذائية"))
	if res.LinePromoTotal != 3000 {
		t.Fatalf("product_fixed 1500 per unit (×2) should win with 3000: %+v", res)
	}

	// invoice_fixed 1000 vs threshold_pct 50% over 5000: threshold wins on 2 lines.
	b1CreateOffer(t, s, OfferInput{Name: "ثابت فاتورة", Type: OfferInvoiceFixed, Amount: 1000, IsActive: true})
	b1CreateOffer(t, s, OfferInput{Name: "حد أدنى", Type: OfferThresholdPct, Percent: 50, Threshold: 5000, IsActive: true})
	lines := append(b1PromoLines(p1.ID, 2000, 2000, 1000, "مواد غذائية"),
		b1PromoLines(p2.ID, 2000, 2000, 1000, "")...)
	res, _ = s.EvaluateOffers(ctx, lines)
	// subtotal after line promos: p1 line 4000-3000=1000, p2 4000 -> 5000; 50% = 2500 > 1000.
	if res.InvoiceBest != 2500 || res.InvoiceBestOffer == nil {
		t.Fatalf("threshold best = %d, offer %+v (res %+v)", res.InvoiceBest, res.InvoiceBestOffer, res)
	}
}

// TestB1OfferProductFixedPerUnit pins R-P10 against PyQt
// (tests/test_promo_offers.py test_product_fixed_per_unit_math_and_records):
// the fixed amount is off the product's price PER UNIT.
func TestB1OfferProductFixedPerUnit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p1 := b1MustProduct(t, s, admin, "سكر")

	// qty 3 × 10 EGP on a 25 EGP product -> saved 30.00, net 45.00.
	b1CreateOffer(t, s, OfferInput{Name: "ثابت للوحدة", Type: OfferProductFixed, Amount: 1000, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, err := s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 3000, 2500, 1000, ""))
	if err != nil {
		t.Fatal(err)
	}
	if res.LinePromoTotal != 3000 || len(res.Applied) != 1 {
		t.Fatalf("product_fixed per unit (3 × 1000): %+v", res)
	}

	// Cap: 10/unit on a price-8 product, qty 2 -> raw 2000 capped at
	// the line total 1600.
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	b1CreateOffer(t, s, OfferInput{Name: "ثابت للوحدة", Type: OfferProductFixed, Amount: 1000, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 800, 1000, ""))
	if res.LinePromoTotal != 1600 {
		t.Fatalf("product_fixed capped at line total: %+v", res)
	}
}

func TestB1OfferProductPrice(t *testing.T) {
	ctx := context.Background()

	// Basic: retail 2000, promo price 1500, 2 units -> saves (2000-1500)*2 = 1000.
	s := testStore(t)
	admin := b1Admin(t, s)
	p1 := b1MustProduct(t, s, admin, "سكر")
	o := b1CreateOffer(t, s, OfferInput{Name: "سعر ترويجي سكر", Type: OfferProductPrice, Amount: 1500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, err := s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, ""))
	if err != nil {
		t.Fatal(err)
	}
	if res.LinePromoTotal != 1000 || len(res.Applied) != 1 || res.Applied[0].OfferID != o.ID {
		t.Fatalf("product_price basic: %+v", res)
	}
	// Price dropped to the promo price (or below) -> saving is 0, never negative.
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 1000, 1500, 1000, ""))
	if res.LinePromoTotal != 0 || len(res.Applied) != 0 {
		t.Fatalf("unit price == promo price must save 0: %+v", res)
	}
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 1000, 1000, 1000, ""))
	if res.LinePromoTotal != 0 {
		t.Fatalf("unit price below promo price must save 0: %+v", res)
	}

	// Free: promo price 0 -> the whole line total is saved.
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	b1CreateOffer(t, s, OfferInput{Name: "مجاني", Type: OfferProductPrice, Amount: 0, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, ""))
	if res.LinePromoTotal != 4000 || len(res.Applied) != 1 {
		t.Fatalf("product_price 0 (free): %+v", res)
	}

	// Cap: saving is capped at the line total after the manual item discount.
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	// FIX-3 L5: the 500 promo price is below the default BuyPrice 1000,
	// so lower BuyPrice (to 400) — the cap math below is unchanged.
	upd := b1ProductInput("سكر")
	upd.BuyPrice = 400
	if _, err := s.UpdateProduct(ctx, admin, p1.ID, upd); err != nil {
		t.Fatalf("lower BuyPrice: %v", err)
	}
	b1CreateOffer(t, s, OfferInput{Name: "سعر 500", Type: OfferProductPrice, Amount: 500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	discounted := []PromoLine{{ProductID: p1.ID, Qty: 2000, UnitPrice: 2000,
		LineGross: 4000, ItemDiscount: 3600, LineTotal: 400, BuyPrice: 1000}}
	res, _ = s.EvaluateOffers(ctx, discounted)
	// (2000-500)*2 = 3000 raw, capped at the 400 line total.
	if res.LinePromoTotal != 400 {
		t.Fatalf("product_price capped at line total: %+v", res)
	}

	// Competition (R-P1, best single only): promo price 1500 saves 1000
	// on 2 units, beats pct 10% (400) and category 20% (800), loses to
	// product_fixed 1500 per unit (1500×2 = 3000); exactly one offer
	// applies in each case.
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	upd = b1ProductInput("سكر")
	upd.Category = "مواد غذائية"
	upd.Quantity = 10000
	upd.MinQuantity = 5000
	if _, err := s.UpdateProduct(ctx, admin, p1.ID, upd); err != nil {
		t.Fatalf("set category: %v", err)
	}
	priceOffer := b1CreateOffer(t, s, OfferInput{Name: "سعر ترويجي", Type: OfferProductPrice, Amount: 1500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	b1CreateOffer(t, s, OfferInput{Name: "خصم 10%", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	b1CreateOffer(t, s, OfferInput{Name: "خصم فئة 20%", Type: OfferCategoryPct, Percent: 20, IsActive: true,
		Targets: []OfferTarget{{TargetType: "category", TargetName: "مواد غذائية"}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, "مواد غذائية"))
	if res.LinePromoTotal != 1000 || len(res.Applied) != 1 || res.Applied[0].OfferID != priceOffer.ID {
		t.Fatalf("product_price should beat pct/category: %+v", res)
	}
	fixedOffer := b1CreateOffer(t, s, OfferInput{Name: "ثابت 1500", Type: OfferProductFixed, Amount: 1500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, "مواد غذائية"))
	if res.LinePromoTotal != 3000 || len(res.Applied) != 1 || res.Applied[0].OfferID != fixedOffer.ID {
		t.Fatalf("product_fixed 1500 per unit (×2 = 3000) should beat product_price: %+v", res)
	}

	// Competition with buy_x_get_y: bxgy (buy 2 get 1, saved 2000) beats
	// a 1500 promo price (saved 1000 on 2 units); a 100 promo price
	// (saved 3800) beats bxgy.
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	b1CreateOffer(t, s, OfferInput{Name: "سعر 1500", Type: OfferProductPrice, Amount: 1500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	b1CreateOffer(t, s, OfferInput{Name: "اشتر 2 واحصل على 1", Type: OfferBuyXGetY, BuyX: 2, FreeY: 1, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, ""))
	if res.LinePromoTotal != 0 || res.FreeUnits[p1.ID] != 1000 {
		t.Fatalf("bxgy should beat product_price 1500: %+v", res)
	}
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	// FIX-3 L5: the 100 promo price is below the default BuyPrice 1000,
	// so lower BuyPrice (to 50) — the bxgy competition math is unchanged.
	upd = b1ProductInput("سكر")
	upd.BuyPrice = 50
	if _, err := s.UpdateProduct(ctx, admin, p1.ID, upd); err != nil {
		t.Fatalf("lower BuyPrice: %v", err)
	}
	b1CreateOffer(t, s, OfferInput{Name: "سعر 100", Type: OfferProductPrice, Amount: 100, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	b1CreateOffer(t, s, OfferInput{Name: "اشتر 2 واحصل على 1", Type: OfferBuyXGetY, BuyX: 2, FreeY: 1, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, ""))
	if res.LinePromoTotal != 3800 || len(res.FreeUnits) != 0 {
		t.Fatalf("product_price 100 should beat bxgy: %+v", res)
	}

	// Expiry window: ended yesterday -> nothing; ending today -> applies;
	// starting tomorrow -> nothing.
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	b1CreateOffer(t, s, OfferInput{Name: "منتهٍ", Type: OfferProductPrice, Amount: 1500, IsActive: true,
		EndAt: addDays(Today(), -1), Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	b1CreateOffer(t, s, OfferInput{Name: "قادم", Type: OfferProductPrice, Amount: 1500, IsActive: true,
		StartAt: addDays(Today(), 1), Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, ""))
	if res.LinePromoTotal != 0 || len(res.Applied) != 0 {
		t.Fatalf("expired/future product_price must not apply: %+v", res)
	}
	b1CreateOffer(t, s, OfferInput{Name: "ينتهي اليوم", Type: OfferProductPrice, Amount: 1500, IsActive: true,
		EndAt: Today(), Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, ""))
	if res.LinePromoTotal != 1000 {
		t.Fatalf("product_price ending today should apply: %+v", res)
	}

	// Update: raising the promo price to (or above) the current retail
	// price is refused; lowering it is accepted.
	s = testStore(t)
	admin = b1Admin(t, s)
	p1 = b1MustProduct(t, s, admin, "سكر")
	o = b1CreateOffer(t, s, OfferInput{Name: "سعر", Type: OfferProductPrice, Amount: 1500, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	for _, bad := range []money.Cents{2000, 2500} {
		in := OfferInput{Name: "سعر", Type: OfferProductPrice, Amount: bad, IsActive: true,
			Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}}
		if _, err := s.UpdateOffer(ctx, admin, o.ID, in); err == nil || !strings.Contains(err.Error(), "أقل من سعر البيع") {
			t.Fatalf("update to %d: err = %v, want retail-price refusal", bad, err)
		}
	}
	in := OfferInput{Name: "سعر", Type: OfferProductPrice, Amount: 1200, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}}
	updated, err := s.UpdateOffer(ctx, admin, o.ID, in)
	if err != nil {
		t.Fatalf("update to 1200: %v", err)
	}
	if updated.Value != "1200" {
		t.Fatalf("updated value = %q, want 1200", updated.Value)
	}
	if got := OfferTypeArabic(OfferProductPrice); got != "سعر ترويجي على منتج" {
		t.Fatalf("OfferTypeArabic(product_price) = %q", got)
	}
}

func TestB1OfferBundleAndBxgy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p1 := b1MustProduct(t, s, admin, "سكر")
	p2 := b1MustProduct(t, s, admin, "زيت")

	// bundle: p1+p2 for 3000 instead of 4000 -> saves 1000.
	b1CreateOffer(t, s, OfferInput{Name: "حزمة", Type: OfferBundleFixed, Amount: 3000, IsActive: true,
		Targets: []OfferTarget{
			{TargetType: "product", TargetID: p1.ID, Qty: 1000},
			{TargetType: "product", TargetID: p2.ID, Qty: 1000},
		}})
	lines := append(b1PromoLines(p1.ID, 1000, 2000, 1000, ""),
		b1PromoLines(p2.ID, 1000, 2000, 1000, "")...)
	res, err := s.EvaluateOffers(ctx, lines)
	if err != nil {
		t.Fatal(err)
	}
	if res.LinePromoTotal != 1000 || len(res.Applied) != 1 {
		t.Fatalf("bundle: %+v", res)
	}
	// Bundle requires both products: p1 alone gets nothing.
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 1000, 2000, 1000, ""))
	if res.LinePromoTotal != 0 {
		t.Fatalf("bundle without p2 should not apply: %+v", res)
	}

	// buy 2 get 1 free on p1: 3 units -> 1 free, saved = 2000 retail, no net reduction.
	b1CreateOffer(t, s, OfferInput{Name: "اشتر 2 واحصل على 1", Type: OfferBuyXGetY, BuyX: 2, FreeY: 1, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 3000, 2000, 1000, ""))
	if res.FreeUnits[p1.ID] != 1000 {
		t.Fatalf("bxgy free = %+v", res.FreeUnits)
	}
	if res.FreeCost != 1000 {
		t.Fatalf("bxgy free cost = %d, want 1000", res.FreeCost)
	}
	if res.LinePromoTotal != 0 {
		t.Fatalf("bxgy must not reduce net: %+v", res)
	}
	if res.TotalSaved != 2000 {
		t.Fatalf("bxgy saved = %d, want 2000", res.TotalSaved)
	}
	// 2 paid units -> 1 free unit (earned_free = floor(kept_paid/X)*Y).
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 2000, 2000, 1000, ""))
	if res.FreeUnits[p1.ID] != 1000 {
		t.Fatalf("bxgy 2 paid units should grant 1 free: %+v", res)
	}
	// 1 unit -> no free unit yet.
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 1000, 2000, 1000, ""))
	if len(res.FreeUnits) != 0 {
		t.Fatalf("bxgy 1 unit should grant nothing: %+v", res)
	}
	// bxgy competes with a line pct: 50% pct on the line (3000 saved on 3 units)
	// beats bxgy's 2000 saved -> bxgy loses.
	b1CreateOffer(t, s, OfferInput{Name: "خصم نصف", Type: OfferProductPct, Percent: 50, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p1.ID}}})
	res, _ = s.EvaluateOffers(ctx, b1PromoLines(p1.ID, 3000, 2000, 1000, ""))
	if res.LinePromoTotal != 3000 || len(res.FreeUnits) != 0 {
		t.Fatalf("line pct should beat bxgy: %+v", res)
	}
}

func TestB1OfferWindowDeleteAndEarnedFree(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p := b1MustProduct(t, s, admin, "سكر")
	// Inactive offer never applies; future offer never applies.
	b1CreateOffer(t, s, OfferInput{Name: "معطل", Type: OfferProductPct, Percent: 50, IsActive: false,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	b1CreateOffer(t, s, OfferInput{Name: "قادم", Type: OfferProductPct, Percent: 50, IsActive: true,
		StartAt: "2999-01-01", Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	res, _ := s.EvaluateOffers(ctx, b1PromoLines(p.ID, 1000, 2000, 1000, ""))
	if res.LinePromoTotal != 0 || len(res.Applied) != 0 {
		t.Fatalf("inactive/future offers must not apply: %+v", res)
	}
	// Delete rule: unused offer deletes; used offer refuses.
	o := b1CreateOffer(t, s, OfferInput{Name: "للحذف", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	if err := s.DeleteOffer(ctx, admin, o.ID); err != nil {
		t.Fatalf("delete unused: %v", err)
	}
	if _, err := s.GetOffer(ctx, admin, o.ID); err == nil {
		t.Fatal("deleted offer should be gone")
	}
	// Simulate a used offer: insert an InvoiceOffers row. (FIX-1 L7:
	// the FKs require a real parent invoice, not a literal ID.)
	o2 := b1CreateOffer(t, s, OfferInput{Name: "مستخدم", Type: OfferProductPct, Percent: 10, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	if _, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,InvoiceDate,SaleType,PaymentMethod,
		CustomerID,TotalAmount,NetAmount,PaidAmount,RemainingAmount,IsCanceled)
		VALUES('B1-DEL', ?, 'retail', 'Cash', 1, 100, 100, 100, 0, 0)`, Today()); err != nil {
		t.Fatal(err)
	}
	var invID int64
	_ = s.DB.QueryRow(`SELECT max(InvoiceID) FROM Invoices`).Scan(&invID)
	if _, err := s.DB.Exec(`INSERT INTO InvoiceOffers(InvoiceID,OfferID,OfferName,SavedAmount) VALUES(?,?,?,100)`, invID, o2.ID, o2.Name); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOffer(ctx, admin, o2.ID); err == nil || !strings.Contains(err.Error(), "عطّل العرض") {
		t.Fatalf("delete used err = %v", err)
	}
	// Deactivate instead works.
	if _, err := s.SetOfferActive(ctx, admin, o2.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	// EarnedFreeUnits: floor(kept/X)*Y.
	if got := EarnedFreeUnits(2500, 2000, 1000); got != 1000 {
		t.Fatalf("earned(2.5, 2, 1) = %d, want 1000", got)
	}
	if got := EarnedFreeUnits(4000, 2000, 1000); got != 2000 {
		t.Fatalf("earned(4, 2, 1) = %d, want 2000", got)
	}
	if got := EarnedFreeUnits(1000, 2000, 1000); got != 0 {
		t.Fatalf("earned(1, 2, 1) = %d, want 0", got)
	}
	// List + status filter.
	all, err := s.ListOffers(ctx, admin, "all")
	if err != nil || len(all) < 3 {
		t.Fatalf("list offers: %+v %v", all, err)
	}
	inactive, _ := s.ListOffers(ctx, admin, "inactive")
	if len(inactive) != 2 { // "معطل" + deactivated o2
		t.Fatalf("inactive list = %d, want 2", len(inactive))
	}
}
