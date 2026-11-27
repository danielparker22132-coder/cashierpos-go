package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

// --- FIX-21 R21-B-F1: a collection on a later-canceled invoice must
// not stay on the customer statement ---
//
// CustomerStatement leg 1 (CustomerPayments) had no IsCanceled scope,
// while legs 2 (at-sale) and 3 (refunds) both carry i.IsCanceled=0 —
// and CancelInvoice never deletes or reverses CustomerPayments rows.
// So credit 3000 -> collect 3000 -> cancel left TotalPaid 3000 with
// purchases 0 and remaining 0, breaking the coherence identity
// (TotalRemaining = TotalPurchases - TotalPaid - totalRefund).
// Leg 1 now mirrors the ListDebtors pattern: a collection counts only
// when its invoice is NULL/0 (walk-in, no invoice) or non-canceled.

// TestFix21_StatementCollectionOnCanceledInvoiceExcluded is the
// primary shape: credit sale, full collection, then cancel.
func TestFix21_StatementCollectionOnCanceledInvoiceExcluded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل ملغى بعد السداد")
	// Credit sale 3000, paid 0.
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 0, 3000, "Cash", cust)
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
	if len(st.Payments) != 1 || st.Payments[0].Amount != 3000 {
		t.Fatalf("after collection: want exactly 1 payment row of 3000, got %+v", st.Payments)
	}
	// Cancel: the CashBox-only refund hands the money back; the
	// statement must show nothing was ever paid on this invoice.
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	st, err = s.CustomerStatement(ctx, admin, cust)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	if paid, purch, rem, _ := statementTotals(t, st); paid != 0 || purch != 0 || rem != 0 {
		t.Fatalf("after cancel: paid=%v purch=%v rem=%v, want 0/0/0", paid, purch, rem)
	}
	if len(st.Payments) != 0 {
		t.Fatalf("canceled invoice must leave no payment rows, got %+v", st.Payments)
	}
	// Coherence identity holds: 0 = 0 - 0 - 0.
	if st.TotalRemaining != st.TotalPurchases-st.TotalPaid {
		t.Fatalf("coherence broken: remaining=%v purchases=%v paid=%v",
			st.TotalRemaining, st.TotalPurchases, st.TotalPaid)
	}
}

// TestFix21_StatementNullInvoiceCollectionSurvives: a walk-in
// collection with no invoice attached (NULL or 0 InvoiceID) must
// still show — the fix must not drop unattributed collections.
func TestFix21_StatementNullInvoiceCollectionSurvives(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "adm1", "admin")
	cust := testCustomer(t, s, "عميل سداد بلا فاتورة")
	if _, err := s.DB.Exec(`INSERT INTO CustomerPayments(CustomerID,InvoiceID,Amount,PaymentDate,PaymentMethod,ReceivedBy,UserID,Notes)
		VALUES(?,NULL,3000,?,'Cash','admin',?, 'سداد بلا فاتورة')`, cust, Now(), admin.ID); err != nil {
		t.Fatalf("insert NULL-invoice collection: %v", err)
	}
	// A 0-InvoiceID row cannot be inserted under the store's FK
	// enforcement (Open: ?_pragma=foreign_keys(1); 0 matches no
	// invoice, so the insert fails with a constraint error) — it
	// can only exist as legacy data from before FK enforcement.
	// Insert it with FK briefly off, then verify FK is back ON, so
	// the statement's 0-branch is still exercised at read time.
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
	st, err := s.CustomerStatement(ctx, admin, cust)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	if paid, purch, rem, _ := statementTotals(t, st); paid != 4000 || purch != 0 || rem != 0 {
		t.Fatalf("walk-in collections: paid=%v purch=%v rem=%v, want 4000/0/0", paid, purch, rem)
	}
	if len(st.Payments) != 2 {
		t.Fatalf("want exactly 2 payment rows (NULL + 0 InvoiceID), got %+v", st.Payments)
	}
}

// TestFix21_StatementCollectReturnCancel: collect -> full return ->
// cancel. The refund leg already vanished on cancel (leg 3 is
// scoped); the collection leg must vanish too, leaving no rows.
func TestFix21_StatementCollectReturnCancel(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mgr := b2TestUser(t, s, "mgr1", "manager")
	admin := b2TestUser(t, s, "adm1", "admin")
	shiftID, err := s.OpenShift(ctx, mgr, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 3000, 100000)
	cust := testCustomer(t, s, "عميل إرجاع ثم إلغاء")
	invID := testSaleInvoice(t, s, mgr, shiftID, 1, pid, 1000, 3000, 1000, 0, 3000, "Cash", cust)
	if _, err := s.PayCustomerDebtGeneral(ctx, mgr, cust, money.Cents(3000), "Cash", ""); err != nil {
		t.Fatalf("collect: %v", err)
	}
	ret, err := s.ProcessReturnAll(ctx, mgr, invID, "إرجاع كامل", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 3000 || ret.CashRefund != 3000 {
		t.Fatalf("refund wrong: %+v", ret)
	}
	st, _ := s.CustomerStatement(ctx, admin, cust)
	if paid, purch, rem, refunds := statementTotals(t, st); paid != 0 || purch != 3000 || rem != 0 || refunds != 1 {
		t.Fatalf("after return: paid=%v purch=%v rem=%v refunds=%d, want 0/3000/0/1",
			paid, purch, rem, refunds)
	}
	if _, err := s.CancelInvoice(ctx, admin, invID, "إلغاء بعد الإرجاع"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	st, err = s.CustomerStatement(ctx, admin, cust)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	if paid, purch, rem, _ := statementTotals(t, st); paid != 0 || purch != 0 || rem != 0 {
		t.Fatalf("after return+cancel: paid=%v purch=%v rem=%v, want 0/0/0", paid, purch, rem)
	}
	if len(st.Payments) != 0 {
		t.Fatalf("return+cancel must leave no payment rows, got %+v", st.Payments)
	}
}
