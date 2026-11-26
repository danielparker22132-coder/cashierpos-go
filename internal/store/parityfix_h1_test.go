// PR-H1 regression tests: cancel after a partial return must refund
// exactly paid − returned, never less.
//
// Root cause: ProcessReturn already posts the paid-out refund as a
// negative Payments row, so CancelCashRefundPreview's extra subtraction
// of Returns.TotalRefund counted every return twice (and over-subtracted
// the debt-absorbed part, which never reaches Payments at all). The fix
// sums Payments only. These tests drive the real sale → return → cancel
// path (CreateInvoice / ProcessReturn / CancelInvoice) and assert the
// preview, the cancel refund, and the CashBox legs per shape.
package store

import (
	"context"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/money"
)

// h1Env is one isolated store with an admin, an open shift (opening
// 10000¢) and one product: retail 2000¢, buy 1000¢, 10 units in stock.
type h1Env struct {
	s       *Store
	ctx     context.Context
	admin   *auth.User
	shiftID int64
	pid     int64
}

func h1Setup(t *testing.T) *h1Env {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	admin := b2TestUser(t, s, "h1admin", "admin")
	shiftID, err := s.OpenShift(ctx, admin, money.Cents(10000), false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000) // 10 units
	return &h1Env{s: s, ctx: ctx, admin: admin, shiftID: shiftID, pid: pid}
}

// h1Sell sells units of the product for cash (full payment) and returns
// the invoice id. One unit = 1000 milliunits, 2000¢ each.
func (e *h1Env) h1Sell(t *testing.T, units int64) int64 {
	t.Helper()
	res, err := e.s.CreateInvoice(e.ctx, e.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: e.pid, Qty: money.Qty(units * 1000)}},
	})
	if err != nil {
		t.Fatalf("sale %d units: %v", units, err)
	}
	if res.Net != money.Cents(units*2000) || res.Paid != money.Cents(units*2000) {
		t.Fatalf("sale %d units: net/paid = %d/%d, want %d/%d",
			units, res.Net, res.Paid, units*2000, units*2000)
	}
	return res.InvoiceID
}

// h1CreditSell sells units to a credit customer, paid partly in cash.
// Returns the invoice id.
func (e *h1Env) h1CreditSell(t *testing.T, custID int64, units int64, paid money.Cents) int64 {
	t.Helper()
	res, err := e.s.CreateInvoice(e.ctx, e.admin, CreateInvoiceInput{
		CustomerID: custID, SaleType: "retail", PaymentMethod: "Cash", Paid: paid,
		Lines: []SaleLineInput{{ProductID: e.pid, Qty: money.Qty(units * 1000)}},
	})
	if err != nil {
		t.Fatalf("credit sale %d units paid %d: %v", units, paid, err)
	}
	if res.Net != money.Cents(units*2000) || res.Paid != paid {
		t.Fatalf("credit sale: net/paid = %d/%d, want %d/%d",
			res.Net, res.Paid, units*2000, paid)
	}
	return res.InvoiceID
}

// h1Return returns units of one invoice via the given method and checks
// the refund the service reports.
func (e *h1Env) h1Return(t *testing.T, invID int64, units int64, method string, wantRefund money.Cents) {
	t.Helper()
	res, err := e.s.ProcessReturn(e.ctx, e.admin, invID,
		[]ReturnLineInput{{ProductID: e.pid, Qty: money.Qty(units * 1000)}}, "تالف", method)
	if err != nil {
		t.Fatalf("return %d units via %s: %v", units, method, err)
	}
	if res.TotalRefund != wantRefund {
		t.Fatalf("return %d units via %s: total refund = %d, want %d",
			units, method, res.TotalRefund, wantRefund)
	}
}

// h1CashBoxSums returns CashBox totals for the shift keyed by
// "MovementType/AutoKind" (e.g. "in/sale", "out/return",
// "out/cancel_refund").
func (e *h1Env) h1CashBoxSums(t *testing.T) map[string]money.Cents {
	t.Helper()
	rows, err := e.s.DB.Query(`SELECT MovementType, AutoKind, COALESCE(SUM(Amount),0)
		FROM CashBox WHERE ShiftID=? GROUP BY MovementType, AutoKind`, e.shiftID)
	if err != nil {
		t.Fatalf("cashbox sums: %v", err)
	}
	defer rows.Close()
	out := map[string]money.Cents{}
	for rows.Next() {
		var typ, kind string
		var sum money.Cents
		if err := rows.Scan(&typ, &kind, &sum); err != nil {
			t.Fatalf("cashbox scan: %v", err)
		}
		out[typ+"/"+kind] = sum
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("cashbox rows: %v", err)
	}
	return out
}

// h1PreviewAndCancel asserts the preview equals want, then cancels and
// asserts the cancel refund equals want too.
func (e *h1Env) h1PreviewAndCancel(t *testing.T, invID int64, want money.Cents) {
	t.Helper()
	preview, err := e.s.CancelCashRefundPreview(e.ctx, invID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview != want {
		t.Fatalf("preview = %d, want %d", preview, want)
	}
	refund, err := e.s.CancelInvoice(e.ctx, e.admin, invID, "إلغاء اختبار")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if refund != want {
		t.Fatalf("cancel refund = %d, want %d", refund, want)
	}
}

// h1AssertDrawerBackToOpening checks the real drawer from the CashBox
// legs themselves: opening + Σ(in) − Σ(out) must equal the opening
// balance, i.e. the sale/return/cancel legs net to zero. NOTE: this
// deliberately asserts from the legs, not from ShiftSummary — the
// summary-vs-legs agreement for these shapes is asserted separately
// in parityfix2_test.go (PR2-H1: the summary used to understate the
// drawer by the returned amount after a same-shift cancel; the
// return-leg cap in shiftSummaryTx fixed it).
func (e *h1Env) h1AssertDrawerBackToOpening(t *testing.T) {
	t.Helper()
	var net int64
	err := e.s.DB.QueryRow(`SELECT COALESCE(SUM(CASE WHEN MovementType='in' THEN Amount
		WHEN MovementType='out' THEN -Amount ELSE 0 END),0)
		FROM CashBox WHERE ShiftID=?`, e.shiftID).Scan(&net)
	if err != nil {
		t.Fatalf("drawer legs: %v", err)
	}
	if got := money.Cents(10000) + money.Cents(net); got != 10000 {
		t.Fatalf("drawer from CashBox legs = %d, want 10000 (net legs %d)", got, net)
	}
}
func (e *h1Env) h1AssertLegs(t *testing.T, paid, returned, refund money.Cents) {
	t.Helper()
	legs := e.h1CashBoxSums(t)
	if legs["in/sale"] != paid {
		t.Fatalf("cashbox in/sale = %d, want %d (legs %+v)", legs["in/sale"], paid, legs)
	}
	if legs["out/return"] != returned {
		t.Fatalf("cashbox out/return = %d, want %d (legs %+v)", legs["out/return"], returned, legs)
	}
	if legs["out/cancel_refund"] != refund {
		t.Fatalf("cashbox out/cancel_refund = %d, want %d (legs %+v)", legs["out/cancel_refund"], refund, legs)
	}
}

// Review shape 1: sell 4×2000¢, return 1×2000¢, cancel → 6000¢.
// (Before the fix the preview was 4000¢.)
func TestParityFixH1_Shape1_4x2000Return1(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	// CashBox legs: in 8000 / out 2000 (return) / out 6000 (cancel) —
	// out legs sum to the 8000 paid.
	e.h1AssertLegs(t, 8000, 2000, 6000)
	// Drawer back to its opening balance: 10000 + 8000 − 2000 − 6000.
	e.h1AssertDrawerBackToOpening(t)
}

// Review shape 2: sell 10×2000¢, return 3×2000¢, cancel → 14000¢.
// (Before the fix the preview was 8000¢.)
func TestParityFixH1_Shape2_10x2000Return3(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 10) // paid 20000
	e.h1Return(t, invID, 3, "Cash", 6000)
	e.h1PreviewAndCancel(t, invID, 14000)
	// CashBox legs: in 20000 / out 6000 (return) / out 14000 (cancel).
	e.h1AssertLegs(t, 20000, 6000, 14000)
	e.h1AssertDrawerBackToOpening(t)
}

// No-return control: cancel with no prior return refunds exactly what
// was paid — 5×2000¢ → 10000¢.
func TestParityFixH1_NoReturnControl(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 5) // paid 10000
	e.h1PreviewAndCancel(t, invID, 10000)
	e.h1AssertLegs(t, 10000, 0, 10000)
}

// Return method differs from sale method: cash sale, return paid out
// via Visa. The cash in the drawer is still 8000, but the customer
// already got 2000 back off-drawer, so the cancel cash refund is capped
// at the total net collected: min(8000, 6000) = 6000.
func TestParityFixH1_NonCashReturnMethod(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000 cash
	e.h1Return(t, invID, 1, "Visa", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	legs := e.h1CashBoxSums(t)
	if legs["in/sale"] != 8000 || legs["out/return"] != 0 || legs["out/cancel_refund"] != 6000 {
		t.Fatalf("cashbox legs = %+v, want in/sale 8000, out/return 0, out/cancel_refund 6000", legs)
	}
}

// Debt-absorbed return: credit sale 2×2000¢, paid 1000¢ cash, remaining
// 3000¢; returning 1 unit (2000¢) is absorbed by the debt — no cash
// leaves, no negative Payments row. Cancel must still refund the 1000¢
// cash actually collected. (Before the fix the preview was 0: the full
// 2000¢ TotalRefund was subtracted from the 1000¢ cash net.)
func TestParityFixH1_DebtAbsorbedReturn(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 2, 1000)
	res, err := e.s.ProcessReturn(e.ctx, e.admin, invID,
		[]ReturnLineInput{{ProductID: e.pid, Qty: 1000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.TotalRefund != 2000 || res.DebtReduction != 2000 || res.CashRefund != 0 {
		t.Fatalf("debt-absorbed return wrong: %+v", res)
	}
	e.h1PreviewAndCancel(t, invID, 1000)
	legs := e.h1CashBoxSums(t)
	if legs["in/sale"] != 1000 || legs["out/return"] != 0 || legs["out/cancel_refund"] != 1000 {
		t.Fatalf("cashbox legs = %+v, want in/sale 1000, out/return 0, out/cancel_refund 1000", legs)
	}
}

// Return exceeding the debt: credit sale 5×2000¢, paid 4000¢ cash,
// remaining 6000¢; returning 4 units (8000¢) cuts the debt by 6000¢ and
// pays out 2000¢ cash. Cancel must refund the 2000¢ net cash collected.
// (Before the fix the preview was 0.)
func TestParityFixH1_ReturnExceedingDebt(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 5, 4000)
	res, err := e.s.ProcessReturn(e.ctx, e.admin, invID,
		[]ReturnLineInput{{ProductID: e.pid, Qty: 4000}}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.TotalRefund != 8000 || res.DebtReduction != 6000 || res.CashRefund != 2000 {
		t.Fatalf("return exceeding debt wrong: %+v", res)
	}
	e.h1PreviewAndCancel(t, invID, 2000)
	// CashBox legs: in 4000 / out 2000 (return) / out 2000 (cancel) —
	// out legs sum to the 4000 cash paid.
	e.h1AssertLegs(t, 4000, 2000, 2000)
}

// Cash return on a non-cash sale: Visa sale 2×2000¢, return 1 unit in
// cash (2000¢ leaves the drawer). Net cash held is −2000¢, so the cancel
// cash refund floors at 0 — the remaining 2000¢ entitlement is non-cash
// and is not a CashBox leg.
func TestParityFixH1_CashReturnOnCardSale(t *testing.T) {
	e := h1Setup(t)
	res, err := e.s.CreateInvoice(e.ctx, e.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Visa", Paid: 4000,
		Lines: []SaleLineInput{{ProductID: e.pid, Qty: 2000}},
	})
	if err != nil {
		t.Fatalf("visa sale: %v", err)
	}
	invID := res.InvoiceID
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 0)
	legs := e.h1CashBoxSums(t)
	if legs["out/return"] != 2000 || legs["out/cancel_refund"] != 0 {
		t.Fatalf("cashbox legs = %+v, want out/return 2000, out/cancel_refund 0", legs)
	}
}
