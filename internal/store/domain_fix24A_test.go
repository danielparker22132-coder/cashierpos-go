package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-24 R24-A-O1: Q-04 newest-first tie was nondeterministic ---
//
// CustomerReportFor's payments query ordered by cp.PaymentDate DESC
// only. Now() is second-precision, so two collections on the same
// invoice in the same second with different amounts had undefined
// refund-allocation order: which row absorbed the refund varied
// with SQLite's scan order. The sums were always correct; only the
// per-row attribution was unstable. The query now orders by
// cp.PaymentDate DESC, cp.CPaymentID DESC, so the newest
// collection (highest CPaymentID) absorbs the refund first, every
// run, matching the documented newest-first netting.

// TestFix24A_SameSecondCollectionsRefundNewestFirst: 3 units at
// 3000 (9000), collect 5000 then 4000 pinned to the SAME second,
// return 1 unit (cash refund 3000) -> the NEWEST collection (4000)
// absorbs the refund first and nets to 1000, the older stays at
// 5000; rows are [1000, 5000] in that order and sum to the header
// TotalPaid 6000.
func TestFix24A_SameSecondCollectionsRefundNewestFirst(t *testing.T) {
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
	cust := testCustomer(t, s, "عميل سدادان بنفس الثانية ومرتجع")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 3000, 3000, 1000, 0, 9000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(5000), "Cash", ""); err != nil {
		t.Fatalf("collect 5000: %v", err)
	}
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(4000), "Cash", ""); err != nil {
		t.Fatalf("collect 4000: %v", err)
	}
	// Pin BOTH collections to the identical second: the tie the
	// old ORDER BY could not break.
	today := Today()
	rows, err := s.DB.Query(`SELECT CPaymentID, PaymentDate FROM CustomerPayments WHERE CustomerID=? ORDER BY CPaymentID`, cust)
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	var ids []int64
	var dates []string
	for rows.Next() {
		var id int64
		var d string
		if err := rows.Scan(&id, &d); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
		dates = append(dates, d)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("want 2 collection rows, got %v", ids)
	}
	sameSecond := today + " 10:00:00"
	for _, id := range ids {
		if _, err := s.DB.Exec(`UPDATE CustomerPayments SET PaymentDate=? WHERE CPaymentID=?`, sameSecond, id); err != nil {
			t.Fatalf("pin date: %v", err)
		}
	}
	t.Logf("collections: ids=%v dates(before pin)=%v pinned=%s", ids, dates, sameSecond)
	ret, err := s.ProcessReturn(ctx, sup, invID, []ReturnLineInput{{ProductID: pid, Qty: 1000}}, "إرجاع جزئي", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.CashRefund != 3000 {
		t.Fatalf("refund wrong: %+v", ret)
	}
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 6000 {
		t.Fatalf("after return: TotalPaid=%v, want 6000", rep.TotalPaid)
	}
	var sum money.Cents
	for _, p := range rep.Payments {
		sum += p.Amount
	}
	if sum != 6000 {
		t.Fatalf("rows sum=%v, want 6000 (header TotalPaid)", sum)
	}
	if len(rep.Payments) != 2 || rep.Payments[0].Amount != 1000 || rep.Payments[1].Amount != 5000 {
		t.Fatalf("same-second refund: want rows [1000, 5000] (newest 4000-collection nets to 1000), got %+v", rep.Payments)
	}
}

// TestFix24A_SameSecondCollectionsOrderStable: two same-second
// collections with NO refund must still display newest-first by
// CPaymentID (4000 before 5000) — the tie-breaker pins the display
// order itself, not only the refund allocation.
func TestFix24A_SameSecondCollectionsOrderStable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل سدادان بنفس الثانية بلا مرتجع")
	_ = testSaleInvoice(t, s, mgr, shiftID, 1, pid, 3000, 3000, 1000, 0, 9000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(5000), "Cash", ""); err != nil {
		t.Fatalf("collect 5000: %v", err)
	}
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(4000), "Cash", ""); err != nil {
		t.Fatalf("collect 4000: %v", err)
	}
	today := Today()
	if _, err := s.DB.Exec(`UPDATE CustomerPayments SET PaymentDate=? WHERE CustomerID=?`, today+" 10:00:00", cust); err != nil {
		t.Fatalf("pin dates: %v", err)
	}
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 9000 {
		t.Fatalf("TotalPaid=%v, want 9000", rep.TotalPaid)
	}
	if len(rep.Payments) != 2 || rep.Payments[0].Amount != 4000 || rep.Payments[1].Amount != 5000 {
		t.Fatalf("same-second order: want rows [4000, 5000] (newest first by CPaymentID), got %+v", rep.Payments)
	}
}
