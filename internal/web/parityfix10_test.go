// Package web_test — PARITY FIX-10 tests (Review-10, 0H / 0M / 3L).
//
// PR10-L2: the ShiftsToday table on /shifts (and GET /api/shifts)
// showed stored 0.00 for an open shift while the same page's live
// summary showed the real Expected. The fix applies the shared
// live-summary override (ApplyLiveSummaryToOpenShifts) in both
// handlers, so the table agrees with the summary above it.
package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestParityFix10_ShiftPageOpenShiftLiveExpected drives a sale over
// HTTP (open shift, opening 100.00, sell 4×2000¢ cash) and asserts
// the shifts table shows the LIVE Expected 180.00 for the still-open
// shift — in the /api/shifts JSON and on the rendered /shifts page
// (the table cell, not the summary above it) — while the stored
// columns stay 0. On pre-fix-10 code the JSON row and the table
// cell both read 0.00.
func TestParityFix10_ShiftPageOpenShiftLiveExpected(t *testing.T) {
	e := readyEnv(t, false)
	adm := e.adminClient()
	shiftID := e.openShift(adm, 10000)
	pid := e.createProduct(adm, "622000000001", "سكر", "20.00", "10")
	e.cashSale(adm, pid, "4", 8000) // fails the test itself on any error
	// The stored columns are still 0 while the shift is open —
	// the fix is display-layer, not a stored write.
	if got := e.queryInt(`SELECT COALESCE(ExpectedBalance,0) FROM Shifts WHERE ShiftID=?`, shiftID); got != 0 {
		t.Fatalf("stored ExpectedBalance = %d, want 0 while open", got)
	}
	// The API renders the LIVE Expected (18000¢), Actual and
	// متوازن — not the stored 0.
	code, body := e.getJSON(adm, "/api/shifts")
	if code != http.StatusOK {
		t.Fatalf("GET /api/shifts: want 200, got %d", code)
	}
	shifts, ok := body["shifts"].([]any)
	if !ok || len(shifts) != 1 {
		t.Fatalf("GET /api/shifts: shifts = %v, want 1 row", body["shifts"])
	}
	row, ok := shifts[0].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/shifts: row = %v, want an object", shifts[0])
	}
	if row["ExpectedBalance"] != float64(18000) || row["ActualBalance"] != float64(18000) ||
		row["Difference"] != float64(0) || row["DifferenceStatus"] != "متوازن" || row["IsOpen"] != true {
		t.Fatalf("GET /api/shifts row Expected/Actual/Difference/Status/IsOpen = %v/%v/%v/%v/%v, want 18000/18000/0/متوازن/true",
			row["ExpectedBalance"], row["ActualBalance"], row["Difference"], row["DifferenceStatus"], row["IsOpen"])
	}
	// The rendered page's ShiftsToday row shows 180.00 too — the
	// summary above it renders <strong>180.00 …</strong>, so the
	// plain table cell pins the row specifically.
	code, page := e.get(adm, "/shifts")
	if code != http.StatusOK {
		t.Fatalf("GET /shifts: want 200, got %d", code)
	}
	if !strings.Contains(page, `<td class="num">180.00 ج.م</td>`) {
		t.Fatalf("GET /shifts: no ShiftsToday cell with 180.00 ج.م — page still shows the stored 0.00 for the open shift")
	}
}
