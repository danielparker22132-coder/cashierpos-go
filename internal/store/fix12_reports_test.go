// Package store — FIX-12 lane D (reports / audit / shifts) tests.
//
// SW-H07 dangerous actions (Arabic ActionType), SW-M06 VendorCashPaid,
// SW-M09 sale audit detail lines, SW-L07 suspicious_users zero exclusion.
package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

// TestFix12DangerousActionsMatchArabic seeds audit rows the way production
// actually writes them (Arabic "إلغاء فاتورة" for cancels) plus legacy
// English rows, and requires both the suspicious-only filter and the
// dangerous smart report to list them (SW-H07).
func TestFix12DangerousActionsMatchArabic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testAdmin()
	today := Today()
	_, err := s.DB.Exec(`INSERT INTO AuditLog(UserID,UserName,ActionType,ActionCategory,InvoiceNumber,Notes,LogDate)
		VALUES(1,'admin','إلغاء فاتورة','إلغاء',2,'إلغاء فاتورة 2',?),
		(1,'admin','إلغاء فاتورة','إلغاء',3,'إلغاء فاتورة 3',?),
		(1,'admin','cancel_invoice','sales',4,'legacy cancel',?),
		(1,'admin','delete_user','users',NULL,'حذف مستخدم',?),
		(1,'admin','إنشاء فاتورة','مبيعات',1,'بيع عادي',?)`,
		today+" 10:00:00", today+" 10:01:00", today+" 10:02:00", today+" 10:03:00", today+" 10:04:00")
	if err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	susp, err := s.ListAuditLog(ctx, admin, AuditFilter{From: today, To: today, SuspiciousOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(susp) != 4 { // 2 Arabic cancels + 1 English cancel + 1 delete_user; the plain sale is excluded
		t.Fatalf("suspicious rows = %d, want 4", len(susp))
	}

	rows, err := s.SmartReport(ctx, admin, "dangerous", today, today)
	if err != nil {
		t.Fatal(err)
	}
	byLabel := map[string]int64{}
	for _, r := range rows {
		byLabel[r.Label] = r.Count
	}
	if byLabel["إلغاء فاتورة"] != 2 {
		t.Fatalf("dangerous 'إلغاء فاتورة' count = %d, want 2 (rows %+v)", byLabel["إلغاء فاتورة"], rows)
	}
	if byLabel["cancel_invoice"] != 1 {
		t.Fatalf("dangerous 'cancel_invoice' count = %d, want 1 (legacy rows still match)", byLabel["cancel_invoice"])
	}
	if byLabel["delete_user"] != 1 {
		t.Fatalf("dangerous 'delete_user' count = %d, want 1", byLabel["delete_user"])
	}
}

// TestFix12VendorCashPaid pays a vendor 1000¢ in cash and requires the
// VendorCashPaid sub-bucket to count it while ExpectedBalance stays
// exactly opening − cash-out (SW-M06).
func TestFix12VendorCashPaid(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := testUser(t, s, "f12admin", "admin")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد الاختبار"})
	prod := testProduct(t, s, "زيت", money.Cents(1000), money.Cents(2000), money.Qty(0))
	_, _, err := s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid,
		Lines:    []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(10000), PurchasePrice: money.Cents(1000)}},
		Paid: 0, PaymentMethod: "Visa",
	})
	if err != nil {
		t.Fatalf("create purchase: %v", err)
	}
	shiftID := testOpenShift(t, s, admin, money.Cents(10000))
	if _, err := s.PayVendor(ctx, admin, vid, 0, money.Cents(1000), "Cash", ""); err != nil {
		t.Fatalf("PayVendor cash: %v", err)
	}
	sum, err := s.ShiftSummary(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.VendorCashPaid != 1000 {
		t.Fatalf("VendorCashPaid = %d, want 1000", sum.VendorCashPaid)
	}
	if sum.CashOut != 1000 {
		t.Fatalf("CashOut = %d, want 1000", sum.CashOut)
	}
	if sum.ExpectedBalance != 9000 {
		t.Fatalf("ExpectedBalance = %d, want 9000 (opening 10000 − cash-out 1000)", sum.ExpectedBalance)
	}
}

// TestFix12SaleAuditDetails makes a real sale (4 of 10 units sold — the
// sweep repro's shape, سكر 100 → 96 scaled down) and requires its
// "إنشاء فاتورة" audit entry to carry a detail line with the correct
// before/after quantities and prices (SW-M09).
func TestFix12SaleAuditDetails(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000, stock 10 units
	_ = b1OpenShiftFor(t, s, env.cashier)
	res := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 4000) // 4 units × 2000 = 8000
	var logID int64
	if err := s.DB.QueryRow(`SELECT LogID FROM AuditLog WHERE ActionType='إنشاء فاتورة' AND InvoiceID=?`,
		res.InvoiceID).Scan(&logID); err != nil {
		t.Fatalf("sale audit row: %v", err)
	}
	_, details, err := s.GetAuditLogDetails(env.ctx, env.admin, logID)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 {
		t.Fatalf("audit detail lines = %d, want 1", len(details))
	}
	d := details[0]
	if d.ProductName != "سكر" || d.ProductID != p.ID {
		t.Fatalf("detail product = %d %q, want %d سكر", d.ProductID, d.ProductName, p.ID)
	}
	if d.QuantityBefore != 10000 || d.QuantityAfter != 6000 || d.QuantityChange != -4000 {
		t.Fatalf("detail qty before/after/change = %d/%d/%d, want 10000/6000/-4000",
			d.QuantityBefore, d.QuantityAfter, d.QuantityChange)
	}
	if d.PriceBefore != 2000 || d.PriceAfter != 2000 {
		t.Fatalf("detail price before/after = %d/%d, want 2000/2000", d.PriceBefore, d.PriceAfter)
	}
	if d.LineTotal != 8000 {
		t.Fatalf("detail line total = %d, want 8000", d.LineTotal)
	}
}

// TestFix12SuspiciousUsersExcludesZero runs one real cancel (cashier's
// invoice, canceled by admin) and requires the suspicious_users smart
// report to list only the cashier — users with zero suspicious actions
// (admin, manager, supervisor) must not appear (SW-L07).
func TestFix12SuspiciousUsersExcludesZero(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر")
	_ = b1OpenShiftFor(t, s, env.cashier)
	res := b1CashCustomerSale(t, s, env, env.cashier, p.ID, 1000)
	if _, err := s.CancelInvoice(env.ctx, env.admin, res.InvoiceID, "اختبار"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	today := Today()
	rows, err := s.SmartReport(env.ctx, env.admin, "suspicious_users", today, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("suspicious_users rows = %+v, want exactly 1 (the cashier)", rows)
	}
	if rows[0].Label != env.cashier.UserName || rows[0].Count != 1 {
		t.Fatalf("suspicious_users row = %+v, want cashier with count 1", rows[0])
	}
}
