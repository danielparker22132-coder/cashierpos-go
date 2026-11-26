// Package web_test — FIX-3 lane 1 (store/logic) HTTP tests: M4 invoice
// scoping, M1 manager self-review, L4 custom-filter bounds, L6
// product_price repricing at insert.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestFix3Lane1_InvoiceScopingHTTP(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "L1-SCOPE-1", "منتج نطاق", "10.00", "10")
	cash1 := e.cashierClient()
	e.openShift(cash1, 0)
	invID, _ := e.cashSale(cash1, pid, "1", 1000)

	seedCashier2(t, e)
	cash2 := e.loginAs("cashier2", cashierPass)

	// Another cashier: API 403, HTML 403.
	if code, body := e.getJSON(cash2, fmt.Sprintf("/api/sales/%d", invID)); code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier2 api get: want 403 ok=false, got %d %v", code, body)
	}
	if code, body := e.get(cash2, fmt.Sprintf("/sales/%d", invID)); code != http.StatusForbidden {
		t.Fatalf("cashier2 html get: want 403, got %d body=%q", code, truncate(body, 200))
	}
	// Warehouse: API 403 as well.
	wh := e.warehouseClient()
	if code, _ := e.getJSON(wh, fmt.Sprintf("/api/sales/%d", invID)); code != http.StatusForbidden {
		t.Fatalf("warehouse api get: want 403, got %d", code)
	}
	// Supervisor and admin may read any invoice.
	sup := e.supClient()
	if code, body := e.getJSON(sup, fmt.Sprintf("/api/sales/%d", invID)); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("supervisor api get: want 200 ok=true, got %d %v", code, body)
	}
	admin := e.adminClient()
	if code, _ := e.get(admin, fmt.Sprintf("/sales/%d", invID)); code != http.StatusOK {
		t.Fatalf("admin html get: want 200, got %d", code)
	}
	// The owner themself may read it.
	if code, body := e.getJSON(cash1, fmt.Sprintf("/api/sales/%d", invID)); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("owner api get: want 200 ok=true, got %d %v", code, body)
	}
	// A missing invoice is still a 404, not a 403.
	if code, _ := e.getJSON(cash2, "/api/sales/999999"); code != http.StatusNotFound {
		t.Fatalf("missing invoice: want 404, got %d", code)
	}
}

func TestFix3Lane1_ManagerSelfReviewAllowedHTTP(t *testing.T) {
	// C-3 (jony's ruling 2026-11-25, reversing FIX-3 M1 for
	// Manager/Admin only): a manager MAY approve their own count
	// over HTTP — 303 to /shifts, count approved, exactly one
	// approve audit row, no self-review-attempt row.
	e := readyEnv(t, false)
	mgr := e.managerClient()
	shiftID := e.openShift(mgr, 10000)
	countID := recordCount(t, e, mgr, shiftID, 9000)
	code, loc, _ := e.postFormLoc(mgr, "/cashbox/count/"+strconv.FormatInt(countID, 10)+"/review",
		url.Values{"approve": {"1"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/shifts") {
		t.Fatalf("manager self-review: want 303 to /shifts, got %d loc=%q", code, loc)
	}
	if st := reviewStatus(t, e, countID); st != "approved" {
		t.Fatalf("self-reviewed count must be approved, got %q", st)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType IN ('اعتماد فرق عدّ النقدية','رفض فرق عدّ النقدية')`); n != 1 {
		t.Fatalf("self-approval must write exactly one approve/reject row, got %d", n)
	}
	if n := e.queryInt(`SELECT COUNT(*) FROM AuditLog WHERE ActionType='محاولة اعتماد عدّ خاص'`); n != 0 {
		t.Fatalf("self-approval must write no attempt audit rows, got %d", n)
	}
}

func TestFix3Lane1_CashcountLogCustomMissingBound(t *testing.T) {
	// L4: a custom filter with a missing bound surfaces the store
	// error instead of silently defaulting to today.
	e := readyEnv(t, false)
	mgr := e.managerClient()
	today := cairoNow().Format("2006-01-02")
	if code, body := e.get(mgr, "/cashcount-approvals?mode=custom&from="+today); code != http.StatusOK ||
		!strings.Contains(body, "يجب تحديد تاريخ البداية وتاريخ النهاية معاً") {
		t.Fatalf("custom from-only: want the store error, got %d body=%q", code, truncate(body, 300))
	}
	if code, body := e.get(mgr, "/cashcount-approvals?mode=custom&to="+today); code != http.StatusOK ||
		!strings.Contains(body, "يجب تحديد تاريخ البداية وتاريخ النهاية معاً") {
		t.Fatalf("custom to-only: want the store error, got %d body=%q", code, truncate(body, 300))
	}
	// Both bounds present still works (empty result, no error).
	if code, body := e.get(mgr, "/cashcount-approvals?mode=custom&from="+today+"&to="+today); code != http.StatusOK ||
		strings.Contains(body, "يجب تحديد تاريخ البداية") {
		t.Fatalf("custom both bounds: want 200 without the error, got %d body=%q", code, truncate(body, 300))
	}
}

func TestFix3Lane1_PromoPriceRepricedLineHTTP(t *testing.T) {
	// L6 over HTTP: the stored line carries the promo price, while
	// net/saved/InvoiceOffers are unchanged (net 7000, saved 3000).
	e := readyEnv(t, false)
	admin := e.adminClient()
	pid := e.createProduct(admin, "L1-PP-1", "سكر برومو خط", "50.00", "100")
	code, loc, body := e.promoPriceForm(admin, pid, "سعر ترويجي خط", "35.00", "")
	if code != http.StatusSeeOther || loc != "/promotions" {
		t.Fatalf("create offer: want 303 to /promotions, got %d loc=%q body=%q", code, loc, truncate(body, 300))
	}
	e.openShift(admin, 0)
	invID, _ := e.cashSale(admin, pid, "2", 7000)
	if got := e.queryInt(`SELECT UnitPrice FROM InvoiceDetails WHERE InvoiceID=?`, invID); got != 3500 {
		t.Fatalf("stored line UnitPrice = %d, want 3500 (promo price)", got)
	}
	if got := e.queryInt(`SELECT TotalPrice FROM InvoiceDetails WHERE InvoiceID=?`, invID); got != 7000 {
		t.Fatalf("stored line TotalPrice = %d, want 7000", got)
	}
	if got := e.queryInt(`SELECT PromoDiscount FROM InvoiceDetails WHERE InvoiceID=?`, invID); got != 0 {
		t.Fatalf("stored line PromoDiscount = %d, want 0 (repriced, not discounted)", got)
	}
	if got := e.queryInt(`SELECT TotalAmount FROM Invoices WHERE InvoiceID=?`, invID); got != 7000 {
		t.Fatalf("header TotalAmount = %d, want 7000", got)
	}
	if got := e.queryInt(`SELECT NetAmount FROM Invoices WHERE InvoiceID=?`, invID); got != 7000 {
		t.Fatalf("net = %d, want 7000", got)
	}
	if got := e.queryInt(`SELECT COALESCE(SUM(SavedAmount),0) FROM InvoiceOffers WHERE InvoiceID=?`, invID); got != 3000 {
		t.Fatalf("saved = %d, want 3000", got)
	}
	// The API read shows the promo price on the line too.
	code, data := e.getJSON(admin, fmt.Sprintf("/api/sales/%d", invID))
	if code != http.StatusOK {
		t.Fatalf("api get: want 200, got %d %v", code, data)
	}
	lines, _ := data["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("api lines = %v, want 1", data["lines"])
	}
	line := lines[0].(map[string]any)
	if line["unit_price"] != float64(3500) || line["total"] != float64(7000) {
		t.Fatalf("api line unit/total = %v/%v, want 3500/7000", line["unit_price"], line["total"])
	}
}
