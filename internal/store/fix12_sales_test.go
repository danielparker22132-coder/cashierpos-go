// Package store — FIX-12 lane B (sales money) regression tests.
//
// SW-H05: cash-customer paid validated before forcing to net.
// SW-H06: sales/profit reports + top products subtract return profit.
// SW-M05: cash-return refund gated on the invoice's drawer.
// SW-L06: shift-monitor performance keeps return/cancel-only users.
package store

import (
	"context"
	"strings"
	"testing"

	"cashierpos-go/internal/money"
)

func TestFix12_CashCustomerPaidRefused(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000
	b1OpenShiftFor(t, s, env.cashier)
	custID := testCustomer(t, s, "عميل آجل")

	// One unit: net 2000. Every wrong cash-customer amount is refused.
	for _, paid := range []money.Cents{500, 1000, 2500, -100} {
		_, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
			CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", Paid: paid,
			Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
		})
		if err == nil || !strings.Contains(err.Error(), "المبلغ المدفوع غير صحيح") {
			t.Fatalf("cash paid %d: want refusal المبلغ المدفوع غير صحيح, got %v", paid, err)
		}
	}
	// No invoice was created by the refusals.
	list, err := s.ListInvoices(env.ctx, env.admin, Today(), Today(), 0)
	if err != nil || len(list) != 0 {
		t.Fatalf("refused sales must not create invoices: list=%v err=%v", list, err)
	}
	// Exactly-net is accepted.
	res, err := s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", Paid: 2000,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err != nil {
		t.Fatalf("cash paid == net should succeed: %v", err)
	}
	if res.Paid != 2000 || res.Remaining != 0 || res.Net != 2000 {
		t.Fatalf("exact-net sale = %+v", res)
	}
	// paid == 0 keeps the S-19 auto-pay-full convention (the POS form
	// autofills paid with the net; API clients send 0 for "full").
	res, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash", Paid: 0,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err != nil {
		t.Fatalf("cash paid 0 (auto-pay full) should succeed: %v", err)
	}
	if res.Paid != 2000 || res.Remaining != 0 {
		t.Fatalf("auto-pay sale = %+v", res)
	}
	// Registered-customer behaviour is unchanged: overpay refused.
	_, err = s.CreateInvoice(env.ctx, env.cashier, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Cash", Paid: 2500,
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "المبلغ المدفوع غير صحيح") {
		t.Fatalf("registered overpay: want refusal, got %v", err)
	}
}

func TestFix12_ReportsSubtractReturnProfit(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	ctx := context.Background()
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000
	cashierShift := b1OpenShiftFor(t, s, env.cashier)
	_ = cashierShift
	// Supervisor's drawer is ample here on purpose, so the return
	// succeeds on pre-fix code too and the test isolates the report
	// profit (the thin-drawer return path is pinned by
	// TestFix12_ReturnUsesInvoiceDrawer).
	sup := env.sup
	if _, err := s.OpenShift(ctx, sup, 10000, false); err != nil {
		t.Fatalf("open supervisor shift: %v", err)
	}
	// 4× سكر cash: net 8000, profit 4×(2000−1000) = 4000.
	res, err := s.CreateInvoice(ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 4000}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	// Return 1 unit: refund 2000, return profit 2000−1000 = 1000.
	ret, err := s.ProcessReturn(ctx, sup, res.InvoiceID,
		[]ReturnLineInput{{ProductID: p.ID, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if ret.TotalRefund != 2000 || ret.CashRefund != 2000 {
		t.Fatalf("return = %+v, want refund/cash 2000", ret)
	}
	admin := testAdmin()
	// Sales report profit == DailyClose profit == 4000 − 1000 = 3000.
	sales, err := s.SalesReportFor(ctx, admin, Today(), Today())
	if err != nil {
		t.Fatalf("sales report: %v", err)
	}
	if sales.TotalProfit != 3000 {
		t.Fatalf("sales TotalProfit = %d, want 3000", sales.TotalProfit)
	}
	profit, err := s.ProfitReportFor(ctx, admin, Today(), Today(), "")
	if err != nil {
		t.Fatalf("profit report: %v", err)
	}
	if profit.TotalProfit != 3000 {
		t.Fatalf("profit TotalProfit = %d, want 3000", profit.TotalProfit)
	}
	if len(profit.BySaleType) != 1 || profit.BySaleType[0].TotalProfit != 3000 {
		t.Fatalf("profit by sale type = %+v, want retail 3000", profit.BySaleType)
	}
	dc, err := s.DailyClose(ctx, admin, Today())
	if err != nil {
		t.Fatalf("daily close: %v", err)
	}
	if dc.TotalProfit != 3000 {
		t.Fatalf("daily TotalProfit = %d, want 3000", dc.TotalProfit)
	}
	if sales.TotalProfit != dc.TotalProfit || profit.TotalProfit != dc.TotalProfit {
		t.Fatalf("reports disagree with daily close: sales %d profit %d daily %d",
			sales.TotalProfit, profit.TotalProfit, dc.TotalProfit)
	}
	// Top products: سكر profit 3000 (was 4000 before the fix).
	top, err := s.TopProducts(ctx, admin, Today(), Today(), 10, false)
	if err != nil {
		t.Fatalf("top products: %v", err)
	}
	if len(top) != 1 || top[0].Profit != 3000 {
		t.Fatalf("top products = %+v, want سكر profit 3000", top)
	}
}

func TestFix12_ReturnUsesInvoiceDrawer(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	ctx := context.Background()
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000
	// Cashier drawer: opening 10000 + cash sale 4000 = 14000.
	cashierShift := b1OpenShiftFor(t, s, env.cashier)
	// Supervisor drawer: thin, 1400 — below the 2000 refund.
	if _, err := s.OpenShift(ctx, env.sup, 1400, false); err != nil {
		t.Fatalf("open supervisor shift: %v", err)
	}
	res, err := s.CreateInvoice(ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	// The refund (2000) exceeds the returner's drawer (1400) but the
	// invoice's drawer (14000) covers it: the return must succeed.
	ret, err := s.ProcessReturn(ctx, env.sup, res.InvoiceID,
		[]ReturnLineInput{{ProductID: p.ID, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return on invoice drawer should succeed: %v", err)
	}
	if ret.CashRefund != 2000 {
		t.Fatalf("cash refund = %d, want 2000", ret.CashRefund)
	}
	// The CashBox leg lands on the invoice's (cashier's) shift, not
	// the returner's.
	var legShift int64
	if err := s.DB.QueryRow(`SELECT ShiftID FROM CashBox
		WHERE AutoKind='return' AND RefInvoiceID=?`, res.InvoiceID).Scan(&legShift); err != nil {
		t.Fatalf("return leg: %v", err)
	}
	if legShift != cashierShift {
		t.Fatalf("return leg shift = %d, want invoice shift %d", legShift, cashierShift)
	}
}

func TestFix12_ReturnRefusedWhenNeitherDrawerCovers(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	ctx := context.Background()
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000
	// Cashier drawer holds only its 500 opening: the sale is Visa, so
	// no cash enters it. Supervisor drawer holds 100.
	if _, err := s.OpenShift(ctx, env.cashier, 500, false); err != nil {
		t.Fatalf("open cashier shift: %v", err)
	}
	if _, err := s.OpenShift(ctx, env.sup, 100, false); err != nil {
		t.Fatalf("open supervisor shift: %v", err)
	}
	res, err := s.CreateInvoice(ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Visa",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("visa sale: %v", err)
	}
	// Cash refund 2000 exceeds both drawers (500 and 100): refused,
	// and the negative-forbidden rule is intact.
	_, err = s.ProcessReturn(ctx, env.sup, res.InvoiceID,
		[]ReturnLineInput{{ProductID: p.ID, Qty: 1000}}, "تالف", "Cash")
	if err == nil || !strings.Contains(err.Error(), "السالب ممنوع") {
		t.Fatalf("return with no covering drawer: want السالب ممنوع refusal, got %v", err)
	}
}

func TestFix12_ReturnFallsBackToReturnerDrawer(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	ctx := context.Background()
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000
	cashierShift := b1OpenShiftFor(t, s, env.cashier)
	supShift, err := s.OpenShift(ctx, env.sup, 5000, false)
	if err != nil {
		t.Fatalf("open supervisor shift: %v", err)
	}
	res, err := s.CreateInvoice(ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	// Close the invoice's shift: the refund must fall back to the
	// returner's open shift.
	if _, err := s.CloseShift(ctx, env.cashier, cashierShift); err != nil {
		t.Fatalf("close cashier shift: %v", err)
	}
	ret, err := s.ProcessReturn(ctx, env.sup, res.InvoiceID,
		[]ReturnLineInput{{ProductID: p.ID, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return with closed invoice shift should fall back: %v", err)
	}
	if ret.CashRefund != 2000 {
		t.Fatalf("cash refund = %d, want 2000", ret.CashRefund)
	}
	var legShift int64
	if err := s.DB.QueryRow(`SELECT ShiftID FROM CashBox
		WHERE AutoKind='return' AND RefInvoiceID=?`, res.InvoiceID).Scan(&legShift); err != nil {
		t.Fatalf("return leg: %v", err)
	}
	if legShift != supShift {
		t.Fatalf("fallback leg shift = %d, want returner shift %d", legShift, supShift)
	}
}

func TestFix12_PerformanceIncludesReturnOnlyUser(t *testing.T) {
	s := testStore(t)
	env := b1SaleEnvSetup(t, s)
	ctx := context.Background()
	p := b1MustProduct(t, s, env.admin, "سكر") // retail 2000, buy 1000
	b1OpenShiftFor(t, s, env.cashier)
	if _, err := s.OpenShift(ctx, env.sup, 10000, false); err != nil {
		t.Fatalf("open supervisor shift: %v", err)
	}
	res, err := s.CreateInvoice(ctx, env.cashier, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: p.ID, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	// Supervisor performs 2 returns (1 unit each, 2000¢) and never
	// sells; admin cancels the invoice afterwards.
	for i := 0; i < 2; i++ {
		if _, err := s.ProcessReturn(ctx, env.sup, res.InvoiceID,
			[]ReturnLineInput{{ProductID: p.ID, Qty: 1000}}, "تالف", "Cash"); err != nil {
			t.Fatalf("return %d: %v", i, err)
		}
	}
	admin := testAdmin()
	if _, err := s.CancelInvoice(ctx, admin, res.InvoiceID, "إلغاء تجريبي"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	m, err := s.ShiftMonitorFor(ctx, admin, Today(), Today())
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	byUser := map[string]CashierPerformance{}
	for _, row := range m.Performance {
		byUser[row.UserName] = row
	}
	// Return-only performer has a row with their counts attached.
	supRow, ok := byUser["b1sup2"]
	if !ok {
		t.Fatalf("supervisor has no performance row: %+v", m.Performance)
	}
	if supRow.ReturnCount != 2 || supRow.ReturnTotal != 4000 {
		t.Fatalf("supervisor row = %+v, want ReturnCount 2 ReturnTotal 4000", supRow)
	}
	if supRow.InvoiceCount != 0 || supRow.TotalSales != 0 {
		t.Fatalf("supervisor row = %+v, want 0 invoices / 0 sales", supRow)
	}
	// Returns are attributed to the performer, not the seller.
	if cashRow, ok := byUser[env.cashier.UserName]; !ok {
		t.Fatalf("cashier has no performance row: %+v", m.Performance)
	} else if cashRow.ReturnCount != 0 {
		t.Fatalf("cashier row = %+v, want ReturnCount 0 (returns belong to the performer)", cashRow)
	} else if cashRow.CanceledCount != 1 || cashRow.InvoiceCount != 0 {
		// All of the cashier's invoices were canceled: they still get
		// a row, carrying the cancel count and zero live sales.
		t.Fatalf("cashier row = %+v, want CanceledCount 1 / InvoiceCount 0", cashRow)
	}
}
