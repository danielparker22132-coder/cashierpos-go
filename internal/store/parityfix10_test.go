// Parity fix-10 tests: PR10-L1 (the ReturnID linkage names only the
// FIRST row of a return event — multi-line events still misdate in
// both directions in a midnight shift), PR10-L2 (the ShiftsToday
// table on /shifts still shows stored 0.00 for open shifts), and
// PR10-L3 (idx_cashbox_autokind_ref in the schema string aborts
// reopening a pre-fix-2 database).
//
// PR10-L1 fix: the writer stamps the event id (the first Returns
// row's ID) on EVERY row of the event as ReturnEventID, and the
// leg already stamps the same id as its ReturnID. returnLegDay
// reads the event's day through ANY of its rows (a single distinct
// parseable day among the event's rows IS the event's day — all
// rows share ReturnDate at write time), and returnsRowDay finds
// its leg through the event id instead of its own row id, so row
// [1] of a multi-line event is no longer leg-less. The guard +
// sibling logic stays only as the fallback for legacy legs/rows
// with no event linkage.
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cashierpos-go/internal/money"
)

// p10SellTwoLine sells 2 units of the h1 product and 2 units of a
// second product (both 2000¢) for cash — an 8000¢ two-line sale —
// and returns the invoice id and the second product id.
func (e *h1Env) p10SellTwoLine(t *testing.T) (int64, int64) {
	t.Helper()
	pid2 := testProductB2(t, e.s, "أرز", 1000, 2000, 10000) // 10 units
	res, err := e.s.CreateInvoice(e.ctx, e.admin, CreateInvoiceInput{
		CustomerID: 1, SaleType: "retail", PaymentMethod: "Cash",
		Lines: []SaleLineInput{
			{ProductID: e.pid, Qty: money.Qty(2000)},
			{ProductID: pid2, Qty: money.Qty(2000)},
		},
	})
	if err != nil {
		t.Fatalf("two-line sale: %v", err)
	}
	if res.Net != 8000 || res.Paid != 8000 {
		t.Fatalf("two-line sale: net/paid = %d/%d, want 8000/8000", res.Net, res.Paid)
	}
	return res.InvoiceID, pid2
}

// p10ReturnTwoLine returns 1 unit of each product of a two-line
// invoice in ONE ProcessReturn call — a single cash leg of 4000¢
// covering 2 Returns rows — and checks the refund.
func (e *h1Env) p10ReturnTwoLine(t *testing.T, invID, pid2 int64) {
	t.Helper()
	res, err := e.s.ProcessReturn(e.ctx, e.admin, invID,
		[]ReturnLineInput{
			{ProductID: e.pid, Qty: money.Qty(1000)},
			{ProductID: pid2, Qty: money.Qty(1000)},
		}, "تالف", "Cash")
	if err != nil {
		t.Fatalf("two-line return: %v", err)
	}
	if res.TotalRefund != 4000 {
		t.Fatalf("two-line return: total refund = %d, want 4000", res.TotalRefund)
	}
}

// p10Yesterday returns yesterday's date string (Today() − 1 day).
func p10Yesterday(t *testing.T) string {
	t.Helper()
	today, err := time.Parse("2006-01-02", Today())
	if err != nil {
		t.Fatalf("parse Today: %v", err)
	}
	return today.AddDate(0, 0, -1).Format("2006-01-02")
}

// p10AssertDay asserts one DailyClose day's
// TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash tuple.
func p10AssertDay(t *testing.T, e *h1Env, date string, want [5]money.Cents) {
	t.Helper()
	rep, err := e.s.DailyClose(e.ctx, e.admin, date)
	if err != nil {
		t.Fatalf("DailyClose %s: %v", date, err)
	}
	got := [5]money.Cents{rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash}
	if got != want {
		t.Fatalf("DailyClose %s TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want %d/%d/%d/%d/%d",
			date, got[0], got[1], got[2], got[3], got[4], want[0], want[1], want[2], want[3], want[4])
	}
}

// PR10-L1 (writer): a two-line return event stamps the event id on
// BOTH Returns rows and on the single cash leg — the leg names its
// whole event, not just row [0].
func TestParityFix10_WriterStampsEventID(t *testing.T) {
	e := h1Setup(t)
	invID, pid2 := e.p10SellTwoLine(t)
	e.p10ReturnTwoLine(t, invID, pid2)
	var firstRow, secondRow, ev0, ev1 int64
	if err := e.s.DB.QueryRow(`SELECT ReturnID, ReturnEventID FROM Returns WHERE InvoiceID=? ORDER BY ReturnID LIMIT 1`,
		invID).Scan(&firstRow, &ev0); err != nil {
		t.Fatalf("row [0]: %v", err)
	}
	if err := e.s.DB.QueryRow(`SELECT ReturnID, ReturnEventID FROM Returns WHERE InvoiceID=? ORDER BY ReturnID DESC LIMIT 1`,
		invID).Scan(&secondRow, &ev1); err != nil {
		t.Fatalf("row [1]: %v", err)
	}
	if firstRow == secondRow {
		t.Fatalf("expected two Returns rows, got one id %d", firstRow)
	}
	if ev0 != firstRow || ev1 != firstRow {
		t.Fatalf("ReturnEventID = %d/%d, want %d/%d (both rows carry the event id)", ev0, ev1, firstRow, firstRow)
	}
	var legID int64
	if err := e.s.DB.QueryRow(`SELECT ReturnID FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?`,
		invID).Scan(&legID); err != nil {
		t.Fatalf("leg ReturnID: %v", err)
	}
	if legID != firstRow {
		t.Fatalf("leg ReturnID = %d, want %d (the event id)", legID, firstRow)
	}
}

// PR10-L1 case (a) (money leg): multi-line event, leg + row [0]
// mangled, midnight shift (shift backdated to yesterday). True
// values: today 8000/4000/4000/2000/4000, yesterday all 0. On
// fix-9 code NetCash was 8000 today and −4000 yesterday (the leg
// read row [0] (garbage), the sibling set {today, garbage} was
// incomplete, so the leg fell through to the shift day — even
// though row [1] proves today). With the event linkage the leg
// reads the event's day through row [1].
func TestParityFix10_MultiLineCaseA_LegAndRow0Mangled(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID, pid2 := e.p10SellTwoLine(t)
		e.p10ReturnTwoLine(t, invID, pid2)
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("mangle leg: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
			WHERE ReturnID=(SELECT MIN(ReturnID) FROM Returns WHERE InvoiceID=?)`, bad, invID); err != nil {
			t.Fatalf("mangle row [0]: %v", err)
		}
		yesterday := p10Yesterday(t)
		if _, err := e.s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday, e.shiftID); err != nil {
			t.Fatalf("backdate shift: %v", err)
		}
		sum := e.p2Summary(t)
		if sum.ReturnsTotal != 4000 {
			t.Fatalf("bad=%q: summary ReturnsTotal = %d, want 4000", bad, sum.ReturnsTotal)
		}
		e.p2AssertSummaryDrawer(t, 14000)
		p10AssertDay(t, e, Today(), [5]money.Cents{8000, 4000, 4000, 2000, 4000})
		p10AssertDay(t, e, yesterday, [5]money.Cents{0, 0, 0, 0, 0})
	}
}

// PR10-L1 case (b) (money leg + integration leg): multi-line event,
// leg + row [1] (non-first) mangled, midnight shift. True values:
// today 8000/4000/4000/2000/4000, yesterday all 0. On fix-9 code
// NetCash stayed right (the leg read row [0], valid today) but
// TotalReturns/TotalProfit misattributed — today 2000 vs true
// 4000 — because row [1] had no leg naming it and fell to the
// shift day. With the event linkage row [1] finds its leg through
// the event id, and the leg's mangled date falls back to the
// event's day through row [0].
func TestParityFix10_MultiLineCaseB_Row1Mangled(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID, pid2 := e.p10SellTwoLine(t)
		e.p10ReturnTwoLine(t, invID, pid2)
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("mangle leg: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate=?
			WHERE ReturnID=(SELECT MAX(ReturnID) FROM Returns WHERE InvoiceID=?)`, bad, invID); err != nil {
			t.Fatalf("mangle row [1]: %v", err)
		}
		yesterday := p10Yesterday(t)
		if _, err := e.s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday, e.shiftID); err != nil {
			t.Fatalf("backdate shift: %v", err)
		}
		sum := e.p2Summary(t)
		if sum.ReturnsTotal != 4000 {
			t.Fatalf("bad=%q: summary ReturnsTotal = %d, want 4000", bad, sum.ReturnsTotal)
		}
		e.p2AssertSummaryDrawer(t, 14000)
		p10AssertDay(t, e, Today(), [5]money.Cents{8000, 4000, 4000, 2000, 4000})
		p10AssertDay(t, e, yesterday, [5]money.Cents{0, 0, 0, 0, 0})
	}
}

// PR10-L1 (legacy fallback): a pre-fix-10 event — leg ReturnID 0
// AND rows ReturnEventID 0 — still resolves through the guarded
// sibling logic. Single return, leg mangled, row valid today,
// midnight shift: the sibling set is exactly the leg's own row,
// complete, so the leg is dated today — the fix-8 midnight
// behaviour, unchanged for unlinked legs.
func TestParityFix10_LegacyUnlinked_Fallback(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000) // made today
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?, ReturnID=0
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("mangle leg + unlink: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnEventID=0 WHERE InvoiceID=?`, invID); err != nil {
			t.Fatalf("strip event id: %v", err)
		}
		e.p2AssertSummaryDrawer(t, 16000)
		yesterday := p10Yesterday(t)
		if _, err := e.s.DB.Exec(`UPDATE Shifts SET ShiftDate=? WHERE ShiftID=?`, yesterday, e.shiftID); err != nil {
			t.Fatalf("backdate shift: %v", err)
		}
		p10AssertDay(t, e, Today(), [5]money.Cents{8000, 2000, 2000, 3000, 6000})
		p10AssertDay(t, e, yesterday, [5]money.Cents{0, 0, 0, 0, 0})
	}
}

// PR10-L2 (store layer): the SHARED helper
// ApplyLiveSummaryToOpenShifts gives an open shift the live
// summary's Expected — the same override DailyClose applies —
// and leaves closed shifts on their stored values. Shape: the
// PR9-L2 shape (sell 4×2000¢ cash, return 1×2000¢ via Visa,
// cancel, refund 6000 → live Expected 12000, Retained 2000).
// ListShiftsByDate WITHOUT the helper must still return the
// stored 0 — the override lives in the helper, not the list.
func TestParityFix10_SharedHelperOpenShiftLiveExpected(t *testing.T) {
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
	// Without the helper the list still reads the stored 0.
	raw, err := e.s.ListShiftsByDate(e.ctx, Today())
	if err != nil {
		t.Fatalf("ListShiftsByDate: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("ListShiftsByDate = %d shifts, want 1", len(raw))
	}
	if !raw[0].IsOpen || raw[0].ExpectedBalance != 0 {
		t.Fatalf("raw list: IsOpen/Expected = %v/%d, want true/0 (stored, while open)", raw[0].IsOpen, raw[0].ExpectedBalance)
	}
	// With the helper the open row shows the live summary.
	if err := e.s.ApplyLiveSummaryToOpenShifts(e.ctx, raw); err != nil {
		t.Fatalf("ApplyLiveSummaryToOpenShifts: %v", err)
	}
	sh := raw[0]
	if sh.ExpectedBalance != 12000 || sh.ActualBalance != 12000 || sh.Difference != 0 ||
		sh.DifferenceStatus != "متوازن" || sh.RetainedCanceledCashTotal != 2000 {
		t.Fatalf("helper row Expected/Actual/Difference/Status/Retained = %d/%d/%d/%q/%d, want 12000/12000/0/متوازن/2000",
			sh.ExpectedBalance, sh.ActualBalance, sh.Difference, sh.DifferenceStatus, sh.RetainedCanceledCashTotal)
	}
	// The stored columns are still 0 — the helper only rewrites
	// the in-memory rows, never the database.
	var storedExp, storedAct int64
	if err := e.s.DB.QueryRow(`SELECT COALESCE(ExpectedBalance,0), COALESCE(ActualBalance,0)
		FROM Shifts WHERE ShiftID=?`, e.shiftID).Scan(&storedExp, &storedAct); err != nil {
		t.Fatalf("stored values: %v", err)
	}
	if storedExp != 0 || storedAct != 0 {
		t.Fatalf("stored Expected/Actual = %d/%d, want 0/0 while open", storedExp, storedAct)
	}
	// After closing, the helper leaves the (now stored) 120.00
	// untouched — closed rows keep their stored values.
	if _, err := e.s.CloseShift(e.ctx, e.admin, e.shiftID); err != nil {
		t.Fatalf("close shift: %v", err)
	}
	closed, err := e.s.ListShiftsByDate(e.ctx, Today())
	if err != nil {
		t.Fatalf("ListShiftsByDate after close: %v", err)
	}
	if err := e.s.ApplyLiveSummaryToOpenShifts(e.ctx, closed); err != nil {
		t.Fatalf("helper after close: %v", err)
	}
	if closed[0].IsOpen {
		t.Fatalf("after close: IsOpen = true, want false")
	}
	if closed[0].ExpectedBalance != 12000 || closed[0].ActualBalance != 12000 || closed[0].Difference != 0 {
		t.Fatalf("after close: row Expected/Actual/Difference = %d/%d/%d, want 12000/12000/0",
			closed[0].ExpectedBalance, closed[0].ActualBalance, closed[0].Difference)
	}
}

// PR10-L3 (migration): a database created before fix-2 gains the
// AutoKind/RefInvoiceID columns (and the idx_cashbox_autokind_ref
// index) when reopened — the server must OPEN it, not abort with
// "no such column: AutoKind". Shape: sell 4×2000¢, return 1×2000¢
// cash; after reopen DailyClose must still give today
// 8000/2000/2000/3000/6000 (the backfill re-links the return leg
// from its reason).
func TestParityFix10_ReopenMigratesAutokindIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mig10.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	admin := b2TestUser(t, s, "mig10admin", "admin")
	if _, err := s.OpenShift(ctx, admin, 10000, false); err != nil {
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
	// Simulate a pre-fix-2 database: drop the AutoKind/RefInvoiceID
	// columns and both CashBox indexes (the review-10 reproduction).
	if _, err := s.DB.Exec(`DROP INDEX IF EXISTS idx_cashbox_autokind_ref`); err != nil {
		t.Fatalf("drop autokind index: %v", err)
	}
	if _, err := s.DB.Exec(`DROP INDEX IF EXISTS idx_cashbox_returnid`); err != nil {
		t.Fatalf("drop returnid index: %v", err)
	}
	if _, err := s.DB.Exec(`ALTER TABLE CashBox DROP COLUMN AutoKind`); err != nil {
		t.Fatalf("drop AutoKind: %v", err)
	}
	if _, err := s.DB.Exec(`ALTER TABLE CashBox DROP COLUMN RefInvoiceID`); err != nil {
		t.Fatalf("drop RefInvoiceID: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Reopen: migrate() must add the columns and both indexes back
	// — pre-fix-10 code aborted here ("no such column: AutoKind")
	// because the index lived in the schema string, which runs
	// before the ALTERs.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	var colCount int
	if err := s2.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('CashBox') WHERE name IN ('AutoKind','RefInvoiceID')`).Scan(&colCount); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if colCount != 2 {
		t.Fatalf("AutoKind/RefInvoiceID columns = %d, want 2 after reopen", colCount)
	}
	var idxCount int
	if err := s2.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_cashbox_autokind_ref'`).Scan(&idxCount); err != nil {
		t.Fatalf("index check: %v", err)
	}
	if idxCount != 1 {
		t.Fatalf("idx_cashbox_autokind_ref missing after reopen")
	}
	// The shape still reports the same money after the reopen.
	rep, err := s2.DailyClose(ctx, admin, Today())
	if err != nil {
		t.Fatalf("DailyClose: %v", err)
	}
	if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.TotalReturns != 2000 ||
		rep.TotalProfit != 3000 || rep.NetCash != 6000 {
		t.Fatalf("today TotalCash/CashReturns/TotalReturns/TotalProfit/NetCash = %d/%d/%d/%d/%d, want 8000/2000/2000/3000/6000",
			rep.TotalCash, rep.CashReturns, rep.TotalReturns, rep.TotalProfit, rep.NetCash)
	}
}
