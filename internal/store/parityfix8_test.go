// Parity fix-8 tests: PR8-L1 (PR7-L1 is incomplete: Returns-row dating
// and CashBox-leg dating still diverge in the multi-day mixed-mangle
// shape). The leg's sibling step queries ALL Returns rows of the
// invoice, so with today's leg AND row both mangled and a valid sibling
// return on another day, the single distinct parseable sibling day is
// the OTHER day and the leg is attributed there — while the row takes
// its own shift day. Today CashReturns 0 vs TotalReturns 2000,
// NetCash 8000 vs true 6000; yesterday CashReturns 4000 vs
// TotalReturns 2000, NetCash -4000 vs true -2000.
//
// The fix restricts the sibling query to the leg's own shift (the
// writer stamps the identical ShiftID on the Returns row and the
// CashBox leg of every return event) AND applies the sibling step
// only when the same-shift set is complete: exactly one distinct
// parseable day and no unparseable row. A lone parseable day from an
// incomplete set proves nothing about this leg — the unparseable row
// might be the leg's own row on a different day — so the leg falls
// through to its shift day instead.
package store

import (
	"testing"
	"time"
)

// PR8-L1 (shape D): sell 4×2000¢ today → return 1×2000¢ cash today
// (leg AND row mangled) → return 1×2000¢ cash backdated to yesterday
// (its Returns row AND its leg). Post-fix both halves agree on both
// days: today TotalCash 8000 / CashReturns 2000 / TotalReturns 2000 /
// TotalProfit 3000 / NetCash 6000; yesterday TotalCash 0 /
// CashReturns 2000 / TotalReturns 2000 / TotalProfit -1000 /
// NetCash -2000. The summary counts both returns (Expected 14000 =
// drawer).
func TestParityFix8_ReturnLegMultiDayMixedMangle(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000) // today
		e.h1Return(t, invID, 1, "Cash", 2000) // backdated
		today, err := time.Parse("2006-01-02", Today())
		if err != nil {
			t.Fatalf("parse Today: %v", err)
		}
		yesterday := today.AddDate(0, 0, -1).Format("2006-01-02")
		day := yesterday + " 10:00:00"
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
			WHERE ReturnID=(SELECT MAX(ReturnID) FROM Returns WHERE InvoiceID=?)`, day, invID); err != nil {
			t.Fatalf("backdate return: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE CashBoxID=(SELECT MAX(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, day, invID); err != nil {
			t.Fatalf("backdate leg: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE CashBoxID=(SELECT MIN(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, bad, invID); err != nil {
			t.Fatalf("mangle leg: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
			WHERE ReturnID=(SELECT MIN(ReturnID) FROM Returns WHERE InvoiceID=?)`, bad, invID); err != nil {
			t.Fatalf("mangle return: %v", err)
		}
		sum := e.p2Summary(t)
		if sum.ReturnsTotal != 4000 {
			t.Fatalf("bad=%q: summary ReturnsTotal = %d, want 4000", bad, sum.ReturnsTotal)
		}
		e.p2AssertSummaryDrawer(t, 14000)
		rep := e.p3DailyClose(t)
		if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
			rep.TotalProfit != 3000 || rep.NetCash != 6000 {
			t.Fatalf("bad=%q: today TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
				bad, rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
		}
		yrep, err := e.s.DailyClose(e.ctx, e.admin, yesterday)
		if err != nil {
			t.Fatalf("bad=%q: DailyClose yesterday: %v", bad, err)
		}
		if yrep.TotalCash != 0 || yrep.CashReturns != 2000 || yrep.TotalReturns != 2000 ||
			yrep.TotalProfit != -1000 || yrep.NetCash != -2000 {
			t.Fatalf("bad=%q: yesterday TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 0/2000/2000/-1000/-2000",
				bad, yrep.TotalCash, yrep.CashReturns, yrep.TotalReturns, yrep.TotalProfit, yrep.NetCash)
		}
	}
}

// PR8-L1 (midnight-shift guard): the leg's shift opened yesterday
// but the return was made today; the leg's MovementDate is mangled
// while its Returns row carries a valid today date. The same-shift
// sibling set is exactly the leg's own row, so the sibling step must
// give today — a shift-first reorder would date the leg to the
// shift's yesterday and disagree with the row. This pins why the fix
// keeps the sibling step (restricted to the leg's own shift, and
// only when the set is complete) ahead of the shift fallback.
// The shift's ShiftDate is backdated AFTER the sale/return so no
// rollover consult runs on the edited date; DailyClose itself never
// auto-closes (verified in code).
func TestParityFix8_ReturnLegMidnightShiftSibling(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000, sold today
		e.h1Return(t, invID, 1, "Cash", 2000) // made today
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("mangle leg: %v", err)
		}
		e.p2AssertSummaryDrawer(t, 16000)
		today, err := time.Parse("2006-01-02", Today())
		if err != nil {
			t.Fatalf("parse Today: %v", err)
		}
		yesterday := today.AddDate(0, 0, -1).Format("2006-01-02")
		if _, err := e.s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday, e.shiftID); err != nil {
			t.Fatalf("backdate shift: %v", err)
		}
		rep := e.p3DailyClose(t)
		if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
			rep.TotalProfit != 3000 || rep.NetCash != 6000 {
			t.Fatalf("bad=%q: today TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
				bad, rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
		}
		yrep, err := e.s.DailyClose(e.ctx, e.admin, yesterday)
		if err != nil {
			t.Fatalf("bad=%q: DailyClose yesterday: %v", bad, err)
		}
		if yrep.TotalCash != 0 || yrep.CashReturns != 0 || yrep.TotalReturns != 0 ||
			yrep.TotalProfit != 0 || yrep.NetCash != 0 {
			t.Fatalf("bad=%q: yesterday TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 0/0/0/0/0",
				bad, yrep.TotalCash, yrep.CashReturns, yrep.TotalReturns, yrep.TotalProfit, yrep.NetCash)
		}
	}
}
