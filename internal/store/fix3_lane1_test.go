// Package store — FIX-3 lane 1 (store/logic) tests: M1/L1/L8a/L8b (cash
// counts), M4 (invoice scoping), L5/L6 (product_price promos), L7
// (sequential purchase numbers), L13 (audit count), L16 (shift error
// first), L17 (auto-closed shift status).
package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

func TestFix3Lane1_CashierCannotApproveManagerCount(t *testing.T) {
	// L1: a cashier reviews only a fellow cashier's count — a
	// manager/admin/supervisor's count is refused, even though the
	// cashier holds review_cash_count.
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	cash := b2TestUser(t, s, "cash1", "cashier")
	cc, _ := b2Count(t, s, mgr, money.Cents(10000), money.Cents(9000))
	if _, err := s.ReviewCashCount(ctx, cash, cc.ID, true, ""); err == nil ||
		!strings.Contains(err.Error(), "كاشير آخر فقط") {
		t.Fatalf("cashier approving manager count: want peer-only refusal, got %v", err)
	}
	var status string
	_ = s.DB.QueryRow(`SELECT ReviewStatus FROM CashCounts WHERE CountID=?`, cc.ID).Scan(&status)
	if status != "pending" {
		t.Fatalf("refused review changed status to %q", status)
	}
	// Admin may still approve a manager's count.
	admin := b2TestUser(t, s, "admin1", "admin")
	if _, err := s.ReviewCashCount(ctx, admin, cc.ID, true, ""); err != nil {
		t.Fatalf("admin approving manager count: %v", err)
	}
}

func TestFix3Lane1_AdminSelfReviewAllowedAndAudited(t *testing.T) {
	// C-3 (jony's ruling 2026-11-25, reversing FIX-3 M1 for
	// Manager/Admin only): an admin MAY approve their own count —
	// the approval succeeds, the count leaves pending, exactly one
	// approve audit row is written, and no self-review-attempt row.
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	cc, _ := b2Count(t, s, admin, money.Cents(10000), money.Cents(9000))
	before := b2ReviewAuditCount(t, s)
	rev, err := s.ReviewCashCount(ctx, admin, cc.ID, true, "")
	if err != nil {
		t.Fatalf("admin self-approve must succeed, got %v", err)
	}
	if rev.ReviewStatus != "approved" {
		t.Fatalf("self-approved count status = %q, want approved", rev.ReviewStatus)
	}
	if b2ReviewAuditCount(t, s) != before+1 {
		t.Fatal("self-approval must write exactly one approve audit row")
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='محاولة اعتماد عدّ خاص'`).Scan(&n)
	if n != 0 {
		t.Fatalf("self-approval must write no attempt audit rows, got %d", n)
	}
	var notes string
	_ = s.DB.QueryRow(`SELECT Notes FROM AuditLog WHERE ActionType='اعتماد فرق عدّ النقدية'`).Scan(&notes)
	if !strings.Contains(notes, "admin1") {
		t.Fatalf("approve audit must name the owner, got %q", notes)
	}
}

func TestFix3Lane1_LegacyCountOwnerFallback(t *testing.T) {
	// L8b: a legacy row (CountedByUserID 0) falls back to resolving the
	// counted-by username to an id — the owner is still recognised,
	// and a peer cashier may still approve.
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	b := b2TestUser(t, s, "cashB", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	if _, err := s.DB.Exec(`UPDATE CashCounts SET CountedByUserID=0 WHERE CountID=?`, cc.ID); err != nil {
		t.Fatalf("strip stored id: %v", err)
	}
	if _, err := s.ReviewCashCount(ctx, a, cc.ID, true, ""); err == nil ||
		!strings.Contains(err.Error(), "عدّك الخاص") {
		t.Fatalf("legacy owner self-review must be refused via lookup, got %v", err)
	}
	if _, err := s.ReviewCashCount(ctx, b, cc.ID, true, ""); err != nil {
		t.Fatalf("peer approve of legacy count: %v", err)
	}
}

func TestFix3Lane1_StoredIDBeatsReusedUsername(t *testing.T) {
	// L8b sharp case: the counting user is deleted and a NEW user
	// reuses the username. The count's stored CountedByUserID still
	// points at the deleted user, so the new user is NOT the owner —
	// the old bare-username fallback would have called them the owner.
	// The new user is refused, but by the L1 peer rule (unresolvable
	// owner), not by the self-review ban.
	s := testStore(t)
	ctx := context.Background()
	a := b2TestUser(t, s, "cashA", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	if _, err := s.DB.Exec(`DELETE FROM Users WHERE UserID=?`, a.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	a2 := b2TestUser(t, s, "cashA", "cashier")
	if a2.ID == a.ID {
		t.Fatal("reused username must get a different user id")
	}
	_, err := s.ReviewCashCount(ctx, a2, cc.ID, true, "")
	if err == nil || strings.Contains(err.Error(), "عدّك الخاص") {
		t.Fatalf("reused-username reviewer must NOT hit the self-review ban, got %v", err)
	}
	if !strings.Contains(err.Error(), "كاشير آخر فقط") {
		t.Fatalf("reused-username reviewer must hit the L1 peer refusal, got %v", err)
	}
	// An unresolvable-owner legacy row is reviewable by an admin.
	if _, err := s.DB.Exec(`UPDATE CashCounts SET CountedByUserID=0, CountedBy='ghost' WHERE CountID=?`, cc.ID); err != nil {
		t.Fatalf("ghost row: %v", err)
	}
	admin := b2TestUser(t, s, "admin1", "admin")
	if _, err := s.ReviewCashCount(ctx, admin, cc.ID, true, ""); err != nil {
		t.Fatalf("admin approving unresolvable-owner count: %v", err)
	}
}

func TestFix3Lane1_RecordCashCountStoresUserID(t *testing.T) {
	s := testStore(t)
	a := b2TestUser(t, s, "cashA", "cashier")
	cc, _ := b2Count(t, s, a, money.Cents(10000), money.Cents(9000))
	if cc.CountedByUserID != a.ID {
		t.Fatalf("CountedByUserID = %d, want %d", cc.CountedByUserID, a.ID)
	}
	var stored int64
	_ = s.DB.QueryRow(`SELECT CountedByUserID FROM CashCounts WHERE CountID=?`, cc.ID).Scan(&stored)
	if stored != a.ID {
		t.Fatalf("stored CountedByUserID = %d, want %d", stored, a.ID)
	}
}

func TestFix3Lane1_InvoiceForbidden(t *testing.T) {
	// M4: cashier reads only their own invoices; admin/manager/supervisor
	// read any. FIX-17 SW2-L10: warehouse reads any invoice view-only.
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر")
	b1OpenShiftFor(t, s, env.cashier)
	res := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 1000)
	otherCash := b2TestUser(t, s, "cash2", "cashier")
	wh := b2TestUser(t, s, "wh1", "warehouse")
	if _, err := s.GetInvoice(env.ctx, otherCash, res.InvoiceID); !errors.Is(err, ErrInvoiceForbidden) {
		t.Fatalf("other cashier GetInvoice: want ErrInvoiceForbidden, got %v", err)
	}
	// FIX-17 SW2-L10 (parent ruling, §15 row 25): warehouse now views any
	// invoice read-only; cashier stays own-only.
	if _, err := s.GetInvoice(env.ctx, wh, res.InvoiceID); err != nil {
		t.Fatalf("warehouse GetInvoice: want success (view-only), got %v", err)
	}
	if _, err := s.GetInvoice(env.ctx, env.sup, res.InvoiceID); err != nil {
		t.Fatalf("supervisor GetInvoice: %v", err)
	}
	if _, err := s.GetInvoice(env.ctx, env.manager, res.InvoiceID); err != nil {
		t.Fatalf("manager GetInvoice: %v", err)
	}
	// The owner themself may read it.
	if _, err := s.GetInvoice(env.ctx, env.cashier, res.InvoiceID); err != nil {
		t.Fatalf("owner GetInvoice: %v", err)
	}
}

func TestFix3Lane1_PromoBelowBuyPrice(t *testing.T) {
	// L5: 0 < promo < BuyPrice refused; promo == BuyPrice allowed;
	// promo 0 (free) exempt; the gate also applies on update.
	s := testStore(t)
	ctx := context.Background()
	admin := b1Admin(t, s)
	p := b1MustProduct(t, s, admin, "سكر") // retail 2000, buy 1000
	target := []OfferTarget{{TargetType: "product", TargetID: p.ID}}
	if _, err := s.CreateOffer(ctx, admin, OfferInput{Name: "رخيص", Type: OfferProductPrice,
		Amount: 999, Targets: target}); err == nil || !strings.Contains(err.Error(), "سعر الشراء") {
		t.Fatalf("promo 999 < buy 1000: want BuyPrice refusal, got %v", err)
	}
	if _, err := s.CreateOffer(ctx, admin, OfferInput{Name: "يساوي الشراء", Type: OfferProductPrice,
		Amount: 1000, IsActive: true, Targets: target}); err != nil {
		t.Fatalf("promo == BuyPrice must be allowed: %v", err)
	}
	if _, err := s.CreateOffer(ctx, admin, OfferInput{Name: "مجاني", Type: OfferProductPrice,
		Amount: 0, IsActive: true, Targets: target}); err != nil {
		t.Fatalf("promo 0 (free) must be allowed: %v", err)
	}
	o, err := s.CreateOffer(ctx, admin, OfferInput{Name: "سعر", Type: OfferProductPrice,
		Amount: 1500, IsActive: true, Targets: target})
	if err != nil {
		t.Fatalf("promo 1500: %v", err)
	}
	if _, err := s.UpdateOffer(ctx, admin, o.ID, OfferInput{Name: "سعر", Type: OfferProductPrice,
		Amount: 500, IsActive: true, Targets: target}); err == nil ||
		!strings.Contains(err.Error(), "سعر الشراء") {
		t.Fatalf("update to promo 500 < buy: want BuyPrice refusal, got %v", err)
	}
}

func TestFix3Lane1_ProductPriceRepricing(t *testing.T) {
	// L6: the winning product_price line stores UnitPrice = promo
	// price, its LinePromos share is zeroed; gross/profit recomputed;
	// net/saved/InvoiceOffers UNCHANGED (net-neutral transform).
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000
	b1CreateOffer(t, s, OfferInput{Name: "سعر ترويجي", Type: OfferProductPrice, Amount: 1500,
		IsActive: true, Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	shiftID := b1OpenShiftFor(t, s, env.cashier)
	res := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 2000)
	if res.Gross != 3000 || res.Net != 3000 || res.PromoDiscount != 0 {
		t.Fatalf("repriced result gross/net/promo = %d/%d/%d, want 3000/3000/0", res.Gross, res.Net, res.PromoDiscount)
	}
	if res.Profit != 1000 { // (1500-1000)*2
		t.Fatalf("profit = %d, want 1000", res.Profit)
	}
	if res.ShiftID != shiftID {
		t.Fatalf("shift = %d, want %d", res.ShiftID, shiftID)
	}
	inv, err := s.GetInvoice(env.ctx, env.admin, res.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(inv.Lines))
	}
	ln := inv.Lines[0]
	if ln.UnitPrice != 1500 || ln.TotalPrice != 3000 || ln.PromoDiscount != 0 {
		t.Fatalf("line unit/total/promo = %d/%d/%d, want 1500/3000/0", ln.UnitPrice, ln.TotalPrice, ln.PromoDiscount)
	}
	if inv.Summary.Total != 3000 || inv.Summary.Net != 3000 || inv.Summary.PromoDiscount != 0 {
		t.Fatalf("header total/net/promo = %d/%d/%d, want 3000/3000/0",
			inv.Summary.Total, inv.Summary.Net, inv.Summary.PromoDiscount)
	}
	// Saved 1000 and the InvoiceOffers row are unchanged.
	if res.PromoSaved != 1000 {
		t.Fatalf("saved = %d, want 1000", res.PromoSaved)
	}
	var saved int64
	var rows int
	_ = s.DB.QueryRow(`SELECT COALESCE(SUM(SavedAmount),0), COUNT(*) FROM InvoiceOffers WHERE InvoiceID=?`,
		res.InvoiceID).Scan(&saved, &rows)
	if saved != 1000 || rows != 1 {
		t.Fatalf("InvoiceOffers saved/rows = %d/%d, want 1000/1", saved, rows)
	}
	// Profit via InvoiceProfit agrees with the repriced line.
	profit, err := s.InvoiceProfit(env.ctx, env.admin, res.InvoiceID)
	if err != nil || profit != 1000 {
		t.Fatalf("InvoiceProfit = %d (%v), want 1000", profit, err)
	}
}

func TestFix3Lane1_ProductPriceCappedKeepsDiscountForm(t *testing.T) {
	// L6 guard: when the product_price saving is capped at the line
	// total (heavy item discount), the line keeps the discount form —
	// repricing would change the net, so UnitPrice stays retail.
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر")
	upd := b1ProductInput("سكر")
	upd.BuyPrice = 400
	if _, err := s.UpdateProduct(env.ctx, env.admin, p.ID, upd); err != nil {
		t.Fatalf("lower BuyPrice: %v", err)
	}
	b1CreateOffer(t, s, OfferInput{Name: "سعر 500", Type: OfferProductPrice, Amount: 500,
		IsActive: true, Targets: []OfferTarget{{TargetType: "product", TargetID: p.ID}}})
	b1OpenShiftFor(t, s, env.admin)
	res, err := s.CreateInvoice(env.ctx, env.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", AllowBelowCost: true,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000, ItemDiscount: 3600}},
	})
	if err != nil {
		t.Fatalf("capped sale: %v", err)
	}
	// Net 0: gross 4000 - item discount 3600 - capped promo 400.
	if res.Net != 0 || res.Gross != 4000 || res.PromoDiscount != 400 {
		t.Fatalf("capped result gross/net/promo = %d/%d/%d, want 4000/0/400",
			res.Gross, res.Net, res.PromoDiscount)
	}
	inv, _ := s.GetInvoice(env.ctx, env.admin, res.InvoiceID)
	if inv.Lines[0].UnitPrice != 2000 || inv.Lines[0].TotalPrice != 400 || inv.Lines[0].PromoDiscount != 400 {
		t.Fatalf("capped line unit/total/promo = %d/%d/%d, want 2000/400/400",
			inv.Lines[0].UnitPrice, inv.Lines[0].TotalPrice, inv.Lines[0].PromoDiscount)
	}
}

func TestFix3Lane1_PurchaseNumbersSequential(t *testing.T) {
	// L7: sequential PI-%06d numbers from the posting transaction;
	// legacy timestamp numbers do not count toward the sequence.
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد"})
	prod := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(5000))
	if _, err := s.DB.Exec(`INSERT INTO PurchaseInvoices(InvoiceNumber,VendorID,PurchaseDate,TotalAmount)
		VALUES('PI-20261125143000',?, '2026-11-25', 1000)`, vid); err != nil {
		t.Fatalf("legacy purchase row: %v", err)
	}
	in := PurchaseInput{VendorID: vid, PaymentMethod: "Visa",
		Lines: []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(1000), PurchasePrice: money.Cents(1200)}}}
	_, no1, err := s.CreatePurchase(ctx, admin, in)
	if err != nil {
		t.Fatalf("purchase 1: %v", err)
	}
	if no1 != "PI-000001" {
		t.Fatalf("purchase 1 number = %q, want PI-000001 (legacy timestamp must not count)", no1)
	}
	_, no2, err := s.CreatePurchase(ctx, admin, in)
	if err != nil {
		t.Fatalf("purchase 2: %v", err)
	}
	if no2 != "PI-000002" {
		t.Fatalf("purchase 2 number = %q, want PI-000002", no2)
	}
	var stored string
	_ = s.DB.QueryRow(`SELECT InvoiceNumber FROM PurchaseInvoices WHERE InvoiceNumber='PI-000002'`).Scan(&stored)
	if stored != "PI-000002" {
		t.Fatalf("stored purchase 2 number = %q", stored)
	}
}

func TestFix3Lane1_CreateInvoiceNoShiftErrorFirst(t *testing.T) {
	// L16: with no open shift, a bad product reports the shift error,
	// not "المنتج غير موجود".
	s := testStore(t)
	ctx := context.Background()
	cash := b2TestUser(t, s, "cash1", "cashier")
	_, err := s.CreateInvoice(ctx, cash, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: 99999, Qty: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "بدون شيفت مفتوح") {
		t.Fatalf("no-shift + bad product: want shift error, got %v", err)
	}
	// With a shift open, the same bad product reports the product error.
	testOpenShift(t, s, cash, money.Cents(10000))
	_, err = s.CreateInvoice(ctx, cash, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: 99999, Qty: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "المنتج غير موجود") {
		t.Fatalf("open-shift + bad product: want product error, got %v", err)
	}
}

func TestFix3Lane1_UnreadAuditResolvedExcluded(t *testing.T) {
	// L13: the unread count is IsRead=0 AND IsResolved=0 — an unread
	// but resolved alert does not count.
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()
	seedAuditData(t, s)
	if n, _ := s.UnreadAuditAlertsCount(ctx, admin); n != 1 {
		t.Fatalf("seeded unread = %d, want 1", n)
	}
	_, err := s.DB.Exec(`INSERT INTO AuditAlerts(LogID,UserName,AlertType,AlertMessage,AlertValue,Threshold,IsRead,IsResolved,AlertDate)
		VALUES(2,'admin','cancel_amount','ملغاة لكن غير مقروءة',20000,10000,0,1,'2026-11-25 11:00:00')`)
	if err != nil {
		t.Fatalf("seed resolved alert: %v", err)
	}
	if n, _ := s.UnreadAuditAlertsCount(ctx, admin); n != 1 {
		t.Fatalf("unread+resolved must not count: got %d, want 1", n)
	}
}

func TestFix3Lane1_ShiftStatusNoneWhenNoShift(t *testing.T) {
	// L17 control: a user with no shifts at all still reports "none".
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	st, err := s.GetShiftStatus(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "none" || st.ShiftID != 0 {
		t.Fatalf("no-shift status = %+v, want state none", st)
	}
}
