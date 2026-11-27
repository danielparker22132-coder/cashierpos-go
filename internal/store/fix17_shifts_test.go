// Package store — FIX-17 lane C regression tests.
//
// SW2-L01: TopProducts qty/revenue must be NET of returns, on the same
// basis as the (already net) profit.
// Expense-delete ruling (parent, 2026-11-26): deleting an expense needs
// the create perm AND (own expense OR Manager/Admin).
package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

// SW2-L01: sale of 4 units + return of 1 must read qty 3 / revenue 45.00 /
// profit 15.00 — the sweep repro was qty 4 / revenue 60.00 beside a net
// profit of 15.00 (mixed gross/net basis).
func TestFix17_TopProductsNetOfReturns(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	ctx := context.Background()
	// سكر: retail 1500¢, buy 1000¢, profit 500¢/unit.
	pid := testProductB2(t, s, "سكر", 1000, 1500, 100000)
	b1OpenShiftFor(t, s, env.cashier)
	if _, err := s.OpenShift(ctx, env.sup, 10000, false); err != nil {
		t.Fatalf("open supervisor shift: %v", err)
	}
	res, err := s.CreateInvoice(ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: 4000}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	ret, err := s.ProcessReturn(ctx, env.sup, res.InvoiceID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 1500 {
		t.Fatalf("return refund = %d, want 1500", ret.TotalRefund)
	}
	admin := testAdmin()
	top, err := s.TopProducts(ctx, admin, Today(), Today(), 10, false)
	if err != nil {
		t.Fatalf("top products: %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("top = %d rows, want 1", len(top))
	}
	row := top[0]
	if row.Qty != 3000 || row.Revenue != 4500 || row.Profit != 1500 {
		t.Fatalf("top row = qty %d revenue %d profit %d, want qty 3000 revenue 4500 profit 1500 (net basis)",
			row.Qty, row.Revenue, row.Profit)
	}
}

// Expense-delete ruling: a non-manager may delete only their own expense;
// Manager/Admin may delete any expense; a denied delete leaves the row
// intact.
func TestFix17_DeleteExpenseOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "fx17admin", "admin")
	manager := b2TestUser(t, s, "fx17mgr", "manager")
	cash1 := b2TestUser(t, s, "fx17cash1", "cashier")
	cash2 := b2TestUser(t, s, "fx17cash2", "cashier")
	sup := b2TestUser(t, s, "fx17sup", "supervisor")
	// Each actor needs an open shift to register an expense.
	if _, err := s.OpenShift(ctx, admin, money.Cents(10000), false); err != nil {
		t.Fatalf("open admin shift: %v", err)
	}
	if _, err := s.OpenShift(ctx, cash1, money.Cents(10000), false); err != nil {
		t.Fatalf("open cash1 shift: %v", err)
	}
	if _, err := s.OpenShift(ctx, cash2, money.Cents(10000), false); err != nil {
		t.Fatalf("open cash2 shift: %v", err)
	}
	adminExp, err := s.RegisterExpense(ctx, admin, "مصروف المدير", money.Cents(1000), "تشغيل")
	if err != nil {
		t.Fatalf("admin expense: %v", err)
	}
	// Cashier deleting another user's (admin's) expense: refused.
	if err := s.DeleteExpense(ctx, cash1, adminExp); err == nil || !strings.Contains(err.Error(), "غير مسموح") {
		t.Fatalf("cashier deleting admin expense: want denial غير مسموح, got %v", err)
	}
	if _, err := s.GetExpense(ctx, adminExp); err != nil {
		t.Fatalf("denied delete must leave the row intact: %v", err)
	}
	// Supervisor deleting another user's expense: refused too.
	if err := s.DeleteExpense(ctx, sup, adminExp); err == nil || !strings.Contains(err.Error(), "غير مسموح") {
		t.Fatalf("supervisor deleting admin expense: want denial غير مسموح, got %v", err)
	}
	// Cashier deleting their OWN expense: allowed.
	ownExp, err := s.RegisterExpense(ctx, cash1, "مصروف الكاشير", money.Cents(500), "تشغيل")
	if err != nil {
		t.Fatalf("cash1 expense: %v", err)
	}
	if err := s.DeleteExpense(ctx, cash1, ownExp); err != nil {
		t.Fatalf("cashier deleting own expense must work: %v", err)
	}
	if _, err := s.GetExpense(ctx, ownExp); err == nil {
		t.Fatal("deleted own expense must be gone")
	}
	// Manager may delete any expense.
	if err := s.DeleteExpense(ctx, manager, adminExp); err != nil {
		t.Fatalf("manager deleting admin expense must work: %v", err)
	}
	if _, err := s.GetExpense(ctx, adminExp); err == nil {
		t.Fatal("manager-deleted expense must be gone")
	}
	// Admin may delete any expense (cash2's).
	cash2Exp, err := s.RegisterExpense(ctx, cash2, "مصروف كاشير2", money.Cents(700), "تشغيل")
	if err != nil {
		t.Fatalf("cash2 expense: %v", err)
	}
	if err := s.DeleteExpense(ctx, admin, cash2Exp); err != nil {
		t.Fatalf("admin deleting cashier expense must work: %v", err)
	}
}
