// Package web_test — FIX-17 lane C regression tests.
//
// SW2-M01: GET /api/shifts/summary?shift_id=<other> must deny a caller
// who is neither the shift owner nor a view-all-cashbox holder (403).
// SW2-L09: cross-user cash-count list denial must be 403 (not 400);
// missing shift_id stays 400.
// Expense-delete ruling (parent, 2026-11-26): delete needs the create
// perm AND (own expense OR Manager/Admin) — API 403, row intact.
package web_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestFix17_ShiftSummaryCrossUserDenied(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	mgr := e.managerClient()
	cash := e.cashierClient()
	wh := e.warehouseClient()
	adminShift := e.openShift(admin, 10000)
	cashShift := e.openShift(cash, 10000)

	// Cashier asking for the admin's shift: 403.
	code, body := e.getJSON(cash, fmt.Sprintf("/api/shifts/summary?shift_id=%d", adminShift))
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier cross-user summary: want 403 ok=false, got %d body=%v", code, body)
	}
	// Warehouse asking for the admin's shift: 403.
	code, body = e.getJSON(wh, fmt.Sprintf("/api/shifts/summary?shift_id=%d", adminShift))
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("warehouse cross-user summary: want 403 ok=false, got %d body=%v", code, body)
	}
	// Cashier asking for their OWN shift: 200.
	code, body = e.getJSON(cash, fmt.Sprintf("/api/shifts/summary?shift_id=%d", cashShift))
	expectOK(t, "cashier own summary", code, body)
	// Manager (counts as admin) asking for any shift: 200.
	code, body = e.getJSON(mgr, fmt.Sprintf("/api/shifts/summary?shift_id=%d", adminShift))
	expectOK(t, "manager any summary", code, body)
	code, body = e.getJSON(mgr, fmt.Sprintf("/api/shifts/summary?shift_id=%d", cashShift))
	expectOK(t, "manager cashier summary", code, body)
	// Admin asking for the cashier's shift: 200.
	code, body = e.getJSON(admin, fmt.Sprintf("/api/shifts/summary?shift_id=%d", cashShift))
	expectOK(t, "admin cashier summary", code, body)
	// No shift_id still resolves to the caller's own open shift: 200.
	code, body = e.getJSON(cash, "/api/shifts/summary")
	expectOK(t, "cashier summary default own", code, body)
}

func TestFix17_CashCountsListDenialIs403(t *testing.T) {
	e := readyEnv(t, false)
	cash := e.cashierClient()
	wh := e.warehouseClient()
	cashShift := e.openShift(cash, 10000)

	// Warehouse asking for the cashier's shift: 403 (was 400).
	code, body := e.getJSON(wh, fmt.Sprintf("/api/cashbox/counts?shift_id=%d", cashShift))
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("warehouse cross-user counts: want 403 ok=false, got %d body=%v", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "غير مسموح") || !strings.Contains(msg, "لا يمكن عرض عدّ درج مستخدم آخر") {
		t.Fatalf("cross-user counts error = %q, want the uniform denial text", msg)
	}
	// Cashier asking for their own shift: 200.
	code, body = e.getJSON(cash, fmt.Sprintf("/api/cashbox/counts?shift_id=%d", cashShift))
	expectOK(t, "cashier own counts", code, body)
	// Missing shift_id stays 400 with the required-message.
	code, body = e.getJSON(wh, "/api/cashbox/counts")
	if code != http.StatusBadRequest || body["ok"] != false {
		t.Fatalf("missing shift_id: want 400 ok=false, got %d body=%v", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "رقم الشيفت مطلوب") {
		t.Fatalf("missing shift_id error = %q, want رقم الشيفت مطلوب", msg)
	}
}

// deleteJSON issues a DELETE with the client's session cookies and
// returns status + decoded body.
func (e *e2eEnv) deleteJSON(c *http.Client, path string) (int, map[string]any) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodDelete, e.base+path, nil)
	if err != nil {
		e.t.Fatalf("new DELETE %s: %v", path, err)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("DELETE %s: status %d, body is not JSON: %q", path, resp.StatusCode, truncate(string(b), 300))
	}
	return resp.StatusCode, out
}

func TestFix17_ExpenseDeleteOwnership(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	mgr := e.managerClient()
	cash := e.cashierClient()
	e.openShift(admin, 10000)
	e.openShift(cash, 10000)

	// Admin creates an expense.
	code, body := e.postJSON(admin, "/api/expenses", map[string]any{
		"amount_cents": 1000, "description": "مصروف المدير", "category": "عام",
	})
	expectOK(t, "admin create expense", code, body)
	adminExp, _ := body["expense_id"].(float64)

	// Cashier deleting the admin's expense via API: 403, row intact.
	code, body = e.deleteJSON(cash, fmt.Sprintf("/api/expenses/%d", int64(adminExp)))
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier deleting admin expense via API: want 403 ok=false, got %d body=%v", code, body)
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM Expenses WHERE ExpenseID=?`, int64(adminExp)); got != 1 {
		t.Fatalf("denied delete removed the row: count=%d, want 1", got)
	}
	// Same denial via the HTML form: error page, row still intact.
	code, htmlBody := e.postForm(cash, fmt.Sprintf("/expenses/%d/delete", int64(adminExp)), url.Values{})
	if code != http.StatusOK || !strings.Contains(htmlBody, "لا يمكن حذف مصروف مستخدم آخر") {
		t.Fatalf("cashier deleting admin expense via form: want 200 page with the denial, got %d body=%.200q", code, htmlBody)
	}
	if got := e.queryInt(`SELECT COUNT(*) FROM Expenses WHERE ExpenseID=?`, int64(adminExp)); got != 1 {
		t.Fatalf("denied form delete removed the row: count=%d, want 1", got)
	}
	// Cashier deleting their OWN expense via API: 200.
	code, body = e.postJSON(cash, "/api/expenses", map[string]any{
		"amount_cents": 500, "description": "مصروف الكاشير", "category": "عام",
	})
	expectOK(t, "cashier create expense", code, body)
	ownExp, _ := body["expense_id"].(float64)
	code, body = e.deleteJSON(cash, fmt.Sprintf("/api/expenses/%d", int64(ownExp)))
	expectOK(t, "cashier delete own expense", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM Expenses WHERE ExpenseID=?`, int64(ownExp)); got != 0 {
		t.Fatalf("own expense delete left the row: count=%d, want 0", got)
	}
	// Manager may delete any expense (the admin's).
	code, body = e.deleteJSON(mgr, fmt.Sprintf("/api/expenses/%d", int64(adminExp)))
	expectOK(t, "manager delete admin expense", code, body)
	if got := e.queryInt(`SELECT COUNT(*) FROM Expenses WHERE ExpenseID=?`, int64(adminExp)); got != 0 {
		t.Fatalf("manager delete left the row: count=%d, want 0", got)
	}
}
