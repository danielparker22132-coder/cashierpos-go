package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

func TestCourierDebtRoles(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	cashier := testUser(t, s, "cash1", "cashier")
	sup := testUser(t, s, "sup1", "supervisor")

	// Cashier is READ-ONLY for create: refused.
	mustErr(t, func() error {
		_, err := s.CreateCourierDebt(ctx, cashier, CourierDebtInput{CourierName: "مندوب", Amount: money.Cents(1000)})
		return err
	}(), "cashier create courier debt")
	// Admin creates.
	id, err := s.CreateCourierDebt(ctx, admin, CourierDebtInput{CourierName: "مندوب أحمد", Amount: money.Cents(5000), OrderRef: "ORD-1", InvoiceNumber: 42})
	if err != nil {
		t.Fatalf("admin create courier debt: %v", err)
	}
	// Cashier CAN view the list.
	list, err := s.ListCourierDebts(ctx, cashier, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].CourierName != "مندوب أحمد" || list[0].InvoiceNumber.Int64 != 42 {
		t.Fatalf("list wrong: %+v", list)
	}
	// Cashier may NOT update / delete — but MAY settle (FIX-12 SW-H03:
	// jony's 2026-11-19 redesign — ANY cashier receiving cash clears
	// the debt, recorded as a COLLECTION on the receiver).
	mustErr(t, s.UpdateCourierDebt(ctx, cashier, id, CourierDebtInput{CourierName: "x", Amount: money.Cents(100)}), "cashier update courier debt")
	mustErr(t, s.DeleteCourierDebt(ctx, cashier, id), "cashier delete courier debt")
	rem, err := s.SettleCourierDebt(ctx, cashier, id, money.Cents(100), "Visa", "")
	if err != nil {
		t.Fatalf("cashier settle courier debt: %v", err)
	}
	if rem != money.Cents(4900) {
		t.Fatalf("remaining after cashier settle = %v, want 49.00 EGP", rem)
	}
	// Supervisor MAY act (edit_courier_debts).
	if err := s.UpdateCourierDebt(ctx, sup, id, CourierDebtInput{CourierName: "مندوب أحمد", Amount: money.Cents(6000), OrderRef: "ORD-1"}); err != nil {
		t.Fatalf("supervisor update: %v", err)
	}
	d, _ := s.GetCourierDebt(ctx, id)
	if d.Amount != money.Cents(6000) {
		t.Fatalf("amount = %v", d.Amount)
	}
}

func TestSettleCourierDebtCashIn(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := testUser(t, s, "admin1", "admin")
	id, _ := s.CreateCourierDebt(ctx, admin, CourierDebtInput{CourierName: "مندوب", Amount: money.Cents(10000), OrderRef: "ORD-9"})
	// Cash settle without a shift is refused.
	mustErr(t, func() error {
		_, err := s.SettleCourierDebt(ctx, admin, id, money.Cents(1000), "Cash", "")
		return err
	}(), "cash settle without shift")
	// Partial cash settle: money comes IN (courier hands cash over).
	testOpenShift(t, s, admin, money.Cents(0))
	rem, err := s.SettleCourierDebt(ctx, admin, id, money.Cents(4000), "Cash", "")
	if err != nil {
		t.Fatalf("partial settle: %v", err)
	}
	if rem != money.Cents(6000) {
		t.Fatalf("remaining = %v", rem)
	}
	var cb int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM CashBox WHERE MovementType='in' AND Amount=4000`).Scan(&cb)
	if cb != 1 {
		t.Fatalf("CashBox 'in' rows = %d, want 1 (courier hands cash over)", cb)
	}
	// Over-settle refused.
	mustErr(t, func() error {
		_, err := s.SettleCourierDebt(ctx, admin, id, money.Cents(6001), "Visa", "")
		return err
	}(), "settle over debt")
	// Full settle records PaidAt/PaidBy and Status paid.
	rem, err = s.SettleCourierDebt(ctx, admin, id, money.Cents(6000), "Visa", "")
	if err != nil {
		t.Fatalf("full settle: %v", err)
	}
	if rem != 0 {
		t.Fatalf("remaining = %v", rem)
	}
	d, _ := s.GetCourierDebt(ctx, id)
	if d.Status != "paid" || d.PaidAt == "" || d.PaidBy != "admin1" {
		t.Fatalf("paid debt wrong: %+v", d)
	}
	// A paid debt cannot be edited or deleted (history).
	mustErr(t, s.UpdateCourierDebt(ctx, admin, id, CourierDebtInput{CourierName: "مندوب", Amount: money.Cents(100)}), "edit paid debt")
	mustErr(t, s.DeleteCourierDebt(ctx, admin, id), "delete paid debt")
	// An open debt can be deleted.
	id2, _ := s.CreateCourierDebt(ctx, admin, CourierDebtInput{CourierName: "مندوب 2", Amount: money.Cents(100)})
	if err := s.DeleteCourierDebt(ctx, admin, id2); err != nil {
		t.Fatalf("delete open debt: %v", err)
	}
}
