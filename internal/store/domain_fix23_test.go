package store

import (
	"context"
	"testing"
	"time"

	"cashierpos-go/internal/money"
)

// --- FIX-23 R23-A-F1: the Q-04 payments table was refund-blind ---
//
// CustomerReportFor's header TotalPaid nets each invoice's refund
// (PaidAmount - refunded, the negative Payments rows a return
// writes), but the "المدفوعات" table listed every collection at
// gross. Collect 3000 -> full cash return 3000 (no cancel) left the
// header at 0.00 while the table still showed the 30.00 row; a
// partial return left the row at full gross. The table now nets
// each invoice's refunds against its collection rows (newest
// first): a fully-refunded collection drops out, a partially
// refunded one reads its net received amount, walk-in rows
// (NULL/0 InvoiceID) are untouched.

// TestFix23_ReportFullReturnDropsCollectionRow is the primary
// R23-A-F1 shape: credit 3000 -> collect 3000 -> full cash return,
// NO cancel. Header 0.00 AND an empty payments table.
func TestFix23_ReportFullReturnDropsCollectionRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل مرتجع كامل بلا إلغاء")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 0, 3000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	today := Today()
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rep.Payments) != 1 || rep.Payments[0].Amount != 3000 {
		t.Fatalf("before return: want 1 payment row of 3000, got %+v", rep.Payments)
	}
	ret, err := s.ProcessReturnAll(ctx, sup, invID, "إرجاع كامل", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.CashRefund != 3000 {
		t.Fatalf("refund wrong: %+v", ret)
	}
	rep, err = s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 0 || rep.TotalSales != 3000 || rep.Balance != 0 {
		t.Fatalf("after return: paid=%v sales=%v balance=%v, want 0/3000/0",
			rep.TotalPaid, rep.TotalSales, rep.Balance)
	}
	if len(rep.Payments) != 0 {
		t.Fatalf("fully-refunded collection must leave no payment rows, got %+v", rep.Payments)
	}
}

// TestFix23_ReportPartialReturnShowsNetCollection: 3 units at 3000
// (9000), collect 9000, return 1 unit (refund 3000) -> the row must
// read the NET received 6000, not gross 9000.
func TestFix23_ReportPartialReturnShowsNetCollection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل مرتجع جزئي")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 3000, 3000, 1000, 0, 9000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(9000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	ret, err := s.ProcessReturn(ctx, sup, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "إرجاع جزئي", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.CashRefund != 3000 {
		t.Fatalf("refund wrong: %+v", ret)
	}
	today := Today()
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 6000 {
		t.Fatalf("after partial return: TotalPaid=%v, want 6000", rep.TotalPaid)
	}
	if len(rep.Payments) != 1 || rep.Payments[0].Amount != 6000 {
		t.Fatalf("after partial return: want exactly 1 payment row of 6000 (net), got %+v", rep.Payments)
	}
}

// TestFix23_ReportTwoCollectionsRefundNewestFirst: two collections
// (3000 at 09:00, 1500 at 10:00) on a 6000 invoice, then a 3000
// return whose cash paid-out is 1500 (the other 1500 reduces the
// remaining debt) -> the newest collection nets to 0 and drops,
// the older stays at 3000; the rows sum to the net received 3000.
func TestFix23_ReportTwoCollectionsRefundNewestFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل سدادان ومرتجع")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 2000, 3000, 1000, 0, 6000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect 3000: %v", err)
	}
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(1500), "Cash", ""); err != nil {
		t.Fatalf("collect 1500: %v", err)
	}
	// Pin the two collection rows to distinct times today so the
	// newest-first allocation is deterministic.
	today := Today()
	rows, err := s.DB.Query(`SELECT CPaymentID FROM CustomerPayments WHERE CustomerID=? ORDER BY CPaymentID`, cust)
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("want 2 collection rows, got %v", ids)
	}
	if _, err := s.DB.Exec(`UPDATE CustomerPayments SET PaymentDate=? WHERE CPaymentID=?`, today+" 09:00:00", ids[0]); err != nil {
		t.Fatalf("backdate first: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE CustomerPayments SET PaymentDate=? WHERE CPaymentID=?`, today+" 10:00:00", ids[1]); err != nil {
		t.Fatalf("backdate second: %v", err)
	}
	ret, err := s.ProcessReturn(ctx, sup, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "إرجاع جزئي", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.CashRefund != 1500 || ret.DebtReduction != 1500 {
		t.Fatalf("refund wrong: %+v", ret)
	}
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 3000 {
		t.Fatalf("after return: TotalPaid=%v, want 3000", rep.TotalPaid)
	}
	var sum money.Cents
	for _, p := range rep.Payments {
		sum += p.Amount
	}
	if len(rep.Payments) != 1 || rep.Payments[0].Amount != 3000 || sum != 3000 {
		t.Fatalf("after return: want exactly 1 payment row of 3000 (net), got %+v", rep.Payments)
	}
}

// --- FIX-23 R23-A-F2: DailyClose missed a cross-day cancel refund ---
//
// The same-day passes only visit invoices sold `date`, so a cancel
// refund dated today for an invoice sold yesterday never entered
// CancelRefundExcess and NetCash overstated by the refund. The
// cross-day correction is the FULL refund dated today (the at-sale
// cash was already counted on the sale day, so there is nothing in
// today's TotalCash to net it against) — the shift summary's
// cross-shift analogue (FIX-2 H-1) enters the full refund in
// CashOut the same way.

func fix23Yesterday() string {
	return time.Now().AddDate(0, 0, -1).Format("2006-01-02")
}

// TestFix23_DailyCloseCrossDayCancelRefund is the primary R23-A-F2
// shape (driver shape G, isolated): credit 3000 + collect 3000,
// both backdated to yesterday, canceled today. Today's close must
// show CancelRefundExcess 3000 and NetCash -3000 (today's only cash
// flow is the refund leaving); yesterday's close stays 3000/3000.
func TestFix23_DailyCloseCrossDayCancelRefund(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل إلغاء عبر يومين")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 0, 3000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	yday := fix23Yesterday()
	today := Today()
	if _, err := s.DB.Exec(`UPDATE Invoices SET InvoiceDate=? WHERE InvoiceID=?`, yday+" 10:00:00", invID); err != nil {
		t.Fatalf("backdate invoice: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE CustomerPayments SET PaymentDate=? WHERE CustomerID=?`, yday+" 10:05:00", cust); err != nil {
		t.Fatalf("backdate collection: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE Payments SET PaymentDate=? WHERE InvoiceID=?`, yday+" 10:00:00", invID); err != nil {
		t.Fatalf("backdate payments: %v", err)
	}
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var n, sum int64
	if err := s.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(Amount),0) FROM CashBox WHERE AutoKind='cancel_refund' AND RefInvoiceID=?`, invID).Scan(&n, &sum); err != nil || n != 1 || sum != 3000 {
		t.Fatalf("cancel_refund row: n=%d sum=%d err=%v, want 1/3000", n, sum, err)
	}
	dc, err := s.DailyClose(ctx, admin, today)
	if err != nil {
		t.Fatalf("daily close today: %v", err)
	}
	if dc.TotalCash != 0 || dc.CashReturns != 0 || dc.CancelRefundExcess != 3000 || dc.NetCash != -3000 {
		t.Fatalf("today: TotalCash=%v CashReturns=%v Excess=%v NetCash=%v, want 0/0/3000/-3000",
			dc.TotalCash, dc.CashReturns, dc.CancelRefundExcess, dc.NetCash)
	}
	dcy, err := s.DailyClose(ctx, admin, yday)
	if err != nil {
		t.Fatalf("daily close yesterday: %v", err)
	}
	if dcy.TotalCash != 3000 || dcy.NetCash != 3000 || dcy.CancelRefundExcess != 0 {
		t.Fatalf("yesterday: TotalCash=%v NetCash=%v Excess=%v, want 3000/3000/0",
			dcy.TotalCash, dcy.NetCash, dcy.CancelRefundExcess)
	}
}

// TestFix23_DailyCloseCrossDayCancelWithReturnLegs: the cross-day
// invoice also has a cash return leg dated today (3000). The leg
// must stay counted in CashReturns AND the cancel refund must enter
// the excess — NetCash -6000, with no double-count of either leg.
func TestFix23_DailyCloseCrossDayCancelWithReturnLegs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	sup := b2TestUser(t, s, "sup1", "supervisor")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل مرتجع ثم ملغى عبر يومين")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 2000, 3000, 1000, 0, 6000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(6000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	yday := fix23Yesterday()
	today := Today()
	if _, err := s.DB.Exec(`UPDATE Invoices SET InvoiceDate=? WHERE InvoiceID=?`, yday+" 10:00:00", invID); err != nil {
		t.Fatalf("backdate invoice: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE CustomerPayments SET PaymentDate=? WHERE CustomerID=?`, yday+" 10:05:00", cust); err != nil {
		t.Fatalf("backdate collection: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE Payments SET PaymentDate=? WHERE InvoiceID=?`, yday+" 10:00:00", invID); err != nil {
		t.Fatalf("backdate payments: %v", err)
	}
	ret, err := s.ProcessReturn(ctx, sup, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "إرجاع جزئي", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.CashRefund != 3000 {
		t.Fatalf("refund wrong: %+v", ret)
	}
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	dc, err := s.DailyClose(ctx, admin, today)
	if err != nil {
		t.Fatalf("daily close today: %v", err)
	}
	if dc.TotalCash != 0 || dc.CashReturns != 3000 || dc.CancelRefundExcess != 3000 || dc.NetCash != -6000 {
		t.Fatalf("today: TotalCash=%v CashReturns=%v Excess=%v NetCash=%v, want 0/3000/3000/-6000",
			dc.TotalCash, dc.CashReturns, dc.CancelRefundExcess, dc.NetCash)
	}
}

// TestFix23_DailyCloseCrossDayCashSaleCancelRefundFull: the
// distinguishing case for FULL vs excess-over-at-sale. A cash sale
// paid 3000 at sale yesterday, canceled today with a 3000 refund:
// the same-day formula (refund - atSaleCash = 0) would miss it
// entirely, but today's outflow really is the full 3000.
func TestFix23_DailyCloseCrossDayCashSaleCancelRefundFull(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل بيع نقدي ملغى عبر يومين")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 3000, 0, "Cash", cust)
	yday := fix23Yesterday()
	today := Today()
	if _, err := s.DB.Exec(`UPDATE Invoices SET InvoiceDate=? WHERE InvoiceID=?`, yday+" 10:00:00", invID); err != nil {
		t.Fatalf("backdate invoice: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE Payments SET PaymentDate=? WHERE InvoiceID=?`, yday+" 10:00:00", invID); err != nil {
		t.Fatalf("backdate payments: %v", err)
	}
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	dc, err := s.DailyClose(ctx, admin, today)
	if err != nil {
		t.Fatalf("daily close today: %v", err)
	}
	if dc.TotalCash != 0 || dc.CancelRefundExcess != 3000 || dc.NetCash != -3000 {
		t.Fatalf("today: TotalCash=%v Excess=%v NetCash=%v, want 0/3000/-3000",
			dc.TotalCash, dc.CancelRefundExcess, dc.NetCash)
	}
	dcy, err := s.DailyClose(ctx, admin, yday)
	if err != nil {
		t.Fatalf("daily close yesterday: %v", err)
	}
	if dcy.TotalCash != 3000 || dcy.NetCash != 3000 {
		t.Fatalf("yesterday: TotalCash=%v NetCash=%v, want 3000/3000", dcy.TotalCash, dcy.NetCash)
	}
}

// TestFix23_DailyCloseSameDayCancelUnchanged guards the same-day
// shape against double-counting by the new pass: sell, collect and
// cancel all today -> TotalCash 3000 (the collection), excess 3000,
// NetCash 0 — exactly the pre-fix parityfix3 shape.
func TestFix23_DailyCloseSameDayCancelUnchanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل إلغاء نفس اليوم")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 0, 3000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	dc, err := s.DailyClose(ctx, admin, Today())
	if err != nil {
		t.Fatalf("daily close: %v", err)
	}
	if dc.TotalCash != 3000 || dc.CashReturns != 0 || dc.CancelRefundExcess != 3000 || dc.NetCash != 0 {
		t.Fatalf("same-day: TotalCash=%v CashReturns=%v Excess=%v NetCash=%v, want 3000/0/3000/0",
			dc.TotalCash, dc.CashReturns, dc.CancelRefundExcess, dc.NetCash)
	}
}
