// Parity fix-4 tests: PR4-L1 (refundTodayTx drops a coexisting legacy
// refund row), PR4-L2 (DailyClose corrections gated on CanceledAt),
// PR4-L3 (add-back booked into CashSales — dedicated
// RetainedCanceledCash bucket).
package store

import (
	"testing"
)

// PR4-L1: a structural cancel-refund row AND an orphaned legacy row
// for the same invoice must BOTH count in DailyClose (the shift
// summary already resolves per-row). Shape (review): credit sale
// 6×2000¢, Paid 8000¢ cash → collect the 4000¢ remainder (so the
// return below is fully cash) → return 3×2000¢ cash → cancel (refund
// 6000¢); the refund is then split into one linked row 4000¢ and one
// orphaned row 2000¢ (RefInvoiceID=0, invoice number only in the
// reason). Pre-fix DailyClose saw refund 4000¢ → cap 4000¢ →
// CashReturns 2000¢ → NetCash 2000¢; the summary said CashReturns
// 4000¢, NetCash 0. Post-fix both agree: CashReturns 4000¢, NetCash 0.
func TestParityFix4_RefundTodaySumsStructuralAndLegacy(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 6, 8000) // net 12000, paid 8000
	e.p3Collect(t, custID, 4000)
	e.h1Return(t, invID, 3, "Cash", 6000)
	e.h1PreviewAndCancel(t, invID, 6000)
	// Split the single 6000¢ refund row into linked 4000¢ + orphan
	// 2000¢, keeping the same reason / date / shift on both.
	var reason, movDate string
	if err := e.s.DB.QueryRow(`SELECT Reason, MovementDate FROM CashBox
		WHERE AutoKind='cancel_refund' AND RefInvoiceID=?`, invID).Scan(&reason, &movDate); err != nil {
		t.Fatalf("refund row: %v", err)
	}
	if _, err := e.s.DB.Exec(`UPDATE CashBox SET Amount=4000
		WHERE AutoKind='cancel_refund' AND RefInvoiceID=?`, invID); err != nil {
		t.Fatalf("split refund row: %v", err)
	}
	if _, err := e.s.DB.Exec(`INSERT INTO CashBox(UserID,UserName,ShiftID,MovementType,Amount,Reason,MovementDate,AutoKind,RefInvoiceID)
		VALUES(?,?,?,'out',?,?,?,'cancel_refund',0)`,
		e.admin.ID, e.admin.UserName, e.shiftID, 2000, reason, movDate); err != nil {
		t.Fatalf("insert orphan refund row: %v", err)
	}
	// The shift summary resolves per-row and must stay right.
	e.p2AssertSummaryDrawer(t, 10000)
	sum := e.p2Summary(t)
	if sum.CashReturns != 4000 {
		t.Fatalf("summary CashReturns = %d, want 4000 (cap 8000-6000=2000 excludes 2000 of the 6000 legs)",
			sum.CashReturns)
	}
	// DailyClose must now count the orphaned 2000¢ too.
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 4000 || rep.CashReturns != 4000 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 4000/4000/0",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
	if rep.TotalReturns != 6000 {
		t.Fatalf("daily TotalReturns = %d, want 6000 (return activity)", rep.TotalReturns)
	}
}

// PR4-L2: a canceled invoice with NULL CanceledAt must still get its
// same-day cancel refund counted — the refund rows' own MovementDate
// decides, not CanceledAt. Shape: cash sale 4×2000¢ → return 1×2000¢
// cash → cancel (refund 6000¢); then CanceledAt is nulled the way a
// legacy row looks. Pre-fix DailyClose computed refundToday 0 → cap
// 8000¢ → addBack 6000¢ → TotalCash 6000¢, NetCash 6000¢ (overstated
// by the refund). Post-fix: TotalCash 0, NetCash 0. The shift summary
// never used CanceledAt and must stay at Expected 10000.
func TestParityFix4_DailyCloseNullCanceledAt(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	if _, err := e.s.DB.Exec(`UPDATE Invoices SET CanceledAt=NULL WHERE InvoiceID=?`, invID); err != nil {
		t.Fatalf("null CanceledAt: %v", err)
	}
	e.p2AssertSummaryDrawer(t, 10000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 0 || rep.CashReturns != 0 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 0/0/0",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
	if rep.TotalReturns != 2000 {
		t.Fatalf("daily TotalReturns = %d, want 2000 (return activity)", rep.TotalReturns)
	}
}

// PR4-L3: the add-back is retained sale cash, not a sale — it gets its
// own RetainedCanceledCash bucket in the shift summary (and in the
// stored close totals) instead of riding on CashSales. Shape: the
// fix-3 non-cash variant (cash sale 4×2000¢, return 1 unit via Visa,
// cancel refund 6000¢; drawer keeps 2000¢). Money is unchanged
// (Expected 12000 = drawer); only the bucket attribution changes.
// DailyClose deliberately keeps the add-back inside TotalCash (a
// cash-in bucket, labeled "إجمالي النقدية" — review-4 found no wart
// there), so its tuple is unchanged: TotalCash 2000, NetCash 2000.
func TestParityFix4_RetainedCanceledCashBucket(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Visa", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	e.h1AssertLegs(t, 8000, 0, 6000)
	sum := e.p2Summary(t)
	if sum.CashSales != 0 || sum.RetainedCanceledCash != 2000 || sum.CashReturns != 0 || sum.CashOut != 0 {
		t.Fatalf("summary CashSales/RetainedCanceledCash/CashReturns/CashOut = %d/%d/%d/%d, want 0/2000/0/0",
			sum.CashSales, sum.RetainedCanceledCash, sum.CashReturns, sum.CashOut)
	}
	e.p2AssertSummaryDrawer(t, 12000)
	// Close persists the bucket separately — and CashSales stays 0.
	if _, err := e.s.CloseShift(e.ctx, e.admin, e.shiftID); err != nil {
		t.Fatalf("CloseShift: %v", err)
	}
	var retained, totalCash, cashTotal int64
	if err := e.s.DB.QueryRow(`SELECT RetainedCanceledCashTotal, TotalSalesCash, CashSalesTotal
		FROM Shifts WHERE ShiftID=?`, e.shiftID).Scan(&retained, &totalCash, &cashTotal); err != nil {
		t.Fatalf("stored totals: %v", err)
	}
	if retained != 2000 || totalCash != 0 || cashTotal != 0 {
		t.Fatalf("stored RetainedCanceledCashTotal/TotalSalesCash/CashSalesTotal = %d/%d/%d, want 2000/0/0",
			retained, totalCash, cashTotal)
	}
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 2000 || rep.CashReturns != 0 || rep.NetCash != 2000 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 2000/0/2000",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
}
