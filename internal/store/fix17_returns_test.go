package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// FIX-17 SW2-H01 / SW2-L02 (lane A) regression tests.
//
// SW2-H01: returns under-refunded any sale with a line-level discount —
// the discount was subtracted twice (once in InvoiceDetails.TotalPrice
// / as d.PromoDiscount, and again inside the pro-rata alloc, because
// invDiscount was Discount + PromoDiscount while Discount already
// includes item discounts). The fix allocates the invoice-level
// discount ONLY (Discount − Σ(ItemDiscount)).
// Expected refunds below are the lines' full net paid amounts; on
// pre-fix code the promo test refunds 400 (want 500), the item-discount
// test 500 (want 1000), the mixed test 590 (want 670), the debt-first
// test 500 (want 1000) and the return-all test 2000 (want 2500).
// The invoice-discount-only control refunds 1250 on both versions.
//
// SW2-L02 (return half): a return audit entry now carries per-line
// detail rows (mirroring the sale path), verified in
// TestFix17_ReturnPromoOnlyRefundsFullNet via GetAuditLogDetails.

// fix17SaleInvoice inserts a sale invoice + one detail line + the
// at-sale Payments row, with explicit discount columns, returning the
// invoice id. totalPrice is the stored InvoiceDetails.TotalPrice
// (already net of the item discount); invDiscount is the stored
// Invoices.Discount (= total item discounts + invoice-level discount);
// invPromo is the stored Invoices.PromoDiscount.
func fix17SaleInvoice(t *testing.T, s *Store, user *auth.User, shiftID int64, number int64, productID int64,
	qty money.Qty, unitPrice, buyPrice money.Cents, itemDisc, promoDisc money.Cents, totalPrice money.Cents,
	invDiscount, invPromo money.Cents, net, paid, remaining money.Cents, method string, customerID int64) int64 {
	t.Helper()
	gross := mustMulQty(unitPrice, qty)
	var prodName string
	if err := s.DB.QueryRow(`SELECT ProductName FROM Products WHERE ProductID=?`, productID).Scan(&prodName); err != nil {
		t.Fatalf("product name: %v", err)
	}
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
		VALUES(?,?,'retail',?,?,?,?,?,?,?,?,?,?,?,0)`,
		number, customerID, int64(gross), int64(invDiscount), int64(invPromo), int64(net), int64(paid),
		int64(remaining), method, user.ID, user.UserName, shiftID, Now())
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}
	invID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("invoice id: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,ItemDiscount,PromoDiscount,TotalPrice)
		VALUES(?,?,?,?,?,?,?,?,?)`, invID, productID, prodName, int64(qty), int64(unitPrice), int64(buyPrice),
		int64(itemDisc), int64(promoDisc), int64(totalPrice)); err != nil {
		t.Fatalf("insert detail: %v", err)
	}
	if paid > 0 {
		if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
			VALUES(?,?,?,?,?,'دفعة عند البيع',?)`, invID, customerID, int64(paid), Now(), method, user.ID); err != nil {
			t.Fatalf("insert payment: %v", err)
		}
	}
	return invID
}

func TestFix17_ReturnPromoOnlyRefundsFullNet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup17", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Soap, promo product_fixed 100/unit: 1× gross 600, net 500, paid 500.
	pid := testProductB2(t, s, "صابون", 300, 600, 10000)
	// Simulate the sale having taken 1 unit out of stock.
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=9000 WHERE ProductID=?`, pid); err != nil {
		t.Fatal(err)
	}
	invID := fix17SaleInvoice(t, s, u, shiftID, 1, pid, 1000, 600, 300, 0, 100, 600, 0, 100, 500, 500, 0, "Cash", 1)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.TotalRefund != 500 || res.CashRefund != 500 || res.DebtReduction != 0 {
		t.Fatalf("promo-only refund wrong: %+v (want TotalRefund 500)", res)
	}
	if q := productQty(t, s, pid); q != 10000 {
		t.Fatalf("stock expected 10000, got %d", q)
	}
	// SW2-L02 (return half): the return audit entry carries a detail line.
	var logID int64
	if err := s.DB.QueryRow(`SELECT LogID FROM AuditLog WHERE ActionType='مرتجع' AND InvoiceID=? ORDER BY LogID DESC LIMIT 1`, invID).Scan(&logID); err != nil {
		t.Fatalf("return audit log: %v", err)
	}
	admin := b2TestUser(t, s, "admin17", "admin")
	_, details, err := s.GetAuditLogDetails(ctx, admin, logID)
	if err != nil {
		t.Fatalf("audit details: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("audit detail lines = %d, want 1", len(details))
	}
	d := details[0]
	if d.ProductID != pid || d.ProductName != "صابون" {
		t.Fatalf("audit detail product wrong: %+v", d)
	}
	if d.QuantityBefore != 9000 || d.QuantityAfter != 10000 || d.QuantityChange != 1000 {
		t.Fatalf("audit detail qty wrong: before=%d after=%d change=%d (want 9000/10000/1000)",
			d.QuantityBefore, d.QuantityAfter, d.QuantityChange)
	}
	if d.PriceBefore != 600 || d.PriceAfter != 500 || d.LineTotal != 500 {
		t.Fatalf("audit detail price wrong: before=%d after=%d total=%d (want 600/500/500)",
			d.PriceBefore, d.PriceAfter, d.LineTotal)
	}
}

func TestFix17_ReturnItemDiscountOnlyRefundsFullNet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup17", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Sugar, per-item discount 500: 1× gross 1500, net 1000, paid 1000.
	// Stored Discount (500) is the item discount itself; there is no
	// invoice-level discount, so the allocation must be zero.
	pid := testProductB2(t, s, "سكر", 1000, 1500, 10000)
	invID := fix17SaleInvoice(t, s, u, shiftID, 1, pid, 1000, 1500, 1000, 500, 0, 1000, 500, 0, 1000, 1000, 0, "Cash", 1)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.TotalRefund != 1000 || res.CashRefund != 1000 || res.DebtReduction != 0 {
		t.Fatalf("item-discount-only refund wrong: %+v (want TotalRefund 1000)", res)
	}
}

func TestFix17_ReturnInvoiceDiscountOnlyControl(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup17", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Control: invoice-discount-only sale, 2× sugar gross 3000, invoice
	// discount 500, net 2500. Returning 1 unit refunds 1250 — this was
	// already correct pre-fix and must stay correct.
	pid := testProductB2(t, s, "سكر", 1000, 1500, 10000)
	invID := fix17SaleInvoice(t, s, u, shiftID, 1, pid, 2000, 1500, 1000, 0, 0, 3000, 500, 0, 2500, 2500, 0, "Cash", 1)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.TotalRefund != 1250 || res.CashRefund != 1250 {
		t.Fatalf("invoice-discount-only refund wrong: %+v (want TotalRefund 1250)", res)
	}
}

func TestFix17_ReturnMixedPromoAndInvoiceDiscount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup17", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Tea, promo 10% + invoice discount 100: 2× gross 1600, promo 160,
	// net 1340, paid 1340. Returning 1 unit refunds 670.
	pid := testProductB2(t, s, "شاي", 500, 800, 10000)
	invID := fix17SaleInvoice(t, s, u, shiftID, 1, pid, 2000, 800, 500, 0, 160, 1600, 100, 160, 1340, 1340, 0, "Cash", 1)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.TotalRefund != 670 || res.CashRefund != 670 {
		t.Fatalf("mixed promo+invoice refund wrong: %+v (want TotalRefund 670)", res)
	}
}

func TestFix17_ReturnDiscountedDebtFirstReducesFullNet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup17", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Credit sale with an item discount: sugar net 1000, paid 0,
	// remaining 1000. The full net must come off the debt.
	pid := testProductB2(t, s, "سكر", 1000, 1500, 10000)
	cust := testCustomer(t, s, "عميل آجل")
	invID := fix17SaleInvoice(t, s, u, shiftID, 1, pid, 1000, 1500, 1000, 500, 0, 1000, 500, 0, 1000, 0, 1000, "Cash", cust)
	res, err := s.ProcessReturn(ctx, u, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.TotalRefund != 1000 || res.DebtReduction != 1000 || res.CashRefund != 0 {
		t.Fatalf("debt-first discounted refund wrong: %+v (want 1000/1000/0)", res)
	}
	var remaining int64
	_ = s.DB.QueryRow(`SELECT RemainingAmount FROM Invoices WHERE InvoiceID=?`, invID).Scan(&remaining)
	if remaining != 0 {
		t.Fatalf("remaining expected 0, got %d", remaining)
	}
	var bal int64
	_ = s.DB.QueryRow(`SELECT Balance FROM Customers WHERE CustomerID=?`, cust).Scan(&bal)
	if bal != 0 {
		t.Fatalf("customer balance expected 0, got %d", bal)
	}
}

func TestFix17_ReturnAllDiscountedRefundsFullNet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "sup17", "supervisor")
	shiftID, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Return-all delegates to ProcessReturn, so it inherits the fix:
	// 2× sugar, item discount 500, net 2500, paid 2500 → refund 2500.
	pid := testProductB2(t, s, "سكر", 1000, 1500, 10000)
	invID := fix17SaleInvoice(t, s, u, shiftID, 1, pid, 2000, 1500, 1000, 500, 0, 2500, 500, 0, 2500, 2500, 0, "Cash", 1)
	res, err := s.ProcessReturnAll(ctx, u, invID, "إرجاع كامل", "Cash")
	if err != nil {
		t.Fatalf("return-all: %v", err)
	}
	if res.TotalRefund != 2500 || res.CashRefund != 2500 {
		t.Fatalf("return-all discounted refund wrong: %+v (want TotalRefund 2500)", res)
	}
}
