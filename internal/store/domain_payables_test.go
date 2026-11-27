package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

func TestCreatePayableRoles(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	vid, _ := s.CreateVendor(ctx, admin, VendorInput{Name: "مورد المستحق"})

	// Admin creates a vendor payable.
	id, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "vendor", PartyID: vid, Amount: money.Cents(5000), DueDate: "2026-12-01", Description: "فاتورة كهرباء"})
	if err != nil {
		t.Fatalf("create payable: %v", err)
	}
	p, err := s.GetPayable(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.PartyName != "مورد المستحق" || p.Remaining != money.Cents(5000) || p.Status != "open" {
		t.Fatalf("payable wrong: %+v", p)
	}
	// Courier payable by name.
	if _, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب أحمد", Amount: money.Cents(3000), DueDate: "2026-12-02", Description: "توصيل"}); err != nil {
		t.Fatalf("courier payable: %v", err)
	}
	// Cashier may NOT create a payable.
	mustErr(t, func() error {
		_, err := s.CreatePayable(ctx, cashier, PayableInput{Kind: "vendor", PartyID: vid, Amount: money.Cents(100), DueDate: "2026-12-01", Description: "اختبار دور"})
		return err
	}(), "cashier create payable")
	// Validation: bad kind, zero amount, unknown vendor, missing
	// courier name, empty description.
	for name, in := range map[string]PayableInput{
		"bad kind":        {Kind: "other", Amount: money.Cents(100), DueDate: "2026-12-01", Description: "اختبار"},
		"zero amount":     {Kind: "vendor", PartyID: vid, Amount: 0, DueDate: "2026-12-01", Description: "اختبار"},
		"unknown vendor":  {Kind: "vendor", PartyID: 9999, Amount: money.Cents(100), DueDate: "2026-12-01", Description: "اختبار"},
		"no courier name": {Kind: "courier", Amount: money.Cents(100), DueDate: "2026-12-01", Description: "اختبار"},
		"no reason":       {Kind: "vendor", PartyID: vid, Amount: money.Cents(100), DueDate: "2026-12-01"},
	} {
		mustErr(t, func() error {
			_, err := s.CreatePayable(ctx, admin, in)
			return err
		}(), name)
	}
	// FIX-12 SW-M01: no due date is now ALLOWED (optional) — it used
	// to be refused. The payable is created with an empty due date and
	// never appears in the due list.
	noDueID, err := s.CreatePayable(ctx, admin, PayableInput{Kind: "vendor", PartyID: vid, Amount: money.Cents(100), Description: "بدون تاريخ"})
	if err != nil {
		t.Fatalf("create payable without due date: %v", err)
	}
	pNoDue, err := s.GetPayable(ctx, admin, noDueID)
	if err != nil {
		t.Fatal(err)
	}
	if pNoDue.DueDate != "" || pNoDue.Status != "open" {
		t.Fatalf("no-due payable wrong: %+v", pNoDue)
	}
}

func TestDuePayablesReminder(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	// Overdue (Nov 20), due soon (Dec 05, within 30 days of Nov 25), far (Jan 25).
	_, _ = s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "متأخر", Amount: money.Cents(1000), DueDate: "2026-11-20", Description: "اختبار"})
	_, _ = s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "قريب", Amount: money.Cents(2000), DueDate: "2026-12-05", Description: "اختبار"})
	_, _ = s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "بعيد", Amount: money.Cents(3000), DueDate: "2027-01-25", Description: "اختبار"})
	// The reminder is visible to the on-shift cashier too (no role gate on read).
	due, err := s.DuePayables(ctx, cashier)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("due payables = %d, want 2 (overdue + soon)", len(due))
	}
	if due[0].PartyName != "متأخر" || due[1].PartyName != "قريب" {
		t.Fatalf("due order wrong: %q %q", due[0].PartyName, due[1].PartyName)
	}
}

func TestSettlePayableByCashierAllowedAndRecorded(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	id, _ := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب", Amount: money.Cents(10000), DueDate: "2026-12-01", Description: "توصيل"})
	// Cashier settles partially with cash; drawer covers it.
	testOpenShift(t, s, cashier, money.Cents(20000))
	rem, err := s.SettlePayable(ctx, cashier, id, money.Cents(4000), "Cash", "")
	if err != nil {
		t.Fatalf("cashier settle: %v", err)
	}
	if rem != money.Cents(6000) {
		t.Fatalf("remaining = %v, want 60.00 EGP", rem)
	}
	p, _ := s.GetPayable(ctx, admin, id)
	if p.Status != "partial" {
		t.Fatalf("status = %q, want partial", p.Status)
	}
	// CashBox 'out' for the cash settlement.
	var cb int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE MovementType='out' AND Amount=4000`).Scan(&cb)
	if cb != 1 {
		t.Fatalf("CashBox 'out' rows = %d, want 1", cb)
	}
	// Audit records WHO settled (cashier username in the notes).
	var notes string
	_ = s.DB.QueryRow(`SELECT Notes FROM AuditLog WHERE ActionType='سداد مستحق يدوي'`).Scan(&notes)
	if notes == "" {
		t.Fatal("settle audit row missing")
	}
	// Full settlement by the cashier, non-cash (no drawer touch).
	rem, err = s.SettlePayable(ctx, cashier, id, money.Cents(6000), "Visa", "")
	if err != nil {
		t.Fatalf("cashier full settle: %v", err)
	}
	if rem != 0 {
		t.Fatalf("remaining = %v, want 0", rem)
	}
	p, _ = s.GetPayable(ctx, admin, id)
	if p.Status != "paid" || p.PaidAt == "" {
		t.Fatalf("after full settle: status %q paidAt %q", p.Status, p.PaidAt)
	}
	// Settling a paid payable is refused.
	mustErr(t, func() error {
		_, err := s.SettlePayable(ctx, cashier, id, money.Cents(100), "Visa", "")
		return err
	}(), "settle paid payable")
}

func TestSettlePayableGuards(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	sup := testUser(t, s, "sup1", "supervisor")
	id, _ := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب", Amount: money.Cents(5000), DueDate: "2026-12-01", Description: "توصيل"})
	// Supervisor may NOT settle (no settle_payable in the matrix).
	mustErr(t, func() error {
		_, err := s.SettlePayable(ctx, sup, id, money.Cents(100), "Visa", "")
		return err
	}(), "supervisor settle payable")
	// Over-remaining refused — remaining never goes below zero.
	mustErr(t, func() error {
		_, err := s.SettlePayable(ctx, admin, id, money.Cents(5001), "Visa", "")
		return err
	}(), "settle over remaining")
	// Cash settle over the drawer refused.
	testOpenShift(t, s, cashier, money.Cents(100)) // 1 EGP in drawer
	mustErr(t, func() error {
		_, err := s.SettlePayable(ctx, cashier, id, money.Cents(200), "Cash", "")
		return err
	}(), "cash settle over drawer")
	// Cash settle without any shift refused.
	id2, _ := s.CreatePayable(ctx, admin, PayableInput{Kind: "courier", PartyName: "مندوب 2", Amount: money.Cents(5000), DueDate: "2026-12-01", Description: "توصيل"})
	mustErr(t, func() error {
		_, err := s.SettlePayable(ctx, admin, id2, money.Cents(100), "Cash", "")
		return err
	}(), "cash settle without shift")
	// Balance unchanged after all refused attempts.
	p, _ := s.GetPayable(ctx, admin, id)
	if p.Remaining != money.Cents(5000) {
		t.Fatalf("remaining changed after refused settles: %v", p.Remaining)
	}
}
