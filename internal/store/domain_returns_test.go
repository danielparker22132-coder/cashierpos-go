package store

import (
	"context"
	"testing"

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

func TestFix1_ReturnCashNoShiftRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "admin1", "admin")
	shiftID := testOpenShift(t, s, admin, 10000)
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	invID := testSaleInvoice(t, s, admin, shiftID, 1, pid, 2000, 2000, 1000, 4000, 0, "Cash", 1)
	// A supervisor with NO open shift: the cash leg cannot be recorded.
	sup := b2TestUser(t, s, "sup2", "supervisor")
	if _, err := s.ProcessReturn(ctx, sup, invID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash"); err == nil {
		t.Fatal("cash return with no open shift should be refused (H6a)")
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM Returns WHERE InvoiceID=?`, invID).Scan(&n)
	if n != 0 {
		t.Fatalf("refused return left %d Returns rows", n)
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
