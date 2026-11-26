// Parity fix-7 tests: PR7-L1 (DailyClose still drops Returns rows with
// malformed/empty ReturnDate from TotalReturns and from the profit
// adjustment, while fix-6 counts the same return's CashBox leg in
// CashReturns — the same DailyClose page disagrees with itself).
//
// Root cause: fix-6 moved the CashBox legs to Go-side dating
// (returnLegDay) but left the Returns-table sums on the raw SQL date
// filter. A Returns row with a malformed ReturnDate dropped out of
// TotalReturns — and out of the profit adjustment, which subtracts
// refund − qty×buy per Returns row — while its CashBox leg counted in
// CashReturns. The fix dates Returns rows in Go the same way
// (returnsRowDay: own ReturnDate → shift day → sale date), for both
// the TotalReturns SUM and the retRows profit query.
package store

import (
	"testing"
	"time"
)

// PR7-L1 (review shape, non-canceled): cash sale 4×2000¢ → return
// 1×2000¢ cash with BOTH the leg's MovementDate and the Returns row's
// ReturnDate mangled. Pre-fix the same DailyClose page showed
// CashReturns 2000 vs TotalReturns 0 and TotalProfit 4000 vs true
// 3000 (sale profit 4×(2000−1000)=4000; the return should subtract
// 2000−1000=1000). Post-fix both halves agree: TotalCash 8000,
// CashReturns 2000, TotalReturns 2000, TotalProfit 3000, NetCash 6000,
// and the summary agrees (ReturnsTotal 2000, Expected 16000 = drawer).
func TestParityFix7_ReturnsMalformedReturnDate(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000)
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("ReturnDate=%q: leg: %v", bad, err)
		}
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
			WHERE InvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("ReturnDate=%q: row: %v", bad, err)
		}
		sum := e.p2Summary(t)
		if sum.ReturnsTotal != 2000 {
			t.Fatalf("ReturnDate=%q: summary ReturnsTotal = %d, want 2000", bad, sum.ReturnsTotal)
		}
		e.p2AssertSummaryDrawer(t, 16000)
		rep := e.p3DailyClose(t)
		if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
			rep.TotalProfit != 3000 || rep.NetCash != 6000 {
			t.Fatalf("ReturnDate=%q: daily TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
				bad, rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
		}
	}
}

// PR7-L1 (canceled variant): sell 4 → return 1 → cancel (refund
// 6000¢) with both dates mangled. The return's profit must still be
// skipped (its sale booked no profit), but TotalReturns must count
// the row: TotalCash 0, CashReturns 0, TotalReturns 2000,
// TotalProfit 0, NetCash 0, CanceledCount 1; summary drawer 10000.
func TestParityFix7_ReturnsMalformedReturnDateCanceled(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000)
		e.h1PreviewAndCancel(t, invID, 6000)
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("ReturnDate=%q: leg: %v", bad, err)
		}
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
			WHERE InvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("ReturnDate=%q: row: %v", bad, err)
		}
		e.p2AssertSummaryDrawer(t, 10000)
		rep := e.p3DailyClose(t)
		if rep.TotalCash != 0 || rep.CashReturns != 0 || rep.TotalReturns != 2000 ||
			rep.TotalProfit != 0 || rep.NetCash != 0 || rep.CanceledCount != 1 {
			t.Fatalf("ReturnDate=%q: daily TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash/CanceledCount = %d/%d/%d/%d/%d/%d, want 0/0/2000/0/0/1",
				bad, rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash, rep.CanceledCount)
		}
	}
}

// PR7-L1 control: the same shape with live (valid) dates must be
// unchanged by the rewrite — the Go-side dating must not move any
// money on live-shaped rows.
func TestParityFix7_ReturnsMalformedReturnDateLiveControl(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	sum := e.p2Summary(t)
	if sum.ReturnsTotal != 2000 {
		t.Fatalf("summary ReturnsTotal = %d, want 2000", sum.ReturnsTotal)
	}
	e.p2AssertSummaryDrawer(t, 16000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
		rep.TotalProfit != 3000 || rep.NetCash != 6000 {
		t.Fatalf("daily TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
			rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
	}
}

// PR7-L1 (multi-day, shift fallback): the invoice has returns on two
// days, so the mangled row must NOT take its sibling's day — it falls
// through to its own shift's date. Shape: sell 4 → return 1 today
// (ReturnDate mangled) → return 1 backdated to yesterday (its Returns
// row and its leg). Today: TotalReturns 2000 (today's return only),
// CashReturns 2000, TotalProfit 3000, NetCash 6000; the summary counts
// both returns (Expected 14000 = drawer).
func TestParityFix7_ReturnsMalformedMultiDayShiftFallback(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	e.h1Return(t, invID, 1, "Cash", 2000)
	today, err := time.Parse("2006-01-02", Today())
	if err != nil {
		t.Fatalf("parse Today: %v", err)
	}
	day := today.AddDate(0, 0, -1).Format("2006-01-02") + " 10:00:00"
	if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
		WHERE ReturnID=(SELECT MAX(ReturnID) FROM Returns WHERE InvoiceID=?)`, day, invID); err != nil {
		t.Fatalf("backdate return: %v", err)
	}
	if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
		WHERE CashBoxID=(SELECT MAX(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, day, invID); err != nil {
		t.Fatalf("backdate leg: %v", err)
	}
	if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate='garbage'
		WHERE ReturnID=(SELECT MIN(ReturnID) FROM Returns WHERE InvoiceID=?)`, invID); err != nil {
		t.Fatalf("mangle return: %v", err)
	}
	e.p2AssertSummaryDrawer(t, 14000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
		rep.TotalProfit != 3000 || rep.NetCash != 6000 {
		t.Fatalf("daily TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
			rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
	}
}

// PR7-L1 (last-resort fallback): every better day source is gone —
// the row's ReturnDate is mangled AND the row is orphaned from its
// shift — so the chain falls back to the invoice's sale date. This
// pins only the DailyClose fallback chain: the summary is NOT
// asserted here, because a row with ShiftID=0 belongs to no shift
// (the test orphaned it; the live writer always stamps a shift).
func TestParityFix7_ReturnsMalformedSaleDateFallback(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate='garbage', ShiftID=0
		WHERE InvoiceID=?`, invID); err != nil {
		t.Fatalf("mangle return: %v", err)
	}
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
		rep.TotalProfit != 3000 || rep.NetCash != 6000 {
		t.Fatalf("daily TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
			rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
	}
}
