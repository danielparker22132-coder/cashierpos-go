// Package web_test — FIX-12 lane D (reports / audit / shifts) tests.
//
// SW-M04 shifts error renders, SW-M08 shift-monitor CSV export,
// SW-L08 service-layer denials answer 403.
package web_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

// TestFix12ShiftDenialRendersMessage is the SW-M04 repro: cashier1 GETs
// another user's shift — the intended denial message must render, not a
// 500 "خطأ في عرض الصفحة".
func TestFix12ShiftDenialRendersMessage(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 10000)
	cash := e.cashierClient()
	code, body := e.get(cash, "/shifts/"+strconv.FormatInt(shiftID, 10))
	if code != http.StatusOK {
		t.Fatalf("GET /shifts/%d as cashier = %d, want 200", shiftID, code)
	}
	if strings.Contains(body, "خطأ في عرض الصفحة") {
		t.Fatalf("denial page rendered the 500 template error; body=%q", truncate(body, 300))
	}
	if !strings.Contains(body, "لا يمكن عرض شيفت مستخدم آخر") {
		t.Fatalf("denial message missing; body=%q", truncate(body, 300))
	}
}

// TestFix12ShiftsCSV requires ?format=csv on GET /api/shifts to return CSV
// content with the shift rows instead of JSON (SW-M08).
func TestFix12ShiftsCSV(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	adm := e.adminClient()
	shiftID := e.openShift(adm, 10000)
	code, body := e.get(adm, "/api/shifts?date="+store.Today()+"&format=csv")
	if code != http.StatusOK {
		t.Fatalf("GET /api/shifts?format=csv = %d, want 200", code)
	}
	if !strings.Contains(body, "الكاشير") || !strings.Contains(body, "admin") {
		t.Fatalf("CSV body missing header/user; body=%q", truncate(body, 300))
	}
	if !strings.Contains(body, strconv.FormatInt(shiftID, 10)) {
		t.Fatalf("CSV body missing shift %d; body=%q", shiftID, truncate(body, 300))
	}
	// JSON control: without format=csv the same endpoint stays JSON.
	code, jsonBody := e.getJSON(adm, "/api/shifts?date="+store.Today())
	if code != http.StatusOK || jsonBody["shifts"] == nil {
		t.Fatalf("GET /api/shifts JSON = %d, want 200 with shifts", code)
	}
}

// TestFix12ShiftMonitorCSV requires the monitor export to return CSV with
// the open shift and the performance row (SW-M08).
func TestFix12ShiftMonitorCSV(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	adm := e.adminClient()
	_ = e.openShift(adm, 10000)
	prod := e.createProduct(adm, "1001", "سكر", "15.00", "100")
	e.cashSale(adm, prod, "1", 1500)
	code, body := e.get(adm, "/shifts/monitor/export?from="+store.Today()+"&to="+store.Today())
	if code != http.StatusOK {
		t.Fatalf("GET /shifts/monitor/export = %d, want 200", code)
	}
	if !strings.Contains(body, "شيفت مفتوح") || !strings.Contains(body, "admin") {
		t.Fatalf("monitor CSV missing open-shift section; body=%q", truncate(body, 400))
	}
	if !strings.Contains(body, "أداء") {
		t.Fatalf("monitor CSV missing performance section; body=%q", truncate(body, 400))
	}
	// The monitor page itself links the export.
	_, page := e.get(adm, "/shifts/monitor")
	if !strings.Contains(page, "/shifts/monitor/export") {
		t.Fatalf("monitor page has no CSV export link")
	}
}

// TestFix12CloseOtherShift403 requires closing another user's shift via
// the API to answer 403 (not 400) with the uniform denial text (SW-L08).
func TestFix12CloseOtherShift403(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 10000)
	cash := e.cashierClient()
	code, body := e.postJSON(cash, "/api/shifts/close", map[string]any{"shift_id": shiftID})
	if code != http.StatusForbidden {
		t.Fatalf("POST /api/shifts/close (other's shift) = %d, want 403; body=%v", code, body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "غير مسموح") {
		t.Fatalf("denial text = %q, want the uniform 'غير مسموح' message", msg)
	}
}

// TestFix12SelfReview403 requires a cashier self-reviewing their own cash
// count via the API to answer 403 (not 400) (SW-L08).
func TestFix12SelfReview403(t *testing.T) {
	e := newEnv(t, false)
	e.seedUsers()
	e.changeAdminPassword()
	cash := e.cashierClient()
	shiftID := e.openShift(cash, 10000)
	code, body := e.postJSON(cash, "/api/cashbox/count",
		map[string]any{"shift_id": shiftID, "amount_cents": 10000})
	if code != http.StatusOK {
		t.Fatalf("POST /api/cashbox/count = %d, want 200; body=%v", code, body)
	}
	countID := e.queryInt(`SELECT CountID FROM CashCounts ORDER BY CountID DESC LIMIT 1`)
	code, body = e.postJSON(cash, "/api/cashbox/count/review",
		map[string]any{"count_id": countID, "approve": true})
	if code != http.StatusForbidden {
		t.Fatalf("POST /api/cashbox/count/review (own count) = %d, want 403; body=%v", code, body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "لا يمكنك اعتماد عدّك الخاص") {
		t.Fatalf("self-review denial text = %q, want the self-review message", msg)
	}
	// FIX-13 I2 (mandated spec change): the denial must now also
	// carry the uniform "غير مسموح" prefix, like every other denial.
	if !strings.Contains(msg, "غير مسموح") {
		t.Fatalf("self-review denial text = %q, want the uniform 'غير مسموح' prefix", msg)
	}
}
