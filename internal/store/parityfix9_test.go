// Parity fix-9 tests: PR9-L1 (the fix-8 completeness guard
// over-fall-throughs in a midnight shift — a leg whose OWN row is
// valid is misdated to the shift day when a DIFFERENT return's row
// is unparseable) and PR9-L2 (the DailyClose shifts table shows
// المتوقع 0.00 for an OPEN shift with real activity).
//
// PR9-L1 fix: the writer stamps the return event's first Returns
// row ID (ReturnID) on the CashBox 'return' leg. returnLegDay reads
// its own row's ReturnDate first (exact — no sibling inference),
// and returnsRowDay reads its own leg's MovementDate back (the leg
// and the row are written with the same `now`). The sibling/guard
// logic stays only as the fallback for legacy legs whose ReturnID
// is 0. This fixes the hunt shape, shape D, the midnight guard, and
// the C-N2 caveat (two parseable sibling days) in one move: with
// the link, no ordering or guard tweak is needed at all.
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// PR9-L1 (writer): a return's CashBox leg carries the ReturnID of
// its own event's first Returns row, and each leg names its own
// event — not the other return's row.
func TestParityFix9_WriterStampsReturnID(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000) // return 1
	e.h1Return(t, invID, 1, "Cash", 2000) // return 2
	var minRow, maxRow int64
	if err := e.s.DB.QueryRow(`SELECT MIN(ReturnID), MAX(ReturnID) FROM Returns WHERE InvoiceID=?`, invID).Scan(&minRow, &maxRow); err != nil {
		t.Fatalf("returns rows: %v", err)
	}
	if minRow == maxRow {
		t.Fatalf("expected two Returns rows, got one id %d", minRow)
	}
	var minLeg, maxLeg int64
	if err := e.s.DB.QueryRow(`SELECT ReturnID FROM CashBox WHERE CashBoxID=(SELECT MIN(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, invID).Scan(&minLeg); err != nil {
		t.Fatalf("first leg ReturnID: %v", err)
	}
	if err := e.s.DB.QueryRow(`SELECT ReturnID FROM CashBox WHERE CashBoxID=(SELECT MAX(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, invID).Scan(&maxLeg); err != nil {
		t.Fatalf("second leg ReturnID: %v", err)
	}
	if minLeg != minRow || maxLeg != maxRow {
		t.Fatalf("leg ReturnIDs = %d/%d, want %d/%d (each leg names its own event)", minLeg, maxLeg, minRow, maxRow)
	}
}

// PR9-L1 (hunt shape, money leg's construction): sell 4×2000¢ today
// → return 1×2000¢ cash today (return 1) → return 1×2000¢ cash today
// (return 2) → mangle return 1's LEG → mangle return 2's ROW →
// backdate the shift's ShiftDate to yesterday (midnight shift).
// True values: today TotalCash 8000 / CashReturns 4000 /
// TotalReturns 4000 / TotalProfit 2000 / NetCash 4000; yesterday
// all 0; summary Expected 14000 = drawer. On fix-8 code NetCash
// was 6000 today and −2000 yesterday (return 1's leg fell through
// to the shift day because return 2's row made the sibling set
// incomplete). With the link, return 1's leg reads its own row
// (today) and return 2's row reads its own leg back (today).
func TestParityFix9_HuntShape_MidnightSplitMangle(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000) // return 1, today
		e.h1Return(t, invID, 1, "Cash", 2000) // return 2, today
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE CashBoxID=(SELECT MIN(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, bad, invID); err != nil {
			t.Fatalf("mangle leg 1: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
			WHERE ReturnID=(SELECT MAX(ReturnID) FROM Returns WHERE InvoiceID=?)`, bad, invID); err != nil {
			t.Fatalf("mangle row 2: %v", err)
		}
		today, err := time.Parse("2006-01-02", Today())
		if err != nil {
			t.Fatalf("parse Today: %v", err)
		}
		yesterday := today.AddDate(0, 0, -1).Format("2006-01-02")
		if _, err := e.s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday, e.shiftID); err != nil {
			t.Fatalf("backdate shift: %v", err)
		}
		sum := e.p2Summary(t)
		if sum.ReturnsTotal != 4000 {
			t.Fatalf("bad=%q: summary ReturnsTotal = %d, want 4000", bad, sum.ReturnsTotal)
		}
		e.p2AssertSummaryDrawer(t, 14000)
		rep := e.p3DailyClose(t)
		if rep.TotalCash != 8000 || rep.CashReturns != 4000 || rep.TotalReturns != 4000 ||
			rep.TotalProfit != 2000 || rep.NetCash != 4000 {
			t.Fatalf("bad=%q: today TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/4000/4000/2000/4000",
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

// PR9-L1 (shape D, still passing through the new code): sell 4×2000¢
// today → return 1×2000¢ cash today (leg AND row mangled) → return
// 1×2000¢ cash backdated to yesterday (row AND leg). The linked leg
// reads its own row (garbage) and falls through to the guarded
// sibling logic exactly as fix-8 did: today TotalCash 8000 /
// CashReturns 2000 / TotalReturns 2000 / TotalProfit 3000 / NetCash
// 6000; yesterday 0 / 2000 / 2000 / −1000 / −2000. Summary Expected
// 14000 = drawer.
func TestParityFix9_ShapeD_StillPasses(t *testing.T) {
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

// PR9-L1 (C-N2, resolved by the link): two parseable sibling days,
// one is the leg's own day. Sell 4×2000¢ today, return 1 today,
// return 1 backdated to yesterday (row + leg), mangle ONLY the
// backdated leg. The backdated leg reads its own row (yesterday)
// instead of falling through the ambiguous sibling set to the shift
// day (today). Today 8000/2000/2000/3000/6000; yesterday
// 0/2000/2000/−1000/−2000.
func TestParityFix9_CN2_BackdatedLegReadsOwnRow(t *testing.T) {
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
			WHERE CashBoxID=(SELECT MAX(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, bad, invID); err != nil {
			t.Fatalf("mangle backdated leg: %v", err)
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

// PR9-L1 (legacy fallback): a pre-PR9-L1 'return' leg carries
// ReturnID 0, so returnLegDay must still resolve it through the
// guarded sibling logic. Single return, leg mangled, row valid
// today, midnight shift (shift day yesterday): the sibling set is
// exactly the leg's own row, complete, so the leg is dated today —
// the fix-8 midnight behaviour, unchanged for unlinked legs.
func TestParityFix9_LegacyNullReturnID_Fallback(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000) // made today
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?, ReturnID=0
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("mangle leg + unlink: %v", err)
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

// PR9-L2: the DailyClose shifts table shows the LIVE summary's
// Expected for an OPEN shift. Review shape: open shift, opening
// 100.00, sell 4×2000¢ cash, return 1×2000¢ via Visa, cancel the
// invoice (refund 6000). While the shift is still OPEN the live
// summary Expected is 120.00 (CashSales 0 / RetainedCanceledCash
// 2000); the stored columns are still 0 (written only at close).
// The DailyClose row must read 120.00 / 120.00 / 0 متوازن — not
// 0.00 — and the page's own report totals stay correct (TotalCash
// 2000 / TotalReturns 2000 / NetCash 2000). After closing, the same
// row reads the stored 120.00.
func TestParityFix9_DailyCloseOpenShiftLiveExpected(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000 cash
	e.h1Return(t, invID, 1, "Visa", 2000)
	e.h1PreviewAndCancel(t, invID, 6000)
	sum := e.p2Summary(t)
	if sum.ExpectedBalance != 12000 || sum.RetainedCanceledCash != 2000 {
		t.Fatalf("live summary Expected/Retained = %d/%d, want 12000/2000",
			sum.ExpectedBalance, sum.RetainedCanceledCash)
	}
	e.p2AssertSummaryDrawer(t, 12000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 2000 || rep.TotalReturns != 2000 || rep.CashReturns != 0 || rep.NetCash != 2000 {
		t.Fatalf("report TotalCash/TotalReturns/CashReturns/NetCash = %d/%d/%d/%d, want 2000/2000/0/2000",
			rep.TotalCash, rep.TotalReturns, rep.CashReturns, rep.NetCash)
	}
	if len(rep.Shifts) != 1 {
		t.Fatalf("DailyClose shifts = %d, want 1", len(rep.Shifts))
	}
	sh := rep.Shifts[0]
	if !sh.IsOpen {
		t.Fatalf("shift IsOpen = false, want true (still open)")
	}
	if sh.ExpectedBalance != 12000 || sh.ActualBalance != 12000 || sh.Difference != 0 ||
		sh.DifferenceStatus != "متوازن" || sh.RetainedCanceledCashTotal != 2000 {
		t.Fatalf("open shift row Expected/Actual/Difference/Status/Retained = %d/%d/%d/%q/%d, want 12000/12000/0/متوازن/2000",
			sh.ExpectedBalance, sh.ActualBalance, sh.Difference, sh.DifferenceStatus, sh.RetainedCanceledCashTotal)
	}
	// The stored columns are still 0 — the fix is display-layer in
	// DailyClose, not a stored write.
	var storedExp, storedAct int64
	if err := e.s.DB.QueryRow(`SELECT COALESCE(ExpectedBalance,0), COALESCE(ActualBalance,0)
		FROM Shifts WHERE ShiftID=?`, e.shiftID).Scan(&storedExp, &storedAct); err != nil {
		t.Fatalf("stored values: %v", err)
	}
	if storedExp != 0 || storedAct != 0 {
		t.Fatalf("stored Expected/Actual = %d/%d, want 0/0 while open", storedExp, storedAct)
	}
	// After closing, the same row reads the stored 120.00.
	if _, err := e.s.CloseShift(e.ctx, e.admin, e.shiftID); err != nil {
		t.Fatalf("close shift: %v", err)
	}
	rep2 := e.p3DailyClose(t)
	if len(rep2.Shifts) != 1 {
		t.Fatalf("after close: DailyClose shifts = %d, want 1", len(rep2.Shifts))
	}
	sh2 := rep2.Shifts[0]
	if sh2.IsOpen {
		t.Fatalf("after close: shift IsOpen = true, want false")
	}
	if sh2.ExpectedBalance != 12000 || sh2.ActualBalance != 12000 || sh2.Difference != 0 {
		t.Fatalf("after close: row Expected/Actual/Difference = %d/%d/%d, want 12000/12000/0",
			sh2.ExpectedBalance, sh2.ActualBalance, sh2.Difference)
	}
}

// PR9-L1 (migration): a database created before fix-9 gains the
// ReturnID column (and its index) when reopened, and its legacy
// 'return' legs — now unlinked (ReturnID 0) — still resolve through
// the guarded sibling fallback. Shape: the midnight single return
// (leg mangled, row valid today, shift backdated to yesterday),
// built before the drop; after reopen, DailyClose must still give
// today 8000/2000/2000/3000/6000 and yesterday all 0.
func TestParityFix9_ReopenMigratesReturnID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mig.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	// Build the midnight single-return shape on the file DB.
	admin := b2TestUser(t, s, "migadmin", "admin")
	shiftID, err := s.OpenShift(ctx, admin, 10000, false)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	pid := testProductB2(t, s, "سكر", 1000, 2000, 10000)
	res, err := s.CreateInvoice(ctx, admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{{ProductID: pid, Qty: 4000}},
	})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if _, err := s.ProcessReturn(ctx, admin, res.InvoiceID,
		[]ReturnLineInput{{ProductID: pid, Qty: 1000}}, "تالف", "Cash"); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE CashBox SET MovementDate='garbage'
		WHERE AutoKind='return' AND RefInvoiceID=?`, res.InvoiceID); err != nil {
		t.Fatalf("mangle leg: %v", err)
	}
	today, err := time.Parse("2006-01-02", Today())
	if err != nil {
		t.Fatalf("parse Today: %v", err)
	}
	yesterday := today.AddDate(0, 0, -1).Format("2006-01-02")
	if _, err := s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday, shiftID); err != nil {
		t.Fatalf("backdate shift: %v", err)
	}
	// Simulate a pre-fix-9 database: drop the column and its index.
	if _, err := s.DB.Exec(`DROP INDEX IF EXISTS idx_cashbox_returnid`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := s.DB.Exec(`ALTER TABLE CashBox DROP COLUMN ReturnID`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Reopen: migrate() must add the column (and the index) back.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	var colCount int
	if err := s2.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('CashBox') WHERE name='ReturnID'`).Scan(&colCount); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if colCount != 1 {
		t.Fatalf("ReturnID column missing after reopen")
	}
	var idxCount int
	if err := s2.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_cashbox_returnid'`).Scan(&idxCount); err != nil {
		t.Fatalf("index check: %v", err)
	}
	if idxCount != 1 {
		t.Fatalf("idx_cashbox_returnid missing after reopen")
	}
	// The legacy (now unlinked) leg still resolves via the fallback.
	rep, err := s2.DailyClose(ctx, admin, Today())
	if err != nil {
		t.Fatalf("DailyClose: %v", err)
	}
	if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
		rep.TotalProfit != 3000 || rep.NetCash != 6000 {
		t.Fatalf("today TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
			rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
	}
	yrep, err := s2.DailyClose(ctx, admin, yesterday)
	if err != nil {
		t.Fatalf("DailyClose yesterday: %v", err)
	}
	if yrep.TotalCash != 0 || yrep.CashReturns != 0 || yrep.TotalReturns != 0 ||
		yrep.TotalProfit != 0 || yrep.NetCash != 0 {
		t.Fatalf("yesterday TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 0/0/0/0/0",
			yrep.TotalCash, yrep.CashReturns, yrep.TotalReturns, yrep.TotalProfit, yrep.NetCash)
	}
}
