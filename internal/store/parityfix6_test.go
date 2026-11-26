// Parity fix-6 tests: PR6-L1 (DailyClose still drops CashBox rows with
// malformed/empty MovementDate that the shift summary counts — return
// legs AND courier collections) and PR6-L2 (fix-5 performance
// regression: DailyClose O(total history) — indexes).
package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cashierpos-go/internal/money"
)

// PR6-L1 (return leg, non-canceled): cash sale 4×2000¢ → return
// 1×2000¢ cash with the leg's MovementDate mangled. Pre-fix DailyClose
// dropped the leg → NetCash 8000 vs true 6000 (and CashReturns 0 vs
// TotalReturns 2000 on the same page); post-fix both reports agree:
// TotalCash 8000, CashReturns 2000, NetCash 6000. The summary must
// agree (Expected 16000 = drawer 16000).
func TestParityFix6_ReturnLegMalformedMovementDate(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000)
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
			t.Fatalf("MovementDate=%q: %v", bad, err)
		}
		e.p2AssertSummaryDrawer(t, 16000)
		rep := e.p3DailyClose(t)
		if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.NetCash != 6000 {
			t.Fatalf("MovementDate=%q: daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 8000/2000/6000",
				bad, rep.TotalCash, rep.CashReturns, rep.NetCash)
		}
		if rep.TotalReturns != 2000 {
			t.Fatalf("MovementDate=%q: daily TotalReturns = %d, want 2000", bad, rep.TotalReturns)
		}
	}
}

// PR6-L1 (return leg, canceled): the review's second shape. Cash sale
// 4×2000¢ → return 1×2000¢ cash → cancel (refund 6000¢) with the leg's
// MovementDate mangled. Pre-fix DailyClose NetCash was 2000 vs true 0
// (the legless pass added back atSaleCash − refundToday without
// subtracting the invisible leg); post-fix the leg is visible to the
// cap logic: TotalCash 0, CashReturns 0, NetCash 0, CanceledCount 1.
func TestParityFix6_ReturnLegMalformedMovementDateCanceled(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		invID := e.h1Sell(t, 4) // paid 8000
		e.h1Return(t, invID, 1, "Cash", 2000)
		e.h1PreviewAndCancel(t, invID, 6000)
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='return' AND RefInvoiceID=?`, bad, invID); err != nil {
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

// PR6-L1 (return leg, ambiguous Returns days): the invoice has returns
// on two days, so the Returns.ReturnDate fallback is genuinely
// ambiguous and must NOT pick one — the leg falls through to its
// shift's date. Shape: sell 4 → return 1 today (leg mangled) → return
// 1 backdated to yesterday (its Returns row and its leg). Today:
// CashReturns 2000 (the mangled leg only), TotalReturns 2000,
// NetCash 6000; the summary counts both legs (Expected 14000).
func TestParityFix6_ReturnLegMalformedMultiReturnAmbiguous(t *testing.T) {
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
	if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate='garbage'
		WHERE CashBoxID=(SELECT MIN(CashBoxID) FROM CashBox WHERE AutoKind='return' AND RefInvoiceID=?)`, invID); err != nil {
		t.Fatalf("mangle leg: %v", err)
	}
	e.p2AssertSummaryDrawer(t, 14000)
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.NetCash != 6000 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 8000/2000/6000",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
	if rep.TotalReturns != 2000 {
		t.Fatalf("daily TotalReturns = %d, want 2000 (only today's return)", rep.TotalReturns)
	}
}

// PR6-L1 (return leg, last-resort fallback): every better day source
// is corrupt — the leg's MovementDate is mangled, its invoice's
// Returns rows are mangled too, and the leg is orphaned from its
// shift — so the chain falls back to the invoice's sale date. This
// pins only the DailyClose fallback chain: the summary is NOT
// asserted here, because a leg with ShiftID=0 belongs to no shift
// (the test orphaned it; the live writer always stamps a shift).
func TestParityFix6_ReturnLegMalformedSaleDateFallback(t *testing.T) {
	e := h1Setup(t)
	invID := e.h1Sell(t, 4) // paid 8000
	e.h1Return(t, invID, 1, "Cash", 2000)
	if _, err := e.s.DB.Exec(`UPDATE Returns SET ReturnDate='garbage' WHERE InvoiceID=?`, invID); err != nil {
		t.Fatalf("mangle ReturnDate: %v", err)
	}
	if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate='garbage', ShiftID=0
		WHERE AutoKind='return' AND RefInvoiceID=?`, invID); err != nil {
		t.Fatalf("mangle leg: %v", err)
	}
	rep := e.p3DailyClose(t)
	if rep.TotalCash != 8000 || rep.CashReturns != 2000 || rep.NetCash != 6000 {
		t.Fatalf("daily TotalCash/CashReturns/NetCash = %d/%d/%d, want 8000/2000/6000",
			rep.TotalCash, rep.CashReturns, rep.NetCash)
	}
}

// PR6-L1 (courier collection): a courier hands over 4000¢ cash
// settling a debt, with the collection row's MovementDate mangled.
// Pre-fix DailyClose dropped it from TotalCash (CourierCollection 0)
// while the summary counted it (Expected 14000); post-fix both agree:
// CourierCollection 4000, TotalCash 4000, NetCash 4000.
func TestParityFix6_CourierMalformedMovementDate(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		e := h1Setup(t)
		id, err := e.s.CreateCourierDebt(e.ctx, e.admin,
			CourierDebtInput{CourierName: "مندوب", Amount: money.Cents(10000), OrderRef: "ORD-9"})
		if err != nil {
			t.Fatalf("create debt: %v", err)
		}
		if _, err := e.s.SettleCourierDebt(e.ctx, e.admin, id, money.Cents(4000), "Cash", ""); err != nil {
			t.Fatalf("settle: %v", err)
		}
		if _, err := e.s.DB.Exec(`UPDATE CashBox SET MovementDate=?
			WHERE AutoKind='courier_collection'`, bad); err != nil {
			t.Fatalf("MovementDate=%q: %v", bad, err)
		}
		e.p2AssertSummaryDrawer(t, 14000)
		rep := e.p3DailyClose(t)
		if rep.CourierCollection != 4000 || rep.TotalCash != 4000 || rep.NetCash != 4000 {
			t.Fatalf("MovementDate=%q: daily CourierCollection/TotalCash/NetCash = %d/%d/%d, want 4000/4000/4000",
				bad, rep.CourierCollection, rep.TotalCash, rep.NetCash)
		}
	}
}

// PR6-L1 control: the same courier shape with a live MovementDate
// must be unchanged by the rewrite — the Go-side dating must not
// move any money on live-shaped rows.
func TestParityFix6_CourierLiveControl(t *testing.T) {
	e := h1Setup(t)
	id, err := e.s.CreateCourierDebt(e.ctx, e.admin,
		CourierDebtInput{CourierName: "مندوب", Amount: money.Cents(10000), OrderRef: "ORD-9"})
	if err != nil {
		t.Fatalf("create debt: %v", err)
	}
	if _, err := e.s.SettleCourierDebt(e.ctx, e.admin, id, money.Cents(4000), "Cash", ""); err != nil {
		t.Fatalf("settle: %v", err)
	}
	e.p2AssertSummaryDrawer(t, 14000)
	rep := e.p3DailyClose(t)
	if rep.CourierCollection != 4000 || rep.TotalCash != 4000 || rep.NetCash != 4000 {
		t.Fatalf("daily CourierCollection/TotalCash/NetCash = %d/%d/%d, want 4000/4000/4000",
			rep.CourierCollection, rep.TotalCash, rep.NetCash)
	}
}

func explainPlan(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN ` + q)
	if err != nil {
		t.Fatalf("explain %q: %v", q, err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		parts = append(parts, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return strings.Join(parts, " | ")
}

// PR6-L2: the indexes exist on a fresh DB, the planner uses the
// CashBox composite for the per-invoice refund lookup, and a database
// created before the indexes existed gains them on reopen (migrate()
// re-executes the schema string, so CREATE INDEX IF NOT EXISTS is
// the migration path — no separate ALTER needed).
func TestParityFix6_IndexesExistAndMigrate(t *testing.T) {
	e := h1Setup(t)
	for _, idx := range []string{"idx_invoices_number", "idx_cashbox_autokind_ref"} {
		var n int
		if err := e.s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master
			WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatalf("index %s lookup: %v", idx, err)
		}
		if n != 1 {
			t.Fatalf("index %s missing on fresh DB (count %d)", idx, n)
		}
	}
	plan := explainPlan(t, e.s.DB, `SELECT Amount,MovementDate FROM CashBox
		WHERE RefInvoiceID=1 AND AutoKind='cancel_refund'`)
	if !strings.Contains(plan, "USING INDEX idx_cashbox_autokind_ref") {
		t.Fatalf("refund lookup plan = %q, want idx_cashbox_autokind_ref", plan)
	}

	path := filepath.Join(t.TempDir(), "idx.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open file DB: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close file DB: %v", err)
	}
	raw, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, idx := range []string{"idx_invoices_number", "idx_cashbox_autokind_ref"} {
		if _, err := raw.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			t.Fatalf("drop %s: %v", idx, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen file DB: %v", err)
	}
	defer s2.Close()
	for _, idx := range []string{"idx_invoices_number", "idx_cashbox_autokind_ref"} {
		var n int
		if err := s2.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master
			WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatalf("index %s lookup after reopen: %v", idx, err)
		}
		if n != 1 {
			t.Fatalf("index %s not recreated on reopen (count %d)", idx, n)
		}
	}
}
