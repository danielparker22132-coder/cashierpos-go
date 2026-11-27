// Package web_test — FIX-24 lane D (R24-E-N2): no JSON-API 500 may echo
// a raw driver/DB error to the client. Every 500 body must be the single
// generic Arabic message; the fix-23 overflow refusal stays 400 Arabic
// and a genuinely missing shift stays 404.
package web_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

const fix24DGeneric500 = "حدث خطأ داخلي، حاول مرة أخرى"
const fix24DBig = int64(5000000000000000000)

// fix24DAdminEnv builds a fresh env with a working admin login.
func fix24DAdminEnv(t *testing.T, fileDB bool) (*e2eEnv, *http.Client) {
	t.Helper()
	e := newEnv(t, fileDB)
	e.seedUsers()
	e.changeAdminPassword()
	return e, e.adminClient()
}

// fix24DAssertGeneric500 fails unless the call answered 500 with exactly
// the generic Arabic message and no raw driver/DB string anywhere.
func fix24DAssertGeneric500(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusInternalServerError {
		t.Fatalf("%s: status = %d, want 500 (body %v)", what, code, body)
	}
	if body["ok"] != false {
		t.Fatalf("%s: ok = %v, want false (body %v)", what, body["ok"], body)
	}
	errMsg, _ := body["error"].(string)
	if errMsg != fix24DGeneric500 {
		t.Fatalf("%s: error = %q, want generic %q", what, errMsg, fix24DGeneric500)
	}
	for _, leak := range []string{"SQL logic error", "no such table", "integer overflow",
		"sqlite", "converting driver", "no such column", "mkdir", "not a directory"} {
		if strings.Contains(errMsg, leak) {
			t.Fatalf("%s: error leaks raw string %q in %q", what, leak, errMsg)
		}
	}
}

// fix24DDrop drops one table out from under the running server.
func fix24DDrop(t *testing.T, e *e2eEnv, table string) {
	t.Helper()
	if _, err := e.store.DB.Exec("DROP TABLE " + table); err != nil {
		t.Fatalf("DROP TABLE %s: %v", table, err)
	}
}

// TestFix24D_ShiftSummaryDropExpensesGeneric500 is the R24-E-N2 repro
// itself: DROP TABLE Expenses, then the summary must answer 500 with the
// generic Arabic message — never the raw "SQL logic error: no such table:
// Expenses (1)" the pre-fix branch echoed.
func TestFix24D_ShiftSummaryDropExpensesGeneric500(t *testing.T) {
	e, _ := fix24DAdminEnv(t, false)
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 100000)
	code, body := e.getJSON(mgr, "/api/shifts/summary?shift_id="+itoa(shiftID))
	if code != http.StatusOK {
		t.Fatalf("summary before DROP: status = %d, want 200 (body %v)", code, body)
	}
	fix24DDrop(t, e, "Expenses")
	code, body = e.getJSON(mgr, "/api/shifts/summary?shift_id="+itoa(shiftID))
	fix24DAssertGeneric500(t, "summary after DROP Expenses", code, body)
}

// TestFix24D_ShiftSummaryOverflowStill400AndMissingStill404 guards the
// fix-23 trichotomy: poisoned Expenses still refuse 400 Arabic, and a
// genuinely missing shift still 404s with الشيفت غير موجود.
func TestFix24D_ShiftSummaryOverflowStill400AndMissingStill404(t *testing.T) {
	e, _ := fix24DAdminEnv(t, false)
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 100000)
	for i := 0; i < 2; i++ {
		if _, err := e.store.DB.Exec(`INSERT INTO Expenses(Amount,Description,Category,ExpenseDate,ShiftID,UserID)
			VALUES(?, 'poison', 'عام', ?, ?, 1)`, fix24DBig, store.Today(), shiftID); err != nil {
			t.Fatalf("insert poison expense: %v", err)
		}
	}
	code, body := e.getJSON(mgr, "/api/shifts/summary?shift_id="+itoa(shiftID))
	if code != http.StatusBadRequest {
		t.Fatalf("poisoned summary: status = %d, want 400 (body %v)", code, body)
	}
	errMsg, _ := body["error"].(string)
	if !strings.Contains(errMsg, "تجاوز الحد") {
		t.Fatalf("poisoned summary: error = %q, want Arabic overflow", errMsg)
	}
	if strings.Contains(errMsg, "SQL logic error") || strings.Contains(errMsg, "integer overflow") {
		t.Fatalf("poisoned summary: error leaks raw string %q", errMsg)
	}
	code, body = e.getJSON(mgr, "/api/shifts/summary?shift_id=999999")
	if code != http.StatusNotFound {
		t.Fatalf("missing shift: status = %d, want 404 (body %v)", code, body)
	}
	errMsg, _ = body["error"].(string)
	if errMsg != "الشيفت غير موجود" {
		t.Fatalf("missing shift: error = %q, want الشيفت غير موجود", errMsg)
	}
}

// TestFix24D_ShiftSummaryDropShiftsGeneric500 covers the GetShift leg of
// apiShiftSummary: a raw driver error from GetShift (dropped Shifts
// table) used to be echoed as a 404 body; it is now a 500 with the
// generic message, while a genuinely missing shift still 404s (see the
// guard test above).
func TestFix24D_ShiftSummaryDropShiftsGeneric500(t *testing.T) {
	e, _ := fix24DAdminEnv(t, false)
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 100000)
	fix24DDrop(t, e, "Shifts")
	code, body := e.getJSON(mgr, "/api/shifts/summary?shift_id="+itoa(shiftID))
	fix24DAssertGeneric500(t, "summary after DROP Shifts (GetShift leg)", code, body)
}

// TestFix24D_ListEndpointsDropTableGeneric500 re-derives the audit: every
// other JSON-API 500 branch that echoed a raw store error now answers the
// generic message. Each case drops the table its store query reads, on a
// fresh env, and hits the endpoint live.
func TestFix24D_ListEndpointsDropTableGeneric500(t *testing.T) {
	cases := []struct {
		name  string
		table string
		path  string
	}{
		{"shift-status", "Shifts", "/api/shifts/status"},
		{"shifts-list", "Shifts", "/api/shifts?date=" + store.Today()},
		{"cashbox-list", "CashBox", "/api/cashbox?date=" + store.Today()},
		{"returns-list", "Returns", "/api/returns"},
		{"expenses-list", "Expenses", "/api/expenses"},
		{"customers-list", "Customers", "/api/customers"},
		{"debtors-list", "Customers", "/api/debtors"},
		{"vendors-list", "Vendors", "/api/vendors"},
		{"payables-list", "Payables", "/api/payables"},
		{"payables-due", "Payables", "/api/payables/due"},
		{"courier-list", "CourierDebts", "/api/courier-debts"},
		{"transfers-list", "ElectronicTransfers", "/api/transfers"},
		{"notif-unread", "Notifications", "/api/notifications/unread-count"},
		{"notif-list", "Notifications", "/api/notifications"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, admin := fix24DAdminEnv(t, false)
			fix24DDrop(t, e, tc.table)
			code, body := e.getJSON(admin, tc.path)
			fix24DAssertGeneric500(t, "GET "+tc.path+" after DROP "+tc.table, code, body)
		})
	}
}

// TestFix24D_ShiftsListLiveSummaryDropExpensesGeneric500 covers the
// second 500 branch of apiShiftsList: with an open shift, dropping
// Expenses breaks ApplyLiveSummaryToOpenShifts (which calls the live
// ShiftSummary), and the list must answer 500 generic, not raw.
func TestFix24D_ShiftsListLiveSummaryDropExpensesGeneric500(t *testing.T) {
	e, _ := fix24DAdminEnv(t, false)
	mgr := e.managerClient()
	e.openShift(mgr, 100000)
	fix24DDrop(t, e, "Expenses")
	code, body := e.getJSON(mgr, "/api/shifts?date="+store.Today())
	fix24DAssertGeneric500(t, "shifts list after DROP Expenses (live-summary leg)", code, body)
}

// TestFix24D_BackupNowBlockedDirGeneric500 covers apiBackupNow: with a
// regular file standing where the backups directory must be created,
// CreateBackup fails with a raw OS error ("mkdir ...: not a directory"),
// and the endpoint must answer 500 generic, not the raw path error.
func TestFix24D_BackupNowBlockedDirGeneric500(t *testing.T) {
	e, admin := fix24DAdminEnv(t, true)
	// NOTE: t.TempDir() returns a fresh directory per call, so the DB's
	// directory must come from the store itself, not from TempDir.
	dir := filepath.Dir(e.store.Path)
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("plant backups file: %v", err)
	}
	code, body := e.postJSON(admin, "/api/backup/now", nil)
	fix24DAssertGeneric500(t, "POST /api/backup/now with blocked backups dir", code, body)
}
