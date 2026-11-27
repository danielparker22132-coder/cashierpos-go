package store

// FIX-12 lane C (customers / payables / courier / vendors) tests:
// SW-H01, SW-H02, SW-H03, SW-H04, SW-H08, SW-M01, SW-M02, SW-L01, SW-L03.

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

func TestFix12_H01_DeleteVendorBlockedByOpenPayable(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")

	vid, err := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد المستحق"})
	if err != nil {
		t.Fatal(err)
	}
	// Vendor with only an open 100.00 payable: no invoices, payments,
	// or batches — the old code deleted it and orphaned the payable.
	pid, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "vendor", PartyID: vid,
		Amount: money.Cents(10000), DueDate: "2026-12-20", Description: "اختبار حذف"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVendor(ctx, admin, vid); err == nil {
		t.Fatal("delete vendor with open payable: expected refusal, got nil")
	} else if !strings.Contains(err.Error(), "مستحقات مفتوحة") {
		t.Fatalf("delete refusal = %q, want it to name the open payables", err.Error())
	}
	// Vendor and payable both intact.
	if _, err := s.GetVendor(ctx, admin, vid); err != nil {
		t.Fatalf("vendor gone after refused delete: %v", err)
	}
	p, err := s.GetPayable(ctx, admin, pid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "open" || p.Remaining != money.Cents(10000) {
		t.Fatalf("payable changed by refused delete: %+v", p)
	}
	// A paid payable does NOT block the delete.
	if _, err := s.SettlePayable(ctx, admin, pid, money.Cents(10000), "Visa", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVendor(ctx, admin, vid); err != nil {
		t.Fatalf("delete vendor with only a paid payable: %v", err)
	}
	if _, err := s.GetVendor(ctx, admin, vid); err == nil {
		t.Fatal("vendor still exists after allowed delete")
	}
	// A vendor with no payables at all still deletes.
	vid2, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد فارغ"})
	if err := s.DeleteVendor(ctx, admin, vid2); err != nil {
		t.Fatalf("delete vendor with no payables: %v", err)
	}
}

func TestFix12_H02_CancelPayable(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")

	id, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب",
		Amount: money.Cents(10000), DueDate: "2026-12-01", Description: "توصيل"})
	if err != nil {
		t.Fatal(err)
	}
	// A reason is required.
	mustErr(t, s.CancelPayable(ctx, admin, id, ""), "cancel without reason")
	mustErr(t, s.CancelPayable(ctx, admin, id, "   "), "cancel with blank reason")
	// Cashier may not cancel (manage_payables is admin/manager only).
	mustErr(t, s.CancelPayable(ctx, cashier, id, "خطأ"), "cashier cancel payable")
	// No money moved by a refused cancel.
	var cbBefore int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox`).Scan(&cbBefore)

	// Cancel the open payable: status cancelled + audit row, no drawer touch.
	before := auditCount(t, s, "إلغاء مستحق يدوي")
	if err := s.CancelPayable(ctx, admin, id, "ألغي الطلب"); err != nil {
		t.Fatalf("cancel open payable: %v", err)
	}
	p, _ := s.GetPayable(ctx, admin, id)
	if p.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", p.Status)
	}
	if p.Remaining != money.Cents(10000) {
		t.Fatalf("remaining changed by cancel: %v (cancel moves no money)", p.Remaining)
	}
	if auditCount(t, s, "إلغاء مستحق يدوي") != before+1 {
		t.Fatal("cancel wrote no audit row")
	}
	var notes string
	_ = s.DB.QueryRow(`SELECT Notes FROM AuditLog WHERE ActionType='إلغاء مستحق يدوي'`).Scan(&notes)
	if !strings.Contains(notes, "ألغي الطلب") {
		t.Fatalf("audit row lacks the cancel reason: %q", notes)
	}
	var cbAfter int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox`).Scan(&cbAfter)
	if cbAfter != cbBefore {
		t.Fatalf("cancel touched CashBox (%d -> %d); cancel must not move money", cbBefore, cbAfter)
	}
	// Cancelled leaves the default list, shows under status=cancelled.
	list, err := s.ListPayables(ctx, admin, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range list {
		if q.ID == id {
			t.Fatal("cancelled payable still in the default list")
		}
	}
	cancelled, err := s.ListPayables(ctx, admin, "cancelled", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 1 || cancelled[0].ID != id {
		t.Fatalf("status=cancelled list = %v, want just payable %d", cancelled, id)
	}
	// Cancel again refused; settle refused.
	mustErr(t, s.CancelPayable(ctx, admin, id, "مرة ثانية"), "cancel already-cancelled")
	mustErr(t, func() error {
		_, err := s.SettlePayable(ctx, admin, id, money.Cents(100), "Visa", "")
		return err
	}(), "settle cancelled payable")
	// A paid payable is final: cancel refused.
	id2, _ := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب 2",
		Amount: money.Cents(5000), DueDate: "2026-12-01", Description: "توصيل"})
	if _, err := s.SettlePayable(ctx, admin, id2, money.Cents(5000), "Visa", ""); err != nil {
		t.Fatal(err)
	}
	mustErr(t, s.CancelPayable(ctx, admin, id2, "بعد السداد"), "cancel paid payable")
	// A partial payable CAN be cancelled.
	id3, _ := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب 3",
		Amount: money.Cents(5000), DueDate: "2026-12-01", Description: "توصيل"})
	if _, err := s.SettlePayable(ctx, admin, id3, money.Cents(2000), "Visa", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelPayable(ctx, admin, id3, "جزئي ملغى"); err != nil {
		t.Fatalf("cancel partial payable: %v", err)
	}
}

func TestFix12_H03_CashierSettlesCourierDebtCollection(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")

	id, err := s.CreateCourierDebt(ctx, admin, CourierDebtInput{CourierName: "مندوب أحمد", Amount: money.Cents(10000), OrderRef: "ORD-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Cashier settles with cash: needs an open shift (money comes IN).
	testOpenShift(t, s, cashier, money.Cents(0))
	rem, err := s.SettleCourierDebt(ctx, cashier, id, money.Cents(4000), "Cash", "")
	if err != nil {
		t.Fatalf("cashier settle courier debt: %v", err)
	}
	if rem != money.Cents(6000) {
		t.Fatalf("remaining = %v, want 60.00 EGP", rem)
	}
	// Recorded as a COLLECTION on the receiving cashier.
	var cbUser, cbKind string
	var cbAmt int64
	if err := s.DB.QueryRow(`SELECT UserName,AutoKind,Amount FROM CashBox WHERE MovementType='in' AND Reason LIKE 'سداد دين مندوب%'`).
		Scan(&cbUser, &cbKind, &cbAmt); err != nil {
		t.Fatalf("courier collection CashBox row: %v", err)
	}
	if cbUser != "cash1" || cbKind != "courier_collection" || cbAmt != 4000 {
		t.Fatalf("collection row = user %q kind %q amount %d, want cash1/courier_collection/4000", cbUser, cbKind, cbAmt)
	}
	// Audit names courier + receiver.
	var notes string
	_ = s.DB.QueryRow(`SELECT Notes FROM AuditLog WHERE ActionType='سداد دين مندوب'`).Scan(&notes)
	if !strings.Contains(notes, "مندوب أحمد") || !strings.Contains(notes, "cash1") {
		t.Fatalf("settle audit lacks courier/receiver: %q", notes)
	}
	// Cashier full settle (non-cash needs no shift): debt cleared.
	rem, err = s.SettleCourierDebt(ctx, cashier, id, money.Cents(6000), "Visa", "")
	if err != nil {
		t.Fatalf("cashier full settle: %v", err)
	}
	if rem != 0 {
		t.Fatalf("remaining = %v, want 0", rem)
	}
	d, _ := s.GetCourierDebt(ctx, id)
	if d.Status != "paid" || d.PaidBy != "cash1" {
		t.Fatalf("paid debt wrong: %+v", d)
	}
	// Cashier still cannot create / edit / delete a courier debt.
	mustErr(t, func() error {
		_, err := s.CreateCourierDebt(ctx, cashier, CourierDebtInput{CourierName: "x", Amount: money.Cents(100)})
		return err
	}(), "cashier create courier debt")
	mustErr(t, s.UpdateCourierDebt(ctx, cashier, id, CourierDebtInput{CourierName: "x", Amount: money.Cents(100)}), "cashier update courier debt")
	mustErr(t, s.DeleteCourierDebt(ctx, cashier, id), "cashier delete courier debt")
}

func TestFix12_H04_BookCourierDebtForOrderIdempotent(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")

	// First booking creates the debt for the order amount.
	id1, err := s.BookCourierDebtForOrder(ctx, admin, 7, "مندوب التوصيل", money.Cents(9000))
	if err != nil {
		t.Fatalf("book delivery debt: %v", err)
	}
	d, err := s.GetCourierDebt(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if d.CourierName != "مندوب التوصيل" || d.Amount != money.Cents(9000) || d.Status != "open" {
		t.Fatalf("booked debt wrong: %+v", d)
	}
	if d.OrderRef != "ONLINE-7" {
		t.Fatalf("OrderRef = %q, want ONLINE-7", d.OrderRef)
	}
	// Confirming the same delivery again books nothing new.
	id2, err := s.BookCourierDebtForOrder(ctx, admin, 7, "مندوب التوصيل", money.Cents(9000))
	if err != nil {
		t.Fatalf("second booking: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("second booking id = %d, want the existing debt %d", id2, id1)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CourierDebts WHERE OrderRef='ONLINE-7'`).Scan(&n)
	if n != 1 {
		t.Fatalf("debts for order 7 = %d, want exactly 1", n)
	}
	// A different order books its own debt; validation still applies.
	id3, err := s.BookCourierDebtForOrder(ctx, admin, 8, "مندوب آخر", money.Cents(5000))
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Fatal("different order reused the first debt id")
	}
	mustErr(t, func() error {
		_, err := s.BookCourierDebtForOrder(ctx, admin, 9, "", money.Cents(100))
		return err
	}(), "book without courier name")
	mustErr(t, func() error {
		_, err := s.BookCourierDebtForOrder(ctx, admin, 9, "مندوب", money.Cents(0))
		return err
	}(), "book zero amount")
}

func TestFix12_H08_PayablesViewWarehouseDenied(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	sup := testUser(t, s, "sup1", "supervisor")
	cashier := testUser(t, s, "cash1", "cashier")
	wh := testUser(t, s, "wh1", "warehouse")

	if _, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب",
		Amount: money.Cents(1000), DueDate: "2026-12-01", Description: "اختبار"}); err != nil {
		t.Fatal(err)
	}
	// Warehouse is denied on both read paths (§15 row 79).
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"ListPayables", func() error { _, err := s.ListPayables(ctx, wh, "", ""); return err }},
		{"DuePayables", func() error { _, err := s.DuePayables(ctx, wh); return err }},
	} {
		err := call.fn()
		if err == nil {
			t.Fatalf("%s as warehouse: expected denial, got nil", call.name)
		}
		if !strings.Contains(err.Error(), "غير مسموح") {
			t.Fatalf("%s as warehouse: error %q, want the uniform denial", call.name, err.Error())
		}
	}
	// Admin / supervisor / cashier keep access.
	if _, err := s.ListPayables(ctx, admin, "", ""); err != nil {
		t.Fatalf("admin ListPayables: %v", err)
	}
	if _, err := s.ListPayables(ctx, sup, "", ""); err != nil {
		t.Fatalf("supervisor ListPayables: %v", err)
	}
	if _, err := s.ListPayables(ctx, cashier, "", ""); err != nil {
		t.Fatalf("cashier ListPayables: %v", err)
	}
	if _, err := s.DuePayables(ctx, cashier); err != nil {
		t.Fatalf("cashier DuePayables: %v", err)
	}
}

func TestFix12_M01M02_PayableDueDateOptionalReasonRequired(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")

	// SW-M01: no due date is allowed; the payable never shows as due.
	id, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب",
		Amount: money.Cents(1000), Description: "بدون تاريخ"})
	if err != nil {
		t.Fatalf("create payable without due date: %v", err)
	}
	p, _ := s.GetPayable(ctx, admin, id)
	if p.DueDate != "" {
		t.Fatalf("DueDate = %q, want empty", p.DueDate)
	}
	due, err := s.DuePayables(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range due {
		if q.ID == id {
			t.Fatal("due-date-less payable appears in the due list")
		}
	}
	// A malformed due date is still refused.
	mustErr(t, func() error {
		_, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب",
			Amount: money.Cents(100), DueDate: "01/12/2026", Description: "تاريخ غلط"})
		return err
	}(), "malformed due date")
	// SW-M02: an empty reason is refused.
	mustErr(t, func() error {
		_, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب",
			Amount: money.Cents(100), DueDate: "2026-12-01", Description: ""})
		return err
	}(), "empty description")
	mustErr(t, func() error {
		_, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب",
			Amount: money.Cents(100), DueDate: "2026-12-01", Description: "   "})
		return err
	}(), "blank description")
}

func TestFix12_L01_VendorPayOneQirshTolerance(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد السداد"})
	prod := testProduct(t, s, "زيت", money.Cents(1000), money.Cents(2000), money.Qty(0))
	_, _, err = s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid,
		Lines:    []PurchaseLineInput{{ProductID: prod, Qty: money.Qty(10000), PurchasePrice: money.Cents(1000)}},
		Paid:     0, PaymentMethod: "Visa",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Balance is exactly 100.00.
	// Balance + 0.01 is allowed; the 1-cent excess is absorbed and the
	// balance lands exactly at 0, never negative.
	after, err := s.PayVendor(ctx, admin, vid, 0, money.Cents(10001), "Visa", "")
	if err != nil {
		t.Fatalf("pay balance + 0.01: %v", err)
	}
	if after != 0 {
		t.Fatalf("balance after = %v, want 0 (tolerance absorbed)", after)
	}
	v, _ := s.GetVendor(ctx, admin, vid)
	if v.CurrentBalance != 0 {
		t.Fatalf("vendor balance = %v, want 0 — never negative", v.CurrentBalance)
	}
	// Balance + 0.02 is still refused.
	vid2, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد السداد 2"})
	prod2 := testProduct(t, s, "سكر", money.Cents(1000), money.Cents(2000), money.Qty(0))
	_, _, err = s.CreatePurchase(ctx, admin, PurchaseInput{
		VendorID: vid2,
		Lines:    []PurchaseLineInput{{ProductID: prod2, Qty: money.Qty(10000), PurchasePrice: money.Cents(1000)}},
		Paid:     0, PaymentMethod: "Visa",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustErr(t, func() error {
		_, err := s.PayVendor(ctx, admin, vid2, 0, money.Cents(10002), "Visa", "")
		return err
	}(), "pay balance + 0.02")
	v2, _ := s.GetVendor(ctx, admin, vid2)
	if v2.CurrentBalance != money.Cents(10000) {
		t.Fatalf("balance changed after refused overpay: %v", v2.CurrentBalance)
	}
}

func TestFix12_L03_DebtorsShowDateOnly(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCustomer(ctx, admin, CustomerInput{Name: "مدين"})
	// Invoice dated with a full datetime; payment stamps Now() too.
	b3Invoice(t, s, id, 1, 10000, 0, 10000, "2026-11-22 04:01:28")
	if _, err := s.PayCustomerDebtGeneral(ctx, admin, id, money.Cents(2000), "Visa", ""); err != nil {
		t.Fatal(err)
	}
	debtors, err := s.ListDebtors(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(debtors) != 1 {
		t.Fatalf("debtors = %d, want 1", len(debtors))
	}
	d := debtors[0]
	if d.LastPurchase != "2026-11-22" {
		t.Fatalf("LastPurchase = %q, want date only 2026-11-22", d.LastPurchase)
	}
	if d.LastPayment == "" || len(d.LastPayment) != 10 || strings.Contains(d.LastPayment, " ") {
		t.Fatalf("LastPayment = %q, want a date only (YYYY-MM-DD)", d.LastPayment)
	}
}
