package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

func TestPartialReturn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000) // 10 units in stock
	// Simulate the sale having taken 2 units out of stock.
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=8000 WHERE ProductID=?`, pid); err != nil {
		t.Fatal(err)
	}
	invID := testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("partial return: %v", err)
	}
	if res.TotalRefund != 2000 || res.CashRefund != 2000 || res.DebtReduction != 0 {
		t.Fatalf("refund wrong: %+v", res)
	}
	if len(res.ReturnIDs) != 1 {
		t.Fatalf("expected 1 return row, got %+v", res)
	}
	// Stock restocked by 1 unit: 8000 + 1000 = 9000.
	if q := productQty(t, s, pid); q != 9000 {
		t.Fatalf("stock expected 9000, got %d", q)
	}
	// Drawer: 10000 + 4000 cash sale − 2000 cash return = 12000.
	sum, _ := s.ShiftSummary(ctx, id)
	if sum.ExpectedBalance != 12000 || sum.CashReturns != 2000 || sum.ReturnsTotal != 2000 {
		t.Fatalf("summary after return wrong: %+v", sum)
	}
	// StockMovements 'return' row exists.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE ProductID=? AND MovementType='return'`, pid).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 return movement, got %d", n)
	}
}

func TestFullReturn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=8000 WHERE ProductID=?`, pid); err != nil {
		t.Fatal(err)
	}
	invID := testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	res, err := s.ProcessReturnAll(ctx, u, invID, "إرجاع كامل", "Cash")
	if err != nil {
		t.Fatalf("full return: %v", err)
	}
	if res.TotalRefund != 4000 {
		t.Fatalf("full refund expected 4000, got %d", res.TotalRefund)
	}
	if q := productQty(t, s, pid); q != 10000 {
		t.Fatalf("stock expected 10000 after full return, got %d", q)
	}
	// Nothing left to return.
	if _, err := s.ProcessReturnAll(ctx, u, invID, "مرة أخرى", "Cash"); err == nil {
		t.Fatal("expected a second full return to be rejected (nothing available)")
	}
}

func TestReturnExceedingAvailableRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	invID := testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	// Sold 2 units; returning 3 must fail.
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 3000}}, "كثير", "Cash"); err == nil {
		t.Fatal("expected return exceeding sold-minus-returned to be rejected")
	}
	// Return 1, then returning 2 more must fail (only 1 left).
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "واحد", "Cash"); err != nil {
		t.Fatalf("first return: %v", err)
	}
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 2000}}, "اثنان", "Cash"); err == nil {
		t.Fatal("expected second return exceeding the remaining available qty to be rejected")
	}
}

func TestReturnValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	id, _ := s.OpenShift(ctx, u, money.Cents(10000), false)
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	invID := testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	// Missing invoice.
	if _, err := s.ProcessReturn(ctx, u, 99999, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "x", "Cash"); err == nil {
		t.Fatal("expected missing invoice to be rejected")
	}
	// Missing reason.
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "", "Cash"); err == nil {
		t.Fatal("expected missing reason to be rejected")
	}
	// Zero qty.
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 0}}, "x", "Cash"); err == nil {
		t.Fatal("expected zero qty to be rejected")
	}
	// Canceled invoice.
	if _, err := s.DB.Exec(`UPDATE Invoices SET IsCanceled=1, CanceledAt=? WHERE InvoiceID=?`, Now(), invID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "x", "Cash"); err == nil {
		t.Fatal("expected return on a canceled invoice to be rejected")
	}
}

func TestReturnDebtFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	cust := testCustomer(t, s, "عميل آجل")
	// Credit sale: net 4000, paid 1000 cash, remaining 3000.
	invID := testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 1000, 3000, "Cash", cust)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	// Refund 2000 is absorbed by the 3000 debt first: no cash out.
	if res.TotalRefund != 2000 || res.DebtReduction != 2000 || res.CashRefund != 0 {
		t.Fatalf("debt-first wrong: %+v", res)
	}
	var remaining int64
	_ = s.DB.QueryRow(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, invID).Scan(&remaining)
	if remaining != 1000 {
		t.Fatalf("remaining expected 1000, got %d", remaining)
	}
	var bal int64
	_ = s.DB.QueryRow(`SELECT Balance FROM Customers WHERE CustomerID=?`, cust).Scan(&bal)
	if bal != 1000 {
		t.Fatalf("customer balance expected 1000, got %d", bal)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE ShiftID=? AND MovementType='out'`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("debt-absorbed return must write no CashBox out, got %d", n)
	}
}

func TestReturnBxgyVoidsFree(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup1", "supervisor")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	// Sale of 4 paid units; stock after sale: 10 − 4 paid − 2 free = 4 units.
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=4000 WHERE ProductID=?`, pid); err != nil {
		t.Fatal(err)
	}
	invID := testSaleInvoice(t, s, u, id, 1, pid, 4000, 2000, 1000, 8000, 0, "Cash", 1)
	// Mark the 2 earned free units on the detail line (as at sale).
	if _, err := s.DB.Exec(`UPDATE InvoiceDetails SET FreeQty=2000 WHERE InvoiceID=?`, invID); err != nil {
		t.Fatal(err)
	}
	// bxgy offer: buy 2 get 1 free on this invoice+product. (FIX-1 L7:
	// the FK requires a real parent offer row, not a literal ID.)
	bxgy := b1CreateOffer(t, s, OfferInput{Name: "اشتر 2 واحصل على 1", Type: OfferBuyXGetY,
		BuyX: 2, FreeY: 1, IsActive: true,
		Targets: []OfferTarget{{TargetType: "product", TargetID: pid}}})
	if _, err := s.DB.Exec(`INSERT INTO InvoiceOffers(InvoiceID,OfferID,OfferName,SavedAmount,ProductID,BuyQty,FreeQty)
		VALUES(?,?, 'اشتر 2 واحصل على 1', 0, ?, 2000, 1000)`, invID, bxgy.ID, pid); err != nil {
		t.Fatal(err)
	}
	// Return 2 paid units: kept 4→2, earned free 2→1, so 1 free unit is voided.
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 2000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("bxgy return: %v", err)
	}
	if len(res.VoidedFree) != 1 {
		t.Fatalf("expected 1 voided-free line, got %+v", res.VoidedFree)
	}
	vf := res.VoidedFree[0]
	if vf.Qty != 1000 {
		t.Fatalf("voided free qty expected 1000 milli (1 unit), got %d", vf.Qty)
	}
	// Refund is the PAID units only: 2 × 2000 = 4000.
	if res.TotalRefund != 4000 {
		t.Fatalf("refund expected 4000 (paid only), got %d", res.TotalRefund)
	}
	// Voided free cost persisted on the first Returns row of the product:
	// 1 unit × buy 1000 = 1000.
	var vq, vc int64
	_ = s.DB.QueryRow(`SELECT VoidedFreeQty,VoidedFreeCost FROM Returns WHERE InvoiceID=? AND ProductID=?`,
		invID, pid).Scan(&vq, &vc)
	if vq != 1000 || vc != 1000 {
		t.Fatalf("persisted voided free wrong: qty=%d cost=%d", vq, vc)
	}
	// Stock: 4000 + 2000 returned paid + 1000 voided free = 7000.
	if q := productQty(t, s, pid); q != 7000 {
		t.Fatalf("stock expected 7000, got %d", q)
	}
	// The voided free units have their own StockMovements row.
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM StockMovements WHERE ProductID=? AND Notes LIKE 'وحدات مجانية مستردة%'`, pid).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 voided-free movement, got %d", n)
	}
}

func TestReturnAnalysis(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	u := b2TestUser(t, s, "sup1", "supervisor")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	invID := testSaleInvoice(t, s, u, id, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	if _, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash"); err != nil {
		t.Fatalf("return: %v", err)
	}
	// Cashier may not read the analysis.
	if _, err := s.GetReturnsAnalytics(ctx, u, "", ""); err == nil {
		t.Fatal("expected cashier to be denied return analysis")
	}
	rep, err := s.GetReturnsAnalytics(ctx, admin, "", "")
	if err != nil {
		t.Fatalf("analysis: %v", err)
	}
	// 1 return, value 2000, lost profit = 2000 − 1×1000 = 1000.
	if rep.Count != 1 || rep.TotalValue != 2000 || rep.LostProfit != 1000 {
		t.Fatalf("analysis wrong: %+v", rep)
	}
	if len(rep.ByProduct) != 1 || len(rep.ByUser) != 1 {
		t.Fatalf("breakdowns wrong: %+v", rep)
	}
	csv, err := s.ReturnAnalysisCSV(ctx, admin, "", "")
	if err != nil || csv == "" {
		t.Fatalf("csv: %v %q", err, csv)
	}
}

// --- FIX-1 regression tests ---

func TestFix1_ReturnDuplicateLinesMerged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sup := b2TestUser(t, s, "sup1", "supervisor")
	shiftID := testOpenShift(t, s, sup, 10000)
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	invID := testSaleInvoice(t, s, sup, shiftID, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	// Two rows for the same product totalling 4000 > sold 2000: pre-fix
	// each row passed the DB check on its own (M3).
	if _, err := s.ProcessReturn(ctx, sup, invID,
		[]ReturnLineInput{{ProductID: pid, Qty: 2000}, {ProductID: pid, Qty: 2000}}, "تالف", "Cash"); err == nil {
		t.Fatal("duplicate return lines totalling more than sold should be refused (M3)")
	}
	// Two rows totalling exactly the sold qty merge into one return row.
	res, err := s.ProcessReturn(ctx, sup, invID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}, {ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("merged return: %v", err)
	}
	if len(res.ReturnIDs) != 1 {
		t.Fatalf("merged return rows = %d, want 1", len(res.ReturnIDs))
	}
	if res.TotalRefund != 4000 {
		t.Fatalf("refund = %d, want 4000", res.TotalRefund)
	}
}

// TestFix1_ReturnCashNoShiftRefused (FIX-1 H6a, amended by FIX-12 SW-M05):
// a cash refund is attributed to the invoice's own shift when it is still
// open — the drawer that actually holds the sale's cash — so a returner
// with NO open shift of their own can still refund a cash sale from an
// open invoice shift. Only when NO shift is open anywhere (neither the
// invoice's nor the returner's) is the cash refund refused, because cash
// must never leave unrecorded.
func TestFix1_ReturnCashNoShiftRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	shiftID := testOpenShift(t, s, admin, 10000)
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	invID := testSaleInvoice(t, s, admin, shiftID, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	// A supervisor with NO open shift: the refund lands in the invoice's
	// (admin's) still-open shift.
	sup := b2TestUser(t, s, "sup2", "supervisor")
	res, err := s.ProcessReturn(ctx, sup, invID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("cash return on an open invoice shift should succeed: %v", err)
	}
	if res.CashRefund != 2000 {
		t.Fatalf("cash refund = %d, want 2000", res.CashRefund)
	}
	var legShift int64
	if err := s.DB.QueryRow(`SELECT ShiftID FROM CashBox WHERE RefInvoiceID=? AND AutoKind='return'`, invID).Scan(&legShift); err != nil {
		t.Fatalf("return leg: %v", err)
	}
	if legShift != shiftID {
		t.Fatalf("return leg shift = %d, want invoice shift %d", legShift, shiftID)
	}
	// Close the invoice's shift too: now no shift is open anywhere, so
	// the next cash refund must be refused and leave no Returns rows.
	if _, err := s.CloseShift(ctx, admin, shiftID); err != nil {
		t.Fatalf("close shift: %v", err)
	}
	if _, err := s.ProcessReturn(ctx, sup, invID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash"); err == nil {
		t.Fatal("cash return with no open shift anywhere should be refused (H6a)")
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID).Scan(&n)
	if n != 1 {
		t.Fatalf("refused return left %d Returns rows, want 1 (only the first return)", n)
	}
}

func TestFix1_ReturnDebtAbsorbedNeedsNoShift(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	shiftID := testOpenShift(t, s, admin, 10000)
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	cust := testCustomer(t, s, "عميل آجل")
	// Fully unpaid credit invoice: the whole refund is absorbed by debt.
	invID := testSaleInvoice(t, s, admin, shiftID, 1, pid, 2000, 2000, 1000, 0, 4000, "Cash", cust)
	sup := b2TestUser(t, s, "sup3", "supervisor")
	res, err := s.ProcessReturn(ctx, sup, invID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("debt-absorbed return with no shift should be allowed: %v", err)
	}
	if res.CashRefund != 0 || res.DebtReduction != 2000 {
		t.Fatalf("refund cash=%d debt=%d, want 0/2000", res.CashRefund, res.DebtReduction)
	}
}

func TestFix1_ReturnCashDrawerGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sup := b2TestUser(t, s, "sup1", "supervisor")
	shiftID := testOpenShift(t, s, sup, 1000) // drawer holds only 1000
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	// Cash-paid invoice (inserted directly: no CashBox row, so the
	// drawer really holds only the 1000 opening).
	invID := testSaleInvoice(t, s, sup, shiftID, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	if _, err := s.ProcessReturn(ctx, sup, invID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash"); err == nil {
		t.Fatal("cash refund 2000 against a 1000 drawer should be refused (H6b)")
	}
}

// --- FIX-2 regression tests ---

func TestFix2_ListReturnsNeedsDoReturn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix2admin", "admin")
	cashier := b2TestUser(t, s, "fix2cash", "cashier")
	// M-4: the list is no longer open to every logged-in role — it
	// requires do_return, like the /returns page and /api/returns.
	if _, err := s.ListReturns(ctx, admin, "", ""); err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if _, err := s.ListReturns(ctx, cashier, "", ""); err == nil {
		t.Fatal("cashier must not list returns without do_return")
	}
}

// --- FIX-18 (R18-A-M01 / R18-A-L02) ---

// fix18Detail is one stored InvoiceDetails row for fix18SaleInvoice.
type fix18Detail struct {
	productID  int64
	qty        money.Qty
	unitPrice  money.Cents
	buyPrice   money.Cents
	itemDisc   money.Cents
	promoDisc  money.Cents
	totalPrice money.Cents
}

// fix18SaleInvoice inserts a sale invoice with the given detail lines
// and the invoice-level stored columns, returning the invoice id.
// totalPrice is the stored InvoiceDetails.TotalPrice (already net of
// the item discount, gross of the line promo); invDiscount is the
// stored Invoices.Discount (= total item discounts + invoice-level
// discount); invPromo is the stored Invoices.PromoDiscount.
func fix18SaleInvoice(t *testing.T, s *Store, user *auth.User, shiftID, number int64, details []fix18Detail, invDiscount, invPromo, net, paid, remaining money.Cents, method string, customerID int64) int64 {
	t.Helper()
	var gross int64
	for _, d := range details {
		gross += int64(mustMulQty(d.unitPrice, d.qty))
	}
	var prodName string
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
		VALUES(?,?,'retail',?,?,?,?,?,?,?,?,?,?,?,0)`,
		number, customerID, gross, int64(invDiscount), int64(invPromo), int64(net), int64(paid),
		int64(remaining), method, user.ID, user.UserName, shiftID, Now())
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}
	invID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("invoice id: %v", err)
	}
	for _, d := range details {
		if err := s.DB.QueryRow(`SELECT ProductName FROM Products WHERE ProductID=?`, d.productID).Scan(&prodName); err != nil {
			t.Fatalf("product name: %v", err)
		}
		if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,ItemDiscount,PromoDiscount,TotalPrice)
			VALUES(?,?,?,?,?,?,?,?,?)`, invID, d.productID, prodName, int64(d.qty), int64(d.unitPrice), int64(d.buyPrice),
			int64(d.itemDisc), int64(d.promoDisc), int64(d.totalPrice)); err != nil {
			t.Fatalf("insert detail: %v", err)
		}
	}
	if paid > 0 {
		if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
			VALUES(?,?,?,?,?,'دفعة عند البيع',?)`, invID, customerID, int64(paid), Now(), method, user.ID); err != nil {
			t.Fatalf("insert payment: %v", err)
		}
	}
	return invID
}

// TestFix18_ReturnPartialMultiLinePromoInvoiceDiscount (R18-A-M01):
// partial returns of a multi-line invoice with a line promo + invoice
// discount must allocate the invoice discount on the post-line-promo
// bases — the same bases the sale used (domain_sales.go:540-548) —
// not on TotalPrice (gross of the line promo). Expected refunds are
// hand-computed from the sale-side allocation (AllocProRata on the
// bases; the rounding remainder goes to the last positive-base line):
//   (a) tea 2x800 promo 160 + sugar 1x1500 item-disc 500, inv disc
//       200, net 2240: bases 1440/1000, shares 118/82 -> tea net
//       1322 (661/unit), sugar net 918.
//   (a3) tea 1x800 promo 400 (50%) + sugar 1x1500, inv disc 500,
//       net 1400: bases 400/1500, shares 105/395 -> 295 / 1105.
//   (a2) tea 2x800 promo 800 (50%) + sugar 1x1500 item-disc 500,
//       inv disc 500, net 1300: bases 800/1000, shares 222/278 ->
//       tea net 578 (289/unit).
// On pre-fix code the refunds are 658/924, 227/1174, 246 — this test
// FAILS on pre-fix code and PASSES post-fix.
func TestFix18_ReturnPartialMultiLinePromoInvoiceDiscount(t *testing.T) {
	newEnv := func(t *testing.T) (*Store, *auth.User, int64, int64, int64) {
		t.Helper()
		s := testStore(t)
		ctx := context.Background()
		u := b2TestUser(t, s, "sup18", "supervisor")
		shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		tea := testProductB2(t, s, "شاي", 500, 800, 10000)
		sugar := testProductB2(t, s, "سكر", 1000, 1500, 10000)
		return s, u, shiftID, tea, sugar
	}
	teaLine := func(tea int64, qty money.Qty, promo money.Cents) fix18Detail {
		return fix18Detail{productID: tea, qty: qty, unitPrice: 800, buyPrice: 500,
			itemDisc: 0, promoDisc: promo, totalPrice: mustMulQty(800, qty)}
	}
	sugarLine := func(sugar int64) fix18Detail {
		return fix18Detail{productID: sugar, qty: 1000, unitPrice: 1500, buyPrice: 1000,
			itemDisc: 500, promoDisc: 0, totalPrice: 1000}
	}
	returnOne := func(t *testing.T, s *Store, u *auth.User, invID, pid int64, qty money.Qty) money.Cents {
		t.Helper()
		res, err := s.ProcessReturn(context.Background(), u, invID,
			[]ReturnLineInput{{ProductID: pid, Qty: qty}}, "تالف", "Cash")
		if err != nil {
			t.Fatalf("return: %v", err)
		}
		return res.TotalRefund
	}

	t.Run("TwoLines10PctPromo", func(t *testing.T) {
		s, u, shiftID, tea, sugar := newEnv(t)
		// Return 1 tea from the (a) invoice: want 661.
		invID := fix18SaleInvoice(t, s, u, shiftID, 1,
			[]fix18Detail{teaLine(tea, 2000, 160), sugarLine(sugar)},
			700, 160, 2240, 2240, 0, "Cash", 1)
		if got := returnOne(t, s, u, invID, tea, 1000); got != 661 {
			t.Fatalf("return 1 tea = %d, want 661", got)
		}
		// Return the sugar from a fresh identical invoice: want 918.
		invID2 := fix18SaleInvoice(t, s, u, shiftID, 2,
			[]fix18Detail{teaLine(tea, 2000, 160), sugarLine(sugar)},
			700, 160, 2240, 2240, 0, "Cash", 1)
		if got := returnOne(t, s, u, invID2, sugar, 1000); got != 918 {
			t.Fatalf("return sugar = %d, want 918", got)
		}
		// Return-all on a fresh identical invoice stays exact: 2240.
		invID3 := fix18SaleInvoice(t, s, u, shiftID, 3,
			[]fix18Detail{teaLine(tea, 2000, 160), sugarLine(sugar)},
			700, 160, 2240, 2240, 0, "Cash", 1)
		res, err := s.ProcessReturnAll(context.Background(), u, invID3, "إرجاع كامل", "Cash")
		if err != nil {
			t.Fatalf("return-all: %v", err)
		}
		if res.TotalRefund != 2240 {
			t.Fatalf("return-all = %d, want 2240", res.TotalRefund)
		}
	})

	t.Run("SingleUnitLines50PctPromo", func(t *testing.T) {
		s, u, shiftID, tea, sugar := newEnv(t)
		// (a3): the sugar line here is PLAIN (no item discount):
		// gross 2300 - promo 400 - inv disc 500 = net 1400, and the
		// stored Discount is the invoice discount alone (500).
		plainSugar := fix18Detail{productID: sugar, qty: 1000, unitPrice: 1500, buyPrice: 1000,
			itemDisc: 0, promoDisc: 0, totalPrice: 1500}
		// (a3): return the tea: want 295.
		invID := fix18SaleInvoice(t, s, u, shiftID, 1,
			[]fix18Detail{teaLine(tea, 1000, 400), plainSugar},
			500, 400, 1400, 1400, 0, "Cash", 1)
		if got := returnOne(t, s, u, invID, tea, 1000); got != 295 {
			t.Fatalf("return tea = %d, want 295", got)
		}
		// Return the sugar from a fresh identical invoice: want 1105.
		invID2 := fix18SaleInvoice(t, s, u, shiftID, 2,
			[]fix18Detail{teaLine(tea, 1000, 400), plainSugar},
			500, 400, 1400, 1400, 0, "Cash", 1)
		if got := returnOne(t, s, u, invID2, sugar, 1000); got != 1105 {
			t.Fatalf("return sugar = %d, want 1105", got)
		}
		// Sequential returns on one invoice sum to the net exactly.
		invID3 := fix18SaleInvoice(t, s, u, shiftID, 3,
			[]fix18Detail{teaLine(tea, 1000, 400), plainSugar},
			500, 400, 1400, 1400, 0, "Cash", 1)
		r1 := returnOne(t, s, u, invID3, tea, 1000)
		r2 := returnOne(t, s, u, invID3, sugar, 1000)
		if r1+r2 != 1400 {
			t.Fatalf("sequential returns = %d + %d = %d, want 1400", r1, r2, r1+r2)
		}
	})

	t.Run("HighPromoVariant", func(t *testing.T) {
		s, u, shiftID, tea, sugar := newEnv(t)
		// (a2): 50% tea promo, inv disc 500, net 1300: return 1 tea: want 289.
		invID := fix18SaleInvoice(t, s, u, shiftID, 1,
			[]fix18Detail{teaLine(tea, 2000, 800), sugarLine(sugar)},
			1000, 800, 1300, 1300, 0, "Cash", 1)
		if got := returnOne(t, s, u, invID, tea, 1000); got != 289 {
			t.Fatalf("return 1 tea = %d, want 289", got)
		}
	})
}

// TestFix18_ReturnNetZeroInvoiceDistinctMessage (R18-A-L02): returning
// a net-0 invoice is refused, but with a distinct accurate message
// ("لا يوجد مبلغ قابل للاسترداد") — not "لا توجد كمية متاحة
// للمرتجع", which misstates the reason (the quantity IS available;
// the refund is zero). A genuinely out-of-quantity return still gets
// its own message, and a fully-returned invoice still gets the old
// "no quantity" message from ProcessReturnAll.
func TestFix18_ReturnNetZeroInvoiceDistinctMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup18", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(100000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tea := testProductB2(t, s, "شاي", 500, 800, 10000)
	// Net-0 invoice: 1x tea @800, 50% promo (400) + invoice discount
	// 400 -> net 0, paid 0.
	invID := fix18SaleInvoice(t, s, u, shiftID, 1,
		[]fix18Detail{{productID: tea, qty: 1000, unitPrice: 800, buyPrice: 500,
			itemDisc: 0, promoDisc: 400, totalPrice: 800}},
		400, 400, 0, 0, 0, "Cash", 1)
	_, err = s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: tea, Qty: 1000}}, "تالف", "Cash")
	if err == nil {
		t.Fatal("net-0 return should be refused")
	}
	if !strings.Contains(err.Error(), "لا يوجد مبلغ قابل للاسترداد") {
		t.Fatalf("net-0 error = %q, want the zero-refund message", err.Error())
	}
	if strings.Contains(err.Error(), "لا توجد كمية متاحة للمرتجع") {
		t.Fatalf("net-0 error still uses the misleading quantity message: %q", err.Error())
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID).Scan(&n)
	if n != 0 {
		t.Fatalf("refused net-0 return left %d Returns rows, want 0", n)
	}
	// Genuinely out-of-quantity: returning more than sold on a normal
	// invoice keeps its own distinct message.
	sugar := testProductB2(t, s, "سكر", 1000, 1500, 10000)
	invID2 := fix18SaleInvoice(t, s, u, shiftID, 2,
		[]fix18Detail{{productID: sugar, qty: 1000, unitPrice: 1500, buyPrice: 1000,
			itemDisc: 0, promoDisc: 0, totalPrice: 1500}},
		0, 0, 1500, 1500, 0, "Cash", 1)
	_, err = s.ProcessReturn(ctx, u, invID2, []ReturnLineInput{{ProductID: sugar, Qty: 2000}}, "تالف", "Cash")
	if err == nil || !strings.Contains(err.Error(), "لا يمكن إرجاع كمية أكبر من") {
		t.Fatalf("over-qty error = %v, want the quantity-exceeded message", err)
	}
	// Fully-returned invoice: ProcessReturnAll still reports no
	// quantity available (the old message, still accurate here).
	if _, err := s.ProcessReturn(ctx, u, invID2, []ReturnLineInput{{ProductID: sugar, Qty: 1000}}, "تالف", "Cash"); err != nil {
		t.Fatalf("full return: %v", err)
	}
	_, err = s.ProcessReturnAll(ctx, u, invID2, "مرة أخرى", "Cash")
	if err == nil || !strings.Contains(err.Error(), "لا توجد كمية متاحة للمرتجع") {
		t.Fatalf("second return-all error = %v, want the no-quantity message", err)
	}
}
