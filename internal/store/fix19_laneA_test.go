package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

// FIX-19 R19-B-F1 (lane A): reports' TotalPaid must net returns out.
// PaidAmount is gross cash received (at-sale + collections) and is never
// zeroed by ProcessReturn; the refund is a negative Payments row for the
// paid-out part only (paidOut = TotalRefund - debtReduction). TotalPaid
// is therefore PaidAmount minus the negative-Payments sum, per invoice,
// in SalesReportFor (total + per-method), PaymentReportFor and
// CustomerReportFor — all from the one netPaidExpr.

// fix19aRefundLeg posts the paid-out leg of a synthetic return: a
// negative Payments row (as ProcessReturn does) plus its Returns row,
// so the fixture mirrors a real return without going through the full
// return flow. totalRefund is the goods value returned; paidOut is the
// cash actually handed back (totalRefund - debtReduction).
func fix19aRefundLeg(t *testing.T, s *Store, userID, invID, custID int64, productID int64, totalRefund, paidOut money.Cents, method string) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT INTO Returns(InvoiceID,ProductID,ProductName,Quantity,UnitPrice,BuyPrice,TotalRefund,Reason,UserID,UserName,ReturnDate,ReturnPaymentMethod,ShiftID)
		VALUES(?,?,'أرز',1000,3000,2000,?,'تالف',?,'fix19a',?,?,0)`,
		invID, productID, int64(totalRefund), userID, Now(), method); err != nil {
		t.Fatalf("seed return: %v", err)
	}
	if paidOut > 0 {
		if _, err := s.DB.Exec(`INSERT INTO Payments(InvoiceID,CustomerID,Amount,PaymentDate,PaymentMethod,Notes,UserID)
			VALUES(?,?,?,?,?,'مرتجع',?)`, invID, custID, int64(-paidOut), Now(), method, userID); err != nil {
			t.Fatalf("seed refund leg: %v", err)
		}
	}
}

func fix19aByMethod(rows []PaymentRow, method string) (count, total int64, ok bool) {
	for _, r := range rows {
		if r.Method == method {
			return r.Count, int64(r.Total), true
		}
	}
	return 0, 0, false
}

func TestFix19A_NetPaidFullAndPartialReturns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix19a", "admin")
	shiftID, err := s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 2000, 3000, 100000)
	cust := testCustomer(t, s, "عميل التقارير")
	today := Today()

	// A: net 3000, paid 1000 Cash, full return (refund 3000, debt 2000,
	// paidOut 1000) -> net paid 0.
	invA := testSaleInvoice(t, s, admin, shiftID, 1, pid, 1000, 3000, 2000, 1000, 2000, "Cash", cust)
	fix19aRefundLeg(t, s, admin.ID, invA, cust, pid, 3000, 1000, "Cash")
	// B: net 3000, paid 1000 Cash, partial return (refund 2500, debt
	// 2000, paidOut 500) -> net paid 500.
	invB := testSaleInvoice(t, s, admin, shiftID, 2, pid, 1000, 3000, 2000, 1000, 2000, "Cash", cust)
	fix19aRefundLeg(t, s, admin.ID, invB, cust, pid, 2500, 500, "Cash")
	// C: net 3000, paid 1000 Cash, debt-absorbed return only (refund
	// 1500, debt 1500, paidOut 0) -> net paid stays 1000.
	invC := testSaleInvoice(t, s, admin, shiftID, 3, pid, 1000, 3000, 2000, 1000, 2000, "Cash", cust)
	fix19aRefundLeg(t, s, admin.ID, invC, cust, pid, 1500, 0, "Cash")
	// D: canceled control — net 3000, paid 1000 Cash, canceled ->
	// excluded entirely.
	invD := testSaleInvoice(t, s, admin, shiftID, 4, pid, 1000, 3000, 2000, 1000, 2000, "Cash", cust)
	if _, err := s.DB.Exec(`UPDATE Invoices SET IsCanceled=1, RemainingAmount=0 WHERE InvoiceID=?`, invD); err != nil {
		t.Fatal(err)
	}
	// E: net 2000, paid 2000 Visa, no return -> net paid 2000 (Visa).
	invE := testSaleInvoice(t, s, admin, shiftID, 5, pid, 1000, 2000, 2000, 2000, 0, "Visa", cust)
	_ = invE
	// F: net 3000, paid 1000 Cash, full return refunded by Visa
	// (refund 3000, paidOut 1000 in Visa) -> net paid 0, and the
	// refund stays on the Cash (sale-method) bucket: Visa must NOT
	// go negative.
	invF := testSaleInvoice(t, s, admin, shiftID, 6, pid, 1000, 3000, 2000, 1000, 2000, "Cash", cust)
	fix19aRefundLeg(t, s, admin.ID, invF, cust, pid, 3000, 1000, "Visa")

	// Pre-fix (gross) totals would be: TotalPaid 6000, Cash 4000,
	// Visa 2000, customer TotalPaid 6000.
	sales, err := s.SalesReportFor(ctx, admin, today, today)
	if err != nil {
		t.Fatal(err)
	}
	if int64(sales.TotalPaid) != 3500 {
		t.Fatalf("SalesReportFor TotalPaid = %d, want 3500 (0+500+1000+2000+0)", sales.TotalPaid)
	}
	if c, tot, ok := fix19aByMethod(sales.ByPayment, "Cash"); !ok || c != 4 || tot != 1500 {
		t.Fatalf("SalesReportFor Cash = count %d total %d (ok %v), want 4 / 1500", c, tot, ok)
	}
	if c, tot, ok := fix19aByMethod(sales.ByPayment, "Visa"); !ok || c != 1 || tot != 2000 {
		t.Fatalf("SalesReportFor Visa = count %d total %d (ok %v), want 1 / 2000", c, tot, ok)
	}

	pay, err := s.PaymentReportFor(ctx, admin, today, today)
	if err != nil {
		t.Fatal(err)
	}
	if c, tot, ok := fix19aByMethod(pay.ByMethod, "Cash"); !ok || c != 4 || tot != 1500 {
		t.Fatalf("PaymentReportFor Cash = count %d total %d (ok %v), want 4 / 1500", c, tot, ok)
	}
	if c, tot, ok := fix19aByMethod(pay.ByMethod, "Visa"); !ok || c != 1 || tot != 2000 {
		t.Fatalf("PaymentReportFor Visa = count %d total %d (ok %v), want 1 / 2000", c, tot, ok)
	}
	// The Returns field itself is untouched: the seeded refunds sum.
	if int64(pay.Returns) != 10000 {
		t.Fatalf("PaymentReportFor Returns = %d, want 10000 (3000+2500+1500+3000)", pay.Returns)
	}

	custRep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatal(err)
	}
	if int64(custRep.TotalPaid) != 3500 {
		t.Fatalf("CustomerReportFor TotalPaid = %d, want 3500", custRep.TotalPaid)
	}
	if custRep.InvoiceCount != 5 {
		t.Fatalf("CustomerReportFor InvoiceCount = %d, want 5 (canceled excluded)", custRep.InvoiceCount)
	}
	if int64(custRep.TotalSales) != 14000 {
		t.Fatalf("CustomerReportFor TotalSales = %d, want 14000 (netting is paid-only)", custRep.TotalSales)
	}
	// The per-row PaidAmount stays the gross historical fact — only
	// the summed TotalPaid nets.
	for _, inv := range custRep.Invoices {
		if inv.InvoiceID == invA && int64(inv.PaidAmount) != 1000 {
			t.Fatalf("customer row A PaidAmount = %d, want 1000 gross (display unchanged)", inv.PaidAmount)
		}
	}
}

func TestFix19A_LivePathFullReturnAndCancel(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix19a2", "admin")
	shiftID, err := s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 2000, 3000, 100000)
	cust := testCustomer(t, s, "عميل حي")
	today := Today()

	reportPaid := func() (salesTotal, salesCash, payCash, custTotal int64) {
		t.Helper()
		sales, err := s.SalesReportFor(ctx, admin, today, today)
		if err != nil {
			t.Fatal(err)
		}
		pay, err := s.PaymentReportFor(ctx, admin, today, today)
		if err != nil {
			t.Fatal(err)
		}
		crep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
		if err != nil {
			t.Fatal(err)
		}
		_, sc, _ := fix19aByMethod(sales.ByPayment, "Cash")
		_, pc, _ := fix19aByMethod(pay.ByMethod, "Cash")
		return int64(sales.TotalPaid), sc, pc, int64(crep.TotalPaid)
	}
	baseSales, baseCash, basePay, baseCust := reportPaid()

	// Sale 1: 1 unit x 3000, paid 1000 Cash, remaining 2000.
	res1, err := s.CreateInvoice(ctx, admin, CreateInvoiceInput{
		CustomerID: cust, SaleType: "retail", PaymentMethod: "Cash", Paid: 1000,
		Lines: []SaleLineInput{{ProductID: pid, Qty: 1000}},
	})
	if err != nil {
		t.Fatalf("sale 1: %v", err)
	}
	if res1.Net != 3000 || res1.Paid != 1000 || res1.Remaining != 2000 {
		t.Fatalf("sale 1 = %+v, want net 3000 paid 1000 remaining 2000", res1)
	}
	if st, sc, pc, ct := reportPaid(); st != baseSales+1000 || sc != baseCash+1000 || pc != basePay+1000 || ct != baseCust+1000 {
		t.Fatalf("after sale: total %d cash %d payCash %d cust %d, want each +1000 over base (%d/%d/%d/%d)",
			st, sc, pc, ct, baseSales, baseCash, basePay, baseCust)
	}

	// Full return, Cash: refund 3000, debt 2000, paidOut 1000.
	ret, err := s.ProcessReturnAll(ctx, admin, res1.InvoiceID, "إرجاع كامل", "Cash")
	if err != nil {
		t.Fatalf("full return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.DebtReduction != 2000 || ret.CashRefund != 1000 {
		t.Fatalf("return = %+v, want refund 3000 debt 2000 cash 1000", ret)
	}
	if st, sc, pc, ct := reportPaid(); st != baseSales || sc != baseCash || pc != basePay || ct != baseCust {
		t.Fatalf("after full return: total %d cash %d payCash %d cust %d, want base (%d/%d/%d/%d) — delta must be -1000",
			st, sc, pc, ct, baseSales, baseCash, basePay, baseCust)
	}
	// Profit netting is untouched: sale profit 1000 (3000-2000) minus
	// return profit (3000-2000) = 0.
	sales, err := s.SalesReportFor(ctx, admin, today, today)
	if err != nil {
		t.Fatal(err)
	}
	if int64(sales.TotalProfit) != 0 {
		t.Fatalf("TotalProfit after full return = %d, want 0 (profit netting unchanged)", sales.TotalProfit)
	}
	// Drawer/shift figures are untouched by the report fix and keep
	// their pre-existing values: cash sale +1000, cash return -1000.
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ExpectedBalance != 10000 || sum.CashSales != 1000 || sum.CashReturns != 1000 {
		t.Fatalf("shift summary = expected %d cashSales %d cashReturns %d, want 10000/1000/1000",
			sum.ExpectedBalance, sum.CashSales, sum.CashReturns)
	}

	// Cancel control: same shape, cancel drops TotalPaid by 1000 too.
	res2, err := s.CreateInvoice(ctx, admin, CreateInvoiceInput{
		CustomerID: cust, SaleType: "retail", PaymentMethod: "Cash", Paid: 1000,
		Lines: []SaleLineInput{{ProductID: pid, Qty: 1000}},
	})
	if err != nil {
		t.Fatalf("sale 2: %v", err)
	}
	if st, _, _, _ := reportPaid(); st != baseSales+1000 {
		t.Fatalf("after sale 2: total %d, want base+1000 (%d)", st, baseSales+1000)
	}
	if _, err := s.CancelInvoice(ctx, admin, res2.InvoiceID, "اختبار إلغاء"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if st, sc, pc, ct := reportPaid(); st != baseSales || sc != baseCash || pc != basePay || ct != baseCust {
		t.Fatalf("after cancel: total %d cash %d payCash %d cust %d, want base (%d/%d/%d/%d)",
			st, sc, pc, ct, baseSales, baseCash, basePay, baseCust)
	}
}

func TestFix19A_LivePathPartialReturnNetsOnlyPaidOut(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fix19a3", "admin")
	if _, err := s.OpenShift(ctx, admin, money.Cents(10000), false); err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "أرز", 500, 1000, 100000)
	cust := testCustomer(t, s, "عميل جزئي")
	today := Today()

	paidOf := func() int64 {
		t.Helper()
		sales, err := s.SalesReportFor(ctx, admin, today, today)
		if err != nil {
			t.Fatal(err)
		}
		return int64(sales.TotalPaid)
	}
	// Sale: 3 units x 1000 = net 3000, paid 1000 Cash, remaining 2000.
	res, err := s.CreateInvoice(ctx, admin, CreateInvoiceInput{
		CustomerID: cust, SaleType: "retail", PaymentMethod: "Cash", Paid: 1000,
		Lines: []SaleLineInput{{ProductID: pid, Qty: 3000}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if paidOf() != 1000 {
		t.Fatalf("after sale: TotalPaid = %d, want 1000", paidOf())
	}
	// Return 2 units: refund 2000, fully absorbed by the 2000 debt,
	// paidOut 0 -> TotalPaid must NOT move.
	ret1, err := s.ProcessReturn(ctx, admin, res.InvoiceID,
		[]ReturnLineInput{{ProductID: pid, Qty: 2000}}, "جزئي أول", "Cash")
	if err != nil {
		t.Fatalf("return 2 units: %v", err)
	}
	if ret1.TotalRefund != 2000 || ret1.DebtReduction != 2000 || ret1.CashRefund != 0 {
		t.Fatalf("return 1 = %+v, want refund 2000 debt 2000 cash 0", ret1)
	}
	if paidOf() != 1000 {
		t.Fatalf("after debt-absorbed return: TotalPaid = %d, want 1000 (unchanged)", paidOf())
	}
	// Return the last unit: refund 1000, debt 0, paidOut 1000 ->
	// TotalPaid drops to 0.
	ret2, err := s.ProcessReturn(ctx, admin, res.InvoiceID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "جزئي ثان", "Cash")
	if err != nil {
		t.Fatalf("return 1 unit: %v", err)
	}
	if ret2.TotalRefund != 1000 || ret2.DebtReduction != 0 || ret2.CashRefund != 1000 {
		t.Fatalf("return 2 = %+v, want refund 1000 debt 0 cash 1000", ret2)
	}
	if paidOf() != 0 {
		t.Fatalf("after paid-out return: TotalPaid = %d, want 0", paidOf())
	}
}
