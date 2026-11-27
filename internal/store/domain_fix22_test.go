package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-22 R22-A-F1: a collection on a later-canceled invoice must
// not stay in the Q-04 customer report's "المدفوعات" table ---
//
// CustomerReportFor's payments query had no IsCanceled scope, while
// the report's own header totals (computed from the invoices loop
// above, non-canceled only) correctly excluded the canceled invoice.
// So credit 3000 -> collect 3000 -> cancel left the header reading
// مدفوعات الفترة 0.00 while the payments table still showed the
// 30.00 row. Same class as FIX-21 R21-B-F1 on the statement; the
// query now mirrors CustomerStatement leg 1: a collection counts
// only when its invoice is NULL/0 (walk-in, no invoice) or
// non-canceled.

// TestFix22_ReportCollectionOnCanceledInvoiceExcluded is the primary
// shape: credit sale, full collection, then cancel.
func TestFix22_ReportCollectionOnCanceledInvoiceExcluded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل تقرير ملغى بعد السداد")
	// Credit sale 3000, paid 0.
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 0, 3000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	today := Today()
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 3000 || rep.TotalSales != 3000 || rep.Balance != 0 {
		t.Fatalf("after collection: paid=%v sales=%v balance=%v, want 3000/3000/0",
			rep.TotalPaid, rep.TotalSales, rep.Balance)
	}
	if len(rep.Payments) != 1 || rep.Payments[0].Amount != 3000 {
		t.Fatalf("after collection: want exactly 1 payment row of 3000, got %+v", rep.Payments)
	}
	// Cancel: the CashBox-only refund hands the money back; the
	// report must show nothing was ever paid on this invoice —
	// header 0.00 AND an empty payments table.
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	rep, err = s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 0 || rep.TotalSales != 0 || rep.Balance != 0 {
		t.Fatalf("after cancel: paid=%v sales=%v balance=%v, want 0/0/0",
			rep.TotalPaid, rep.TotalSales, rep.Balance)
	}
	if len(rep.Payments) != 0 {
		t.Fatalf("canceled invoice must leave no payment rows in the report, got %+v", rep.Payments)
	}
}

// TestFix22_ReportNullInvoiceCollectionSurvives: a walk-in collection
// with no invoice attached (NULL or 0 InvoiceID) must still show in
// the report's payments table — the fix must not drop unattributed
// collections.
func TestFix22_ReportNullInvoiceCollectionSurvives(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	cust := testCustomer(t, s, "عميل تقرير سداد بلا فاتورة")
	if _, err := s.DB.Exec(`INSERT INTO CustomerPayments(CustomerID,InvoiceID,Amount,PaymentDate,PaymentMethod,ReceivedBy,UserID,Notes)
		VALUES(?,NULL,3000,?,'Cash','admin',?, 'سداد بلا فاتورة')`, cust, Now(), admin.ID); err != nil {
		t.Fatalf("insert NULL-invoice collection: %v", err)
	}
	// A 0-InvoiceID row cannot be inserted under the store's FK
	// enforcement (Open: ?_pragma=foreign_keys(1); 0 matches no
	// invoice, so the insert fails with a constraint error) — it
	// can only exist as legacy data from before FK enforcement.
	// Insert it with FK briefly off, then verify FK is back ON, so
	// the report's 0-branch is still exercised at read time.
	if _, err := s.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("FK off: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO CustomerPayments(CustomerID,InvoiceID,Amount,PaymentDate,PaymentMethod,ReceivedBy,UserID,Notes)
		VALUES(?,0,1000,?,'Cash','admin',?, 'سداد بلا فاتورة')`, cust, Now(), admin.ID); err != nil {
		t.Fatalf("insert 0-invoice collection: %v", err)
	}
	if _, err := s.DB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatalf("FK on: %v", err)
	}
	var fkOn int
	if err := s.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fkOn); err != nil || fkOn != 1 {
		t.Fatalf("FK not re-enabled after 0-row insert (fk=%d err=%v)", fkOn, err)
	}
	today := Today()
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rep.Payments) != 2 {
		t.Fatalf("want exactly 2 payment rows (NULL + 0 InvoiceID), got %+v", rep.Payments)
	}
	var sum money.Cents
	for _, p := range rep.Payments {
		sum += p.Amount
	}
	if sum != 4000 {
		t.Fatalf("walk-in collections sum=%v, want 4000", sum)
	}
}

// TestFix22_ReportNormalCollectionListed: a collection on a live
// (non-canceled) invoice still appears in the report, header and
// table agreeing.
func TestFix22_ReportNormalCollectionListed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل تقرير عادي")
	testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 0, 3000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	today := Today()
	rep, err := s.CustomerReportFor(ctx, admin, cust, today, today)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.TotalPaid != 3000 || rep.TotalSales != 3000 || rep.Balance != 0 {
		t.Fatalf("normal collection: paid=%v sales=%v balance=%v, want 3000/3000/0",
			rep.TotalPaid, rep.TotalSales, rep.Balance)
	}
	if len(rep.Payments) != 1 || rep.Payments[0].Amount != 3000 {
		t.Fatalf("normal collection must list exactly 1 payment row of 3000, got %+v", rep.Payments)
	}
}
