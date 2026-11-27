package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-20 R20-A-F1: PaidAmount fallback must survive a return ---
//
// A legacy / directly-inserted invoice has PaidAmount > 0 and zero
// Payments rows, so the shift summary and DailyClose count its
// PaidAmount via the fallback. Before the fix, the return's own
// negative Payments row marked the invoice as "has payment", killing
// the fallback while the return's CashBox out leg still subtracted —
// CashSales lost the whole PaidAmount (double subtraction).

func TestFix20_LegacyFallbackSurvivesReturn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(5000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 500, 1000, 100000)
	cust := testCustomer(t, s, "عميل قديم")
	// Legacy invoice: PaidAmount 1000, NO Payments rows at all.
	res, err := s.DB.Exec(`INSERT INTO Invoices(InvoiceNumber,CustomerID,SaleType,TotalAmount,Discount,
		PromoDiscount,NetAmount,PaidAmount,RemainingAmount,PaymentMethod,UserID,UserName,ShiftID,InvoiceDate,IsCanceled)
		VALUES(900001,?,'retail',1000,0,0,1000,1000,0,'Cash',?,?,?,?,0)`,
		cust, mgr.ID, mgr.UserName, shiftID, Now())
	if err != nil {
		t.Fatalf("insert legacy invoice: %v", err)
	}
	legacyID, _ := res.LastInsertId()
	if _, err := s.DB.Exec(`INSERT INTO InvoiceDetails(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalPrice)
		VALUES(?,?,?,?,?,?,?)`, legacyID, pid, "أرز", 1000, 1000, 500, 1000); err != nil {
		t.Fatalf("insert legacy detail: %v", err)
	}

	// Before the return the fallback counts the legacy sale.
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.CashSales != 1000 || sum.ExpectedBalance != 6000 {
		t.Fatalf("pre-return summary: CashSales=%v Expected=%v, want 1000/6000", sum.CashSales, sum.ExpectedBalance)
	}
	dc, err := s.DailyClose(ctx, admin, Today())
	if err != nil {
		t.Fatalf("daily close: %v", err)
	}
	if dc.TotalCash != 1000 || dc.CashReturns != 0 || dc.NetCash != 1000 {
		t.Fatalf("pre-return daily: TotalCash=%v CashReturns=%v NetCash=%v, want 1000/0/1000",
			dc.TotalCash, dc.CashReturns, dc.NetCash)
	}

	// Full return: refund 1000, debt 0, cash 1000.
	ret, err := s.ProcessReturnAll(ctx, mgr, legacyID, "إرجاع كامل", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 1000 || ret.CashRefund != 1000 || ret.DebtReduction != 0 {
		t.Fatalf("refund wrong: %+v", ret)
	}

	// After the return the gross sale must STAY counted (fallback
	// survives the negative Payments row) and the drawer must be
	// back at its pre-sale baseline: +1000 sale, -1000 return.
	sum, _ = s.ShiftSummary(ctx, shiftID)
	if sum.CashSales != 1000 || sum.CashReturns != 1000 || sum.ExpectedBalance != 5000 {
		t.Fatalf("post-return summary: CashSales=%v CashReturns=%v Expected=%v, want 1000/1000/5000",
			sum.CashSales, sum.CashReturns, sum.ExpectedBalance)
	}
	dc, _ = s.DailyClose(ctx, admin, Today())
	if dc.TotalCash != 1000 || dc.CashReturns != 1000 || dc.NetCash != 0 {
		t.Fatalf("post-return daily: TotalCash=%v CashReturns=%v NetCash=%v, want 1000/1000/0",
			dc.TotalCash, dc.CashReturns, dc.NetCash)
	}
}

// The same return on a normal app-created invoice (at-sale Payments
// row exists) must behave exactly as before: the probe was already
// true for it, so the fix changes nothing there.
func TestFix20_NormalInvoiceUnaffectedByFallbackFix(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(5000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	if _, err := s.DB.Exec(`UPDATE Products SET Quantity=8000 WHERE ProductID=?`, pid); err != nil {
		t.Fatal(err)
	}
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 2000, 1000, 2000, 0, "Cash", 1)
	sum, _ := s.ShiftSummary(ctx, shiftID)
	if sum.CashSales != 2000 || sum.ExpectedBalance != 7000 {
		t.Fatalf("pre-return summary: CashSales=%v Expected=%v, want 2000/7000", sum.CashSales, sum.ExpectedBalance)
	}
	if _, err := s.ProcessReturnAll(ctx, mgr, invID, "إرجاع كامل", "Cash"); err != nil {
		t.Fatalf("return: %v", err)
	}
	sum, _ = s.ShiftSummary(ctx, shiftID)
	if sum.CashSales != 2000 || sum.CashReturns != 2000 || sum.ExpectedBalance != 5000 {
		t.Fatalf("post-return summary: CashSales=%v CashReturns=%v Expected=%v, want 2000/2000/5000",
			sum.CashSales, sum.CashReturns, sum.ExpectedBalance)
	}
}

// --- FIX-20 R20-A-F2: customer statement nets refunded collections ---
//
// TotalPaid is the sum of the listed payment rows (net cash received:
// at-sale + collections - refunds). TotalPurchases stays gross (the
// listed invoice rows sum to it); TotalRemaining is the live balance.

func statementTotals(t *testing.T, st *CustomerStatement) (paid, purch, rem money.Cents, refundRows int) {
	t.Helper()
	var rowsSum money.Cents
	for _, p := range st.Payments {
		rowsSum += p.Amount
		if p.Amount < 0 {
			refundRows++
		}
	}
	if rowsSum != st.TotalPaid {
		t.Fatalf("listed payment rows sum to %v, want TotalPaid %v", rowsSum, st.TotalPaid)
	}
	return st.TotalPaid, st.TotalPurchases, st.TotalRemaining, refundRows
}

func TestFix20_StatementNetsRefundedCollection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 2000, 3000, 100000)
	cust := testCustomer(t, s, "عميل كشف")
	// S10 shape: credit sale 3000, paid 0.
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 2000, 0, 3000, "Cash", cust)
	// Cash collection needs the collecting user's own open shift today.
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	st, err := s.CustomerStatement(ctx, admin, cust)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	if paid, purch, rem, _ := statementTotals(t, st); paid != 3000 || purch != 3000 || rem != 0 {
		t.Fatalf("after collection: paid=%v purch=%v rem=%v, want 3000/3000/0", paid, purch, rem)
	}
	// Full return: refund 3000, debt 0, cash 3000.
	ret, err := s.ProcessReturnAll(ctx, mgr, invID, "إرجاع كامل", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.CashRefund != 3000 {
		t.Fatalf("refund wrong: %+v", ret)
	}
	st, _ = s.CustomerStatement(ctx, admin, cust)
	paid, purch, rem, refunds := statementTotals(t, st)
	if paid != 0 || purch != 3000 || rem != 0 {
		t.Fatalf("after refund: paid=%v purch=%v rem=%v, want 0/3000/0", paid, purch, rem)
	}
	if refunds != 1 {
		t.Fatalf("want exactly 1 listed refund row, got %d (rows: %+v)", refunds, st.Payments)
	}
	// Rows read in flow order even when the collection and the
	// refund share one second: collection first, refund second.
	if len(st.Payments) != 2 || st.Payments[0].Amount != 3000 || st.Payments[1].Amount != -3000 {
		t.Fatalf("payment rows out of flow order: %+v", st.Payments)
	}
	var refundAmt money.Cents
	for _, p := range st.Payments {
		if p.Amount < 0 {
			refundAmt = p.Amount
			if p.Notes != "مرتجع" {
				t.Fatalf("refund row notes = %q, want مرتجع", p.Notes)
			}
		}
	}
	if refundAmt != -3000 {
		t.Fatalf("refund row amount = %v, want -3000", refundAmt)
	}
}

// The symmetric shape: a sale paid in full at sale time, then fully
// returned. The refund repays at-sale money, so TotalPaid must land
// on 0 — never negative.
func TestFix20_StatementPaidAtSaleReturnIsNotNegative(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 2000, 3000, 100000)
	cust := testCustomer(t, s, "عميل نقدي")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 2000, 3000, 0, "Cash", cust)
	st, err := s.CustomerStatement(ctx, admin, cust)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	if paid, _, rem, _ := statementTotals(t, st); paid != 3000 || rem != 0 {
		t.Fatalf("before return: paid=%v rem=%v, want 3000/0", paid, rem)
	}
	if _, err := s.ProcessReturnAll(ctx, mgr, invID, "إرجاع كامل", "Cash"); err != nil {
		t.Fatalf("return: %v", err)
	}
	st, _ = s.CustomerStatement(ctx, admin, cust)
	paid, purch, rem, refunds := statementTotals(t, st)
	if paid != 0 || purch != 3000 || rem != 0 || refunds != 1 {
		t.Fatalf("after return: paid=%v purch=%v rem=%v refunds=%d, want 0/3000/0/1",
			paid, purch, rem, refunds)
	}
}

// Partial return: the debt-absorbed leg moves no cash (no refund row),
// the paid-out leg does (one refund row); TotalPaid nets only the
// cash actually handed back.
func TestFix20_StatementPartialReturn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 2000, 3000, 100000)
	cust := testCustomer(t, s, "عميل جزئي")
	// Net 9000, paid 1000 at sale, remaining 8000.
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 3000, 3000, 2000, 1000, 8000, "Cash", cust)
	res, err := s.ProcessReturn(ctx, mgr, invID, []ReturnLineInput{{ProductID: pid, Qty: 2000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return 2 units: %v", err)
	}
	if res.TotalRefund != 6000 || res.DebtReduction != 6000 || res.CashRefund != 0 {
		t.Fatalf("debt-absorbed return wrong: %+v", res)
	}
	st, _ := s.CustomerStatement(ctx, admin, cust)
	if paid, _, rem, refunds := statementTotals(t, st); paid != 1000 || rem != 2000 || refunds != 0 {
		t.Fatalf("after debt-absorbed return: paid=%v rem=%v refunds=%d, want 1000/2000/0", paid, rem, refunds)
	}
	res, err = s.ProcessReturn(ctx, mgr, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return last unit: %v", err)
	}
	if res.TotalRefund != 3000 || res.DebtReduction != 2000 || res.CashRefund != 1000 {
		t.Fatalf("paid-out return wrong: %+v", res)
	}
	st, _ = s.CustomerStatement(ctx, admin, cust)
	if paid, purch, rem, refunds := statementTotals(t, st); paid != 0 || purch != 9000 || rem != 0 || refunds != 1 {
		t.Fatalf("after paid-out return: paid=%v purch=%v rem=%v refunds=%d, want 0/9000/0/1",
			paid, purch, rem, refunds)
	}
}

// A canceled invoice contributes nothing to the payments section:
// its at-sale leg is excluded (the cancel handed the cash back via
// CashBox with no offsetting Payments row), so TotalPaid stays 0
// and never goes negative through the cancel path either.
func TestFix20_StatementCanceledInvoiceExcluded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 2000, 3000, 100000)
	cust := testCustomer(t, s, "عميل ملغى")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 2000, 3000, 0, "Cash", cust)
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	st, err := s.CustomerStatement(ctx, admin, cust)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	if paid, purch, rem, _ := statementTotals(t, st); paid != 0 || purch != 0 || rem != 0 {
		t.Fatalf("after cancel: paid=%v purch=%v rem=%v, want 0/0/0", paid, purch, rem)
	}
	if len(st.Payments) != 0 {
		t.Fatalf("canceled invoice must leave no payment rows, got %+v", st.Payments)
	}
}
