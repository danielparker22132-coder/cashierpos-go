// Parity fix-3 tests: PR3-H1 (same-shift cancel-after-return STILL
// overstates the drawer when the cancel refund is funded by debt
// collections), PR3-L2 (DailyClose legacy refund fallback), and the
// disclosed non-cash-return variant.
//
// PR3-H1 root cause: the fix-2 report claimed collections "cancel out
// of the correction" because they enter via CashDebtCollection. False
// whenever the refund exceeds the at-sale cash: the refund sums ALL
// Payments rows (collections included), so summary − drawer =
// refund − atSaleCash. The fix adds max(0, refund − atSaleCash) to
// CashOut in both the shift summary and DailyClose, and adds back the
// sale cash a non-cash return leaves in the drawer
// (max(0, (atSaleCash − refund) − legs) to RetainedCanceledCash in
// the shift summary / TotalCash in DailyClose — PR4-L3: the summary
// add-back is retained sale cash, not a sale, so it no longer rides
// on CashSales.)
// These tests drive the real sale → collect → return → cancel path
// and assert the SUMMARY buckets (not just preview/legs) — the
// collections pin fix-2's report claimed but never had (PR3-L1).
package store

import (
	"testing"

	"cashierpos-go/internal/money"
)

// p3Collect collects amount in cash from a credit customer.
func (e *h1Env) p3Collect(t *testing.T, custID int64, amount money.Cents) {
	t.Helper()
	if _, err := e.s.PayCustomerDebtGeneral(e.ctx, e.admin, custID, amount, "Cash", "سداد تجريبي"); err != nil {
		t.Fatalf("collect %d: %v", amount, err)
	}
}

// p3DailyClose fetches today's DailyClose for the h1 env.
func (e *h1Env) p3DailyClose(t *testing.T) *DailyCloseReport {
	t.Helper()
	rep, err := e.s.DailyClose(e.ctx, e.admin, Today())
	if err != nil {
		t.Fatalf("DailyClose: %v", err)
	}
	return rep
}

// PR3-H1 shape A (review): credit sale 2×2000¢, Paid 0 → collect
// 4000¢ cash → return 1×2000¢ cash → cancel (refund 2000¢).
// Pre-fix the summary said Expected 12000 (CashOut 0) while the true
// drawer is 10000; DailyClose NetCash was 2000 vs true 0.
func TestParityFix3_CollectionsShapeA_Summary(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 2, 0) // net 4000, paid 0
	e.p3Collect(t, custID, 4000)
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 2000)
	e.h1AssertLegs(t, 0, 2000, 2000)
	sum := e.p2Summary(t)
	if sum.CashSales != 0 || sum.CashDebtCollection != 4000 || sum.CashReturns != 2000 || sum.CashOut != 2000 {
		t.Fatalf("summary CashSales/CashDebtCollection/CashReturns/CashOut = %d/%d/%d/%d, want 0/4000/2000/2000",
			sum.CashSales, sum.CashDebtCollection, sum.CashReturns, sum.CashOut)
	}
	e.p2AssertSummaryDrawer(t, 10000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 4000 || rep.CashReturns != 2000 || rep.CancelRefundExcess != 2000 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/CancelRefundExcess/NetCash = %d/%d/%d/%d, want 4000/2000/2000/0",
			rep.TotalCash, rep.CashReturns, rep.CancelRefundExcess, rep.NetCash)
	}
}

// PR3-H1 shape B (review): credit sale 5×2000¢, 4000¢ cash at sale →
// collect 4000¢ cash → cancel, no return (refund 8000¢).
// Pre-fix the summary said Expected 14000 vs true drawer 10000.
func TestParityFix3_CollectionsShapeB_Summary(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 5, 4000) // net 10000, paid 4000
	e.p3Collect(t, custID, 4000)
	e.h1PreviewAndCancel(t, invID, 8000)
	e.h1AssertLegs(t, 4000, 0, 8000)
	sum := e.p2Summary(t)
	if sum.CashSales != 0 || sum.CashDebtCollection != 4000 || sum.CashReturns != 0 || sum.CashOut != 4000 {
		t.Fatalf("summary CashSales/CashDebtCollection/CashReturns/CashOut = %d/%d/%d/%d, want 0/4000/0/4000",
			sum.CashSales, sum.CashDebtCollection, sum.CashReturns, sum.CashOut)
	}
	e.p2AssertSummaryDrawer(t, 10000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 4000 || rep.CashReturns != 0 || rep.CancelRefundExcess != 4000 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/CancelRefundExcess/NetCash = %d/%d/%d/%d, want 4000/0/4000/0",
			rep.TotalCash, rep.CashReturns, rep.CancelRefundExcess, rep.NetCash)
	}
}

// PR3-H1 pure-credit variant: sale 5×2000¢, Paid 0 → collect 10000¢
// → cancel (refund 10000¢). Pre-fix Expected 20000 vs drawer 10000.
func TestParityFix3_PureCreditCollectThenCancel(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 5, 0) // net 10000, paid 0
	e.p3Collect(t, custID, 10000)
	e.h1PreviewAndCancel(t, invID, 10000)
	e.h1AssertLegs(t, 0, 0, 10000)
	sum := e.p2Summary(t)
	if sum.CashDebtCollection != 10000 || sum.CashOut != 10000 {
		t.Fatalf("summary CashDebtCollection/CashOut = %d/%d, want 10000/10000",
			sum.CashDebtCollection, sum.CashOut)
	}
	e.p2AssertSummaryDrawer(t, 10000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 10000 || rep.CancelRefundExcess != 10000 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CancelRefundExcess/NetCash = %d/%d/%d, want 10000/10000/0",
			rep.TotalCash, rep.CancelRefundExcess, rep.NetCash)
	}
}

// Non-cash-return variant (disclosed limitation, fixed in the same
// pass): cash sale 4×2000¢, return 1 unit via Visa (no CashBox leg),
// cancel (refund 6000¢). The drawer keeps 2000¢ of the sale cash;
// pre-fix the summary said Expected 10000 vs true drawer 12000.
func TestParityFix3_NonCashReturnVariant(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Visa", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	e.h1AssertLegs(t, 8000, 0, 6000)
	sum := e.p2Summary(t)
	if sum.CashSales != 0 || sum.RetainedCanceledCash != 2000 || sum.CashReturns != 0 || sum.CashOut != 0 {
		t.Fatalf("summary CashSales/RetainedCanceledCash/CashReturns/CashOut = %d/%d/%d/%d, want 0/2000/0/0 (add-back of retained sale cash is its own bucket, PR4-L3)",
			sum.CashSales, sum.RetainedCanceledCash, sum.CashReturns, sum.CashOut)
	}
	e.p2AssertSummaryDrawer(t, 12000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 2000 || rep.CashReturns != 0 || rep.NetCash != 2000 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 2000/0/2000",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
}

// The close must certify the TRUE drawer after the collections shape:
// CloseShift persists Expected = Actual = 10000, Difference 0, متوازن.
func TestParityFix3_CloseAfterCollectionsCancelCertifiesTrueDrawer(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 2, 0)
	e.p3Collect(t, custID, 4000)
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 2000)
	if _, err := e.s.CloseShift(e.ctx, e.admin, e.shiftID); err != nil {
		t.Fatalf("CloseShift: %v", err)
	}
	sh, err := e.s.GetShift(e.ctx, e.shiftID)
	if err != nil {
		t.Fatalf("GetShift: %v", err)
	}
	if sh.ExpectedBalance != 10000 || sh.ActualBalance != 10000 || sh.Difference != 0 || sh.DifferenceStatus != "متوازن" {
		t.Fatalf("closed shift Expected/Actual/Difference/Status = %d/%d/%d/%q, want 10000/10000/0/متوازن",
			sh.ExpectedBalance, sh.ActualBalance, sh.Difference, sh.DifferenceStatus)
	}
}

// PR3-L2: DailyClose's refundToday has no legacy-reason fallback — a
// refund row with RefInvoiceID=0 (invoice number only in the reason)
// was missed, inflating the cap to full at-sale cash and overstating
// NetCash by 4000. Shape (review): credit sale 6×2000¢, Paid 8000¢
// cash → collect 4000¢ → return 3×2000¢ cash → cancel (refund 6000¢);
// then the refund row is orphaned to RefInvoiceID=0.
func TestParityFix3_DailyCloseLegacyRefundFallback(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 6, 8000) // net 12000, paid 8000
	e.p3Collect(t, custID, 4000)
	e.h1Return(t, invID, 3, "Cash", 6000)
	e.h1PreviewAndCancel(t, invID, 6000)
	// Orphan the refund row the way a legacy row looks: structural
	// link gone, invoice number only in the reason.
	if _, err := e.s.DB.Exec(`UPDATE CashBox SET RefInvoiceID=0
		WHERE AutoKind='cancel_refund' AND RefInvoiceID=?`, invID); err != nil {
		t.Fatalf("orphan refund row: %v", err)
	}
	// The shift summary already had the fallback — it must stay right.
	e.p2AssertSummaryDrawer(t, 10000)
	sum := e.p2Summary(t)
	if sum.CashReturns != 4000 {
		t.Fatalf("summary CashReturns = %d, want 4000 (cap 8000-6000=2000 excludes 2000 of the 6000 legs)",
			sum.CashReturns)
	}
	// DailyClose must now find the refund through the reason too.
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 4000 || rep.CashReturns != 4000 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 4000/4000/0",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
}
