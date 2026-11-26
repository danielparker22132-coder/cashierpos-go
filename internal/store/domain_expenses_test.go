package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/money"
)

func TestRegisterExpense(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	expID, err := s.RegisterExpense(ctx, u, "كهرباء", money.Cents(1500), "تشغيل")
	if err != nil {
		t.Fatalf("expense: %v", err)
	}
	if expID == 0 {
		t.Fatal("expected an expense id")
	}
	// The expense reduces the expected drawer balance (H-09/E-02).
	sum, _ := s.ShiftSummary(ctx, id)
	if sum.ExpensesTotal != 1500 || sum.ExpectedBalance != 8500 {
		t.Fatalf("summary after expense wrong: %+v", sum)
	}
	// Validation.
	if _, err := s.RegisterExpense(ctx, u, "", money.Cents(100), "x"); err == nil {
		t.Fatal("expected missing description to be rejected")
	}
	if _, err := s.RegisterExpense(ctx, u, "x", money.Cents(0), "x"); err == nil {
		t.Fatal("expected zero amount to be rejected")
	}
	if _, err := s.RegisterExpense(ctx, u, "x", money.Cents(-5), "x"); err == nil {
		t.Fatal("expected negative amount to be rejected")
	}
}

func TestExpenseExceedingDrawerRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	if _, err := s.OpenShift(ctx, u, money.Cents(5000), false); err != nil {
		t.Fatalf("open: %v", err)
	}
	// More than the drawer holds: refused.
	if _, err := s.RegisterExpense(ctx, u, "كبير", money.Cents(5001), "تشغيل"); err == nil {
		t.Fatal("expected expense exceeding the drawer to be rejected")
	}
	// Exactly the balance: allowed.
	if _, err := s.RegisterExpense(ctx, u, "كل الرصيد", money.Cents(5000), "تشغيل"); err != nil {
		t.Fatalf("exact-balance expense must be allowed: %v", err)
	}
}

func TestExpenseRequiresOpenShift(t *testing.T) {
	s := testStore(t)
	u := b2TestUser(t, s, "cash1", "cashier")
	if _, err := s.RegisterExpense(context.Background(), u, "بلا شيفت", money.Cents(100), "x"); err == nil {
		t.Fatal("expected expense without an open shift to be rejected")
	}
}

func TestExpenseUpdateDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := b2TestUser(t, s, "cash1", "cashier")
	id, err := s.OpenShift(ctx, u, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	expID, err := s.RegisterExpense(ctx, u, "كهرباء", money.Cents(1000), "تشغيل")
	if err != nil {
		t.Fatalf("expense: %v", err)
	}
	// Description-only edit stays allowed.
	if err := s.UpdateExpense(ctx, u, expID, "كهرباء الشهر", money.Cents(1000), "تشغيل"); err != nil {
		t.Fatalf("description edit: %v", err)
	}
	// Increasing the amount beyond the drawer is refused.
	if err := s.UpdateExpense(ctx, u, expID, "كهرباء", money.Cents(20000), "تشغيل"); err == nil {
		t.Fatal("expected amount increase beyond the drawer to be rejected")
	}
	// A smaller increase is fine.
	if err := s.UpdateExpense(ctx, u, expID, "كهرباء", money.Cents(2000), "تشغيل"); err != nil {
		t.Fatalf("amount increase: %v", err)
	}
	e, err := s.GetExpense(ctx, expID)
	if err != nil || e.Amount != 2000 || e.Description != "كهرباء" {
		t.Fatalf("expense after update wrong: %+v %v", e, err)
	}
	// Delete works while the shift is open.
	if err := s.DeleteExpense(ctx, u, expID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetExpense(ctx, expID); err == nil {
		t.Fatal("expected deleted expense to be gone")
	}
	// After the shift closes, amount edits and deletes are frozen.
	expID2, err := s.RegisterExpense(ctx, u, "نظافة", money.Cents(500), "تشغيل")
	if err != nil {
		t.Fatalf("expense2: %v", err)
	}
	if _, err := s.CloseShift(ctx, u, 0); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = id
	if err := s.UpdateExpense(ctx, u, expID2, "نظافة", money.Cents(600), "تشغيل"); err == nil {
		t.Fatal("expected amount edit after close to be rejected")
	}
	// Description-only edit after close stays allowed.
	if err := s.UpdateExpense(ctx, u, expID2, "نظافة الشهر", money.Cents(500), "تشغيل"); err != nil {
		t.Fatalf("description edit after close must stay allowed: %v", err)
	}
	if err := s.DeleteExpense(ctx, u, expID2); err == nil {
		t.Fatal("expected delete after close to be rejected")
	}
}

func TestExpenseListVisibility(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u1 := b2TestUser(t, s, "cash1", "cashier")
	u2 := b2TestUser(t, s, "cash2", "cashier")
	admin := b2TestUser(t, s, "admin1", "admin")
	if _, err := s.OpenShift(ctx, u1, money.Cents(10000), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenShift(ctx, u2, money.Cents(10000), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterExpense(ctx, u1, "أ", money.Cents(100), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterExpense(ctx, u2, "ب", money.Cents(200), "x"); err != nil {
		t.Fatal(err)
	}
	own, err := s.ListExpenses(ctx, u1, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(own) != 1 || own[0].Description != "أ" {
		t.Fatalf("cashier should see only own expenses, got %+v", own)
	}
	all, err := s.ListExpenses(ctx, admin, "", "")
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("admin should see all expenses, got %d", len(all))
	}
}
