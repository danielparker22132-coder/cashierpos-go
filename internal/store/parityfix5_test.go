// Parity fix-5 tests: PR5-L1 (DailyClose CanceledCount still gated on
// CanceledAt), PR5-L2 (a cancel-refund row with a malformed/empty
// MovementDate is dropped by DailyClose while the shift summary counts
// it), PR5-L3 (RetainedCanceledCashTotal persisted but never read
// back).
package store

import (
	"testing"
	"time"
)

// PR5-L1: a canceled invoice with NULL CanceledAt must still be
// counted — the count derives the cancel day from the same sources as
// the money path (refund rows' effective day, else the sale date),
// not from CanceledAt alone. Shape: the PR4-L2 shape (cash sale
// 4×2000¢ → return 1×2000¢ cash → cancel 6000¢) with CanceledAt
// nulled the way a legacy row looks. Pre-fix CanceledCount was 0,
// want 1; the money tuple stays 0/0/0.
func TestParityFix5_CanceledCountNullCanceledAt(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	if _, err := e.s.DB.Exec(`UPDATE Invoices SET CanceledAt=NULL WHERE InvoiceID=?`, invID); err != nil {
		t.Fatalf("null CanceledAt: %v", err)
	}
	e.p2AssertSummaryDrawer(t, 10000)
	rep := e.p3DailyClose(t)
	if rep.CanceledCount != 1 {
		t.Fatalf("daily CanceledCount = %d, want 1 (NULL CanceledAt must not hide the canceled invoice)", rep.CanceledCount)
	}
	if rep.TotalCash != 0 || rep.CashReturns != 0 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 0/0/0",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
}

// PR5-L1 control: the same shape with a live CanceledAt still counts
// exactly 1 — the new count rule must not break the live case.
func TestParityFix5_CanceledCountLiveControl(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	rep := e.p3DailyClose(t)
	if rep.CanceledCount != 1 {
		t.Fatalf("daily CanceledCount = %d, want 1 (live CanceledAt)", rep.CanceledCount)
	}
}

// PR5-L1 partition: an invoice canceled on another day must not be
// counted today — each invoice counts on at most one day. The shape
// is backdated a day (sale, cancel and refund rows alike); today's
// DailyClose must see CanceledCount 0 and no money from it.
func TestParityFix5_CanceledCountOtherDayNotCounted(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	today, err := time.Parse("2006-01-02", Today())
	if err != nil {
		t.Fatalf("parse Today: %v", err)
	}
	yesterday := today.AddDate(0, 0, -1).Format("2006-01-02")
	day := yesterday + " 10:00:00"
	if _, err := e.s.DB.Exec(`UPDATE Invoices SET InvoiceDate=?, CanceledAt=? WHERE InvoiceID=?`,
		day, day, invID); err != nil {
		t.Fatalf("backdate invoice: %v", err)
	}
	if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=? WHERE ShiftID=?`, day, e.shiftID); err != nil {
		t.Fatalf("backdate cashbox: %v", err)
	}
	rep := e.p3DailyClose(t)
	if rep.CanceledCount != 0 {
		t.Fatalf("daily CanceledCount = %d, want 0 (invoice canceled yesterday)", rep.CanceledCount)
	}
	if rep.TotalCash != 0 || rep.CashReturns != 0 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 0/0/0",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
}

// PR5-L2 (structural row): a cancel-refund row whose MovementDate is
// malformed/empty must still be attributed to the invoice's cancel
// day (its CanceledAt, which is valid here) instead of being dropped
// by DailyClose. Shape: the PR4-L2 shape; pre-fix DailyClose said
// TotalCash 6000 / NetCash 6000 (refundToday 0 → cap 8000 → addBack
// 6000) while the summary said Expected 10000. Post-fix both agree:
// DailyClose 0/0/0, summary Expected 10000.
func TestParityFix5_DailyCloseMalformedMovementDate(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000)
		e.h1PreviewAndCancel(t, invID, 6000)
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='cancel_refund' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("MovementDate=%q: %v", bad, err)
		}
		e.p2AssertSummaryDrawer(t, 10000)
		rep := e.p3DailyClose(t)
		if rep.TotalCash != 0 || rep.CashReturns != 0 || rep.NetCash != 0 {
			t.Fatalf("MovementDate=%q: daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 0/0/0",
				bad, rep.TotalCash, rep.CashReturns, rep.NetCash)
		}
		if rep.CanceledCount != 1 {
			t.Fatalf("MovementDate=%q: daily CanceledCount = %d, want 1", bad, rep.CanceledCount)
		}
	}
}

// PR5-L2 (legacy row): the same fallback must apply to an orphaned
// legacy refund row. Shape: the PR4-L1 split (credit sale 6×2000¢,
// Paid 8000¢, collect 4000¢, return 3×2000¢ cash, cancel 6000¢ split
// into linked 4000¢ + orphan 2000¢) with the orphan's MovementDate
// set to 'garbage'. Pre-fix DailyClose dropped the orphan →
// CashReturns 2000¢, NetCash 2000¢; post-fix both reports agree:
// CashReturns 4000¢, NetCash 0.
func TestParityFix5_DailyCloseMalformedLegacyMovementDate(t *testing.T) {
	e := h1Setup(t)
	custID := testCustomer(t, e.s, "عميل آجل")
	invID := e.h1CreditSell(t, custID, 6, 8000) // net 12000, paid 8000
	e.p3Collect(t, custID, 4000)
	e.h1Return(t, invID, 3, "Cash", 6000)
	e.h1PreviewAndCancel(t, invID, 6000)
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
		e.admin.ID, e.admin.UserName, e.shiftID, 2000, reason, "garbage"); err != nil {
		t.Fatalf("insert orphan refund row: %v", err)
	}
	e.p2AssertSummaryDrawer(t, 10000)
	sum := e.p2Summary(t)
	if sum.CashReturns != 4000 {
		t.Fatalf("summary CashReturns = %d, want 4000", sum.CashReturns)
	}
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 4000 || rep.CashReturns != 4000 || rep.NetCash != 0 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 4000/4000/0",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
}

// PR5-L3: the stored RetainedCanceledCashTotal must round-trip
// through GetShift and ListShiftsByDate — the column is read back
// like its sibling stored totals, not write-only. Shape: the fix-4
// non-cash variant (cash sale 4×2000¢, return 1 unit via Visa,
// cancel refund 6000¢; drawer keeps 2000¢). After close, the stored
// bucket is 2000 and CashSales stays 0 in both reads.
func TestParityFix5_RetainedCanceledCashRoundTrips(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Visa", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	if _, err := e.s.CloseShift(e.ctx, e.admin, e.shiftID); err != nil {
		t.Fatalf("CloseShift: %v", err)
	}
	sh, err := e.s.GetShift(e.ctx, e.shiftID)
	if err != nil {
		t.Fatalf("GetShift: %v", err)
	}
	if sh.RetainedCanceledCashTotal != 2000 || sh.TotalSalesCash != 0 || sh.ExpectedBalance != 12000 {
		t.Fatalf("GetShift RetainedCanceledCashTotal/TotalSalesCash/ExpectedBalance = %d/%d/%d, want 2000/0/12000",
			sh.RetainedCanceledCashTotal, sh.TotalSalesCash, sh.ExpectedBalance)
	}
	shifts, err := e.s.ListShiftsByDate(e.ctx, Today())
	if err != nil {
		t.Fatalf("ListShiftsByDate: %v", err)
	}
	var found *Shift
	for _, s := range shifts {
		if s.ID == e.shiftID {
			found = s
		}
	}
	if found == nil {
		t.Fatalf("ListShiftsByDate: shift %d not listed", e.shiftID)
	}
	if found.RetainedCanceledCashTotal != 2000 || found.TotalSalesCash != 0 {
		t.Fatalf("ListShiftsByDate RetainedCanceledCashTotal/TotalSalesCash = %d/%d, want 2000/0",
			found.RetainedCanceledCashTotal, found.TotalSalesCash)
	}
}
