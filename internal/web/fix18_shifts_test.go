// Package web_test — FIX-18 lane C regression tests.
//
// R18-C1: POST /api/cashbox/count cross-user denial must be 403 (was 400)
// with the uniform "غير مسموح — …" denial text; own-shift count still 200.
// R18-C2: GET /api/cashbox/counts?shift_id=<missing> must be 404 (was 400),
// like apiShiftSummary for the same shift; missing shift_id stays 400 and
// the cross-user list denial stays 403.
// O-C2 (parent ruling): UpdateExpense now needs the create perm AND (own
// expense OR Manager/Admin) — API 403 + HTML form denial, row intact;
// own/other x cashier/manager + supervisor-other verified.
package web_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/store"
)

// putJSON issues a PUT with the client's session cookies and returns
// status + decoded body (mirror of postJSON for the expense update API).
func (e *e2eEnv) putJSON(c *http.Client, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	if body == nil {
		buf.WriteString("{}")
	} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
		e.t.Fatalf("encode JSON for %s: %v", path, err)
	}
	req, err := http.NewRequest(http.MethodPut, e.base+path, &buf)
	if err != nil {
		e.t.Fatalf("new PUT %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("PUT %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("PUT %s: status %d, body is not JSON: %q", path, resp.StatusCode, truncate(string(b), 300))
	}
	return resp.StatusCode, out
}

// expenseDescription reads one expense's description straight from the DB.
func (e *e2eEnv) expenseDescription(id int64) string {
	e.t.Helper()
	var d string
	if err := e.store.DB.QueryRow(`SELECT Description FROM Expenses WHERE ExpenseID=?`, id).Scan(&d); err != nil {
		e.t.Fatalf("read expense %d: %v", id, err)
	}
	return d
}

// secondCashierClient seeds a second cashier (cashier2) and logs in as them.
func (e *e2eEnv) secondCashierClient() *http.Client {
	e.t.Helper()
	if _, err := e.store.DB.Exec(
		`INSERT INTO Users(UserName,Password,FullName,Role,IsActive,MustChangePassword,CreatedDate)
		 VALUES(?,?,?, ?,1,0,?)`,
		"cashier2", sha256Hex("Cash2Pass123"), "cashier2", "cashier", store.Today()); err != nil {
		e.t.Fatalf("seed cashier2: %v", err)
	}
	return e.loginAs("cashier2", "Cash2Pass123")
}

func TestFix18_CashCountCreateCrossUserIs403(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	mgr := e.managerClient()
	cash := e.cashierClient()
	cash2 := e.secondCashierClient()
	cashShift := e.openShift(cash, 10000)
	cash2Shift := e.openShift(cash2, 10000)

	// cashier2 counting cashier1's shift: 403 with the uniform denial.
	code, body := e.postJSON(cash2, "/api/cashbox/count", map[string]any{
		"shift_id": cashShift, "amount_cents": 10000,
	})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier2 counting cashier1 shift: want 403 ok=false, got %d body=%v", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "غير مسموح") || !strings.Contains(msg, "لا يمكن عدّ درج مستخدم آخر") {
		t.Fatalf("cross-user count error = %q, want the uniform denial text", msg)
	}
	// Own-shift count still works.
	code, body = e.postJSON(cash, "/api/cashbox/count", map[string]any{
		"shift_id": cashShift, "amount_cents": 10000,
	})
	expectOK(t, "cashier own count", code, body)
	code, body = e.postJSON(cash2, "/api/cashbox/count", map[string]any{
		"shift_id": cash2Shift, "amount_cents": 10000,
	})
	expectOK(t, "cashier2 own count", code, body)
	// Manager counting another user's shift: 200.
	code, body = e.postJSON(mgr, "/api/cashbox/count", map[string]any{
		"shift_id": cashShift, "amount_cents": 10000,
	})
	expectOK(t, "manager count on cashier shift", code, body)
	_ = admin
}

func TestFix18_CashCountsListMissingShiftIs404(t *testing.T) {
	e := readyEnv(t, false)
	cash := e.cashierClient()
	wh := e.warehouseClient()
	cashShift := e.openShift(cash, 10000)

	// Nonexistent shift: 404 (was 400), same message as the sibling
	// shift-summary endpoint for the same shift.
	code, body := e.getJSON(cash, "/api/cashbox/counts?shift_id=999999")
	if code != http.StatusNotFound || body["ok"] != false {
		t.Fatalf("counts for missing shift: want 404 ok=false, got %d body=%v", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "الشيفت غير موجود") {
		t.Fatalf("missing-shift counts error = %q, want الشيفت غير موجود", msg)
	}
	code, body = e.getJSON(cash, "/api/shifts/summary?shift_id=999999")
	if code != http.StatusNotFound {
		t.Fatalf("summary for missing shift: want 404, got %d body=%v", code, body)
	}
	// Existing shift still lists counts: 200.
	code, body = e.getJSON(cash, fmt.Sprintf("/api/cashbox/counts?shift_id=%d", cashShift))
	expectOK(t, "cashier own counts", code, body)
	// Missing shift_id still 400.
	code, body = e.getJSON(cash, "/api/cashbox/counts")
	if code != http.StatusBadRequest || body["ok"] != false {
		t.Fatalf("missing shift_id: want 400 ok=false, got %d body=%v", code, body)
	}
	// Cross-user list denial still 403 (L09 guard).
	code, body = e.getJSON(wh, fmt.Sprintf("/api/cashbox/counts?shift_id=%d", cashShift))
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("warehouse cross-user counts: want 403 ok=false, got %d body=%v", code, body)
	}
}

func TestFix18_ExpenseUpdateOwnership(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	mgr := e.managerClient()
	cash := e.cashierClient()
	cash2 := e.secondCashierClient()
	sup := e.supClient()
	e.openShift(admin, 10000)
	e.openShift(cash, 10000)
	e.openShift(cash2, 10000)
	e.openShift(sup, 10000)

	// Admin creates an expense.
	code, body := e.postJSON(admin, "/api/expenses", map[string]any{
		"amount_cents": 1000, "description": "مصروف المدير", "category": "عام",
	})
	expectOK(t, "admin create expense", code, body)
	adminExp, _ := body["expense_id"].(float64)

	update := map[string]any{"amount_cents": 1000, "description": "مخترق", "category": "عام"}

	// Cashier updating the admin's expense via API: 403, row intact.
	code, body = e.putJSON(cash, fmt.Sprintf("/api/expenses/%d", int64(adminExp)), update)
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier updating admin expense via API: want 403 ok=false, got %d body=%v", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "غير مسموح") || !strings.Contains(msg, "لا يمكن تعديل مصروف مستخدم آخر") {
		t.Fatalf("cross-user update error = %q, want the uniform denial text", msg)
	}
	if got := e.expenseDescription(int64(adminExp)); got != "مصروف المدير" {
		t.Fatalf("denied update rewrote the row: description=%q", got)
	}
	// Same denial via the HTML form: error page, row still intact.
	code, htmlBody := e.postForm(cash, fmt.Sprintf("/expenses/%d/edit", int64(adminExp)),
		url.Values{"description": {"مخترق"}, "amount": {"10.00"}, "category": {"عام"}})
	if code != http.StatusOK || !strings.Contains(htmlBody, "لا يمكن تعديل مصروف مستخدم آخر") {
		t.Fatalf("cashier updating admin expense via form: want 200 page with the denial, got %d body=%.200q", code, htmlBody)
	}
	if got := e.expenseDescription(int64(adminExp)); got != "مصروف المدير" {
		t.Fatalf("denied form update rewrote the row: description=%q", got)
	}
	// Second cashier and supervisor updating another's expense: 403.
	code, body = e.putJSON(cash2, fmt.Sprintf("/api/expenses/%d", int64(adminExp)), update)
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier2 updating admin expense: want 403 ok=false, got %d body=%v", code, body)
	}
	code, body = e.putJSON(sup, fmt.Sprintf("/api/expenses/%d", int64(adminExp)), update)
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("supervisor updating admin expense: want 403 ok=false, got %d body=%v", code, body)
	}

	// Cashier updating their OWN expense via API: 200.
	code, body = e.postJSON(cash, "/api/expenses", map[string]any{
		"amount_cents": 500, "description": "مصروف الكاشير", "category": "عام",
	})
	expectOK(t, "cashier create expense", code, body)
	ownExp, _ := body["expense_id"].(float64)
	code, body = e.putJSON(cash, fmt.Sprintf("/api/expenses/%d", int64(ownExp)),
		map[string]any{"amount_cents": 500, "description": "مصروف الكاشير المعدل", "category": "عام"})
	expectOK(t, "cashier update own expense", code, body)
	if got := e.expenseDescription(int64(ownExp)); got != "مصروف الكاشير المعدل" {
		t.Fatalf("own update did not land: description=%q", got)
	}

	// Supervisor updating their OWN expense: 200.
	code, body = e.postJSON(sup, "/api/expenses", map[string]any{
		"amount_cents": 500, "description": "مصروف المشرف", "category": "عام",
	})
	expectOK(t, "supervisor create expense", code, body)
	supExp, _ := body["expense_id"].(float64)
	code, body = e.putJSON(sup, fmt.Sprintf("/api/expenses/%d", int64(supExp)),
		map[string]any{"amount_cents": 500, "description": "مصروف المشرف المعدل", "category": "عام"})
	expectOK(t, "supervisor update own expense", code, body)

	// Manager may update any expense (the admin's): 200.
	code, body = e.putJSON(mgr, fmt.Sprintf("/api/expenses/%d", int64(adminExp)),
		map[string]any{"amount_cents": 1000, "description": "مصروف المدير المعدل", "category": "عام"})
	expectOK(t, "manager update admin expense", code, body)
	if got := e.expenseDescription(int64(adminExp)); got != "مصروف المدير المعدل" {
		t.Fatalf("manager update did not land: description=%q", got)
	}

	// Supervisor updating a CASHIER's expense (other user): 403.
	code, body = e.putJSON(sup, fmt.Sprintf("/api/expenses/%d", int64(ownExp)), update)
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("supervisor updating cashier expense: want 403 ok=false, got %d body=%v", code, body)
	}
	if got := e.expenseDescription(int64(ownExp)); got != "مصروف الكاشير المعدل" {
		t.Fatalf("denied supervisor update rewrote the row: description=%q", got)
	}
}
