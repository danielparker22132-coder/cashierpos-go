// Package web_test — parity-fix UI tests (PR-M1, PR-M2, PR-L1).
//
// PR-M1: a cashier with no open shift sees all 4 hub stat cards (like
// PyQt design D), the shift-scope cards at 0.00 / 0, and latest-3
// scoped to their OWN invoices today — never day totals or another
// cashier's invoices (§15 row 6 / M4).
// PR-M2: the global-nav web-orders badges hide at 0 (like the topbar
// bells and the banner) and the 5 s poll toggles visibility.
// PR-L1: the low-stock sentence occurs exactly once on the hub — in
// the persistent alerts strip, not also in the dismissible alert.
package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestParityFixUI_ShiftlessCashierFourCardsAndOwnLatest(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()
	cash := e.cashierClient()

	pid := e.createProduct(admin, "PFX001", "صنف باريتي", "10.00", "100")

	// The seeded cashier sells twice, then closes the shift: now
	// shift-less (closed_today) while invoices from today exist.
	e.openShift(cash, 0)
	_, num1 := e.cashSale(cash, pid, "1", 1000)
	_, num2 := e.cashSale(cash, pid, "1", 1000)
	code, closeResp := e.postJSON(cash, "/api/shifts/close", map[string]any{})
	expectOK(t, "close shift", code, closeResp)

	// A second cashier sells once today (open shift) — their invoice
	// must never leak into the first cashier's hub.
	mgr := e.managerClient()
	code, loc, _ := e.postFormLoc(mgr, "/users/new", url.Values{
		"username": {"cash2"}, "password": {"Cash2Pass123"},
		"full_name": {"كاشير ثان"}, "role": {"cashier"}, "is_active": {"1"},
	})
	if code != http.StatusSeeOther || loc != "/users" {
		t.Fatalf("create cash2: want 303 to /users, got %d loc %q", code, loc)
	}
	cash2 := e.loginAs("cash2", "Cash2Pass123")
	e.openShift(cash2, 0)
	_, numOther := e.cashSale(cash2, pid, "1", 1000)

	// Shift-less cashier hub: all 4 stat cards, shift-scope at zero.
	body := hubBody(t, e, cash)
	for _, label := range []string{
		"مبيعات شيفتي", "فواتير شيفتي", "رصيد الدرج", "طلبات متجر معلقة",
	} {
		if !strings.Contains(body, `<span class="summary-label">`+label+`</span>`) {
			t.Errorf("shift-less cashier hub missing stat card %q", label)
		}
	}
	if n := strings.Count(body, `class="summary-box"`); n != 4 {
		t.Errorf("shift-less cashier hub: want 4 summary boxes, got %d", n)
	}
	if !strings.Contains(body, `<span class="summary-label">مبيعات شيفتي</span><span class="summary-value num">0.00 ج.م</span>`) {
		t.Error("shift-less cashier: shift sales card must show 0.00 ج.م")
	}
	if !strings.Contains(body, `<span class="summary-label">فواتير شيفتي</span><span class="summary-value num">0</span>`) {
		t.Error("shift-less cashier: shift invoices card must show 0")
	}
	// No day totals leak: the day-scope labels must not render at all.
	for _, forbidden := range []string{"مبيعات اليوم", "عدد فواتير اليوم"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("shift-less cashier hub leaks day scope %q", forbidden)
		}
	}
	// Latest-3: the cashier's own invoices today, not the empty state
	// and not the other cashier's invoice.
	if strings.Contains(body, "لا توجد فواتير اليوم") {
		t.Error("shift-less cashier hub must not show the empty latest-invoices state while their own invoices exist today")
	}
	for _, n := range []int64{num1, num2} {
		if !strings.Contains(body, fmt.Sprintf("فاتورة %d —", n)) {
			t.Errorf("shift-less cashier latest-3 must show own invoice %d", n)
		}
	}
	if strings.Contains(body, fmt.Sprintf("فاتورة %d —", numOther)) {
		t.Errorf("shift-less cashier latest-3 must not show the other cashier's invoice %d", numOther)
	}

	// Contrast: the second cashier (open shift) sees only their own
	// shift's invoice; the admin (day scope) sees the newest 3 of all.
	body2 := hubBody(t, e, cash2)
	if !strings.Contains(body2, fmt.Sprintf("فاتورة %d —", numOther)) {
		t.Errorf("open-shift cash2 latest-3 must show own invoice %d", numOther)
	}
	if strings.Contains(body2, fmt.Sprintf("فاتورة %d —", num1)) {
		t.Errorf("open-shift cash2 latest-3 must not show the first cashier's invoice %d", num1)
	}
	adminBody := hubBody(t, e, admin)
	for _, n := range []int64{numOther, num2, num1} {
		if !strings.Contains(adminBody, fmt.Sprintf("فاتورة %d —", n)) {
			t.Errorf("admin latest-3 must show invoice %d", n)
		}
	}
}

func TestParityFixUI_NavBadgesHiddenAtZeroAndPollToggles(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Fresh DB, no pending orders/evidence: both nav badges hidden for
	// admin, manager AND cashier (the poll JS toggles the same spans).
	for name, c := range map[string]*http.Client{
		"admin": admin, "manager": e.managerClient(), "cashier": e.cashierClient(),
	} {
		body := hubBody(t, e, c)
		if !strings.Contains(body, `hidden id="orders-badge"`) {
			t.Errorf("%s hub: orders badge must be hidden at 0", name)
		}
		if !strings.Contains(body, `hidden id="orders-evidence-badge"`) {
			t.Errorf("%s hub: evidence badge must be hidden at 0", name)
		}
		// The nav link itself still renders (only the counts hide).
		if !strings.Contains(body, "طلبات المتجر") {
			t.Errorf("%s hub missing the orders nav link", name)
		}
	}
	// The 5 s poll toggles visibility instead of only rewriting text.
	body := hubBody(t, e, admin)
	for _, want := range []string{
		"badge.hidden=n===0", "evBadge.hidden=e===0", "setInterval(poll,5000)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hub poll script missing %q", want)
		}
	}

	// One pending order + one awaiting-evidence order: badges appear.
	for _, req := range []map[string]any{
		{"customer_name": "عميل معلق", "status": "CREATED", "payment_status": "PENDING", "total": 5000,
			"items": []map[string]any{{"name": "سكر", "qty": "1", "unit_price": 5000}}},
		{"customer_name": "عميل دليل", "status": "PREPARED", "payment_status": "AWAITING_VERIFICATION", "total": 3000,
			"items": []map[string]any{{"name": "أرز", "qty": "2", "unit_price": 1500}}},
	} {
		code, resp := e.postJSON(admin, "/api/staff/orders", req)
		if code != http.StatusCreated {
			t.Fatalf("intake order: want 201, got %d %v", code, resp)
		}
	}
	body = hubBody(t, e, admin)
	if strings.Contains(body, `hidden id="orders-badge"`) {
		t.Error("orders badge must be visible with 2 pending orders")
	}
	if !strings.Contains(body, `id="orders-badge">2<`) {
		t.Error("orders badge must show 2 with pending orders")
	}
	if !strings.Contains(body, `id="orders-evidence-badge">1<`) {
		t.Error("evidence badge must show 1 with an awaiting-evidence order")
	}

	// Clear both orders (cancel): the poll endpoints report 0 and the
	// badges hide again on the next render.
	var ids []int64
	if rows, err := e.store.DB.Query(`SELECT OrderID FROM OnlineOrders ORDER BY OrderID`); err != nil {
		t.Fatalf("list orders: %v", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
	}
	for _, id := range ids {
		code, resp := e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", id), map[string]any{"status": "CANCELED"})
		if code != http.StatusOK {
			t.Fatalf("cancel order %d: want 200, got %d %v", id, code, resp)
		}
	}
	if code, resp := e.getJSON(admin, "/api/staff/orders/pending-count"); code != http.StatusOK || resp["pendingCount"] != float64(0) {
		t.Fatalf("pending-count after clear: want 200/0, got %d %v", code, resp)
	}
	if code, resp := e.getJSON(admin, "/api/staff/orders/pending-evidence"); code != http.StatusOK || resp["pendingEvidence"] != float64(0) {
		t.Fatalf("pending-evidence after clear: want 200/0, got %d %v", code, resp)
	}
	body = hubBody(t, e, admin)
	if !strings.Contains(body, `hidden id="orders-badge"`) {
		t.Error("orders badge must hide again after clearing pending orders")
	}
	if !strings.Contains(body, `hidden id="orders-evidence-badge"`) {
		t.Error("evidence badge must hide again after clearing pending evidence")
	}
}

func TestParityFixUI_LowStockSentenceOnce(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// Quiet zero state: the sentence occurs nowhere, no strip at all.
	if body := hubBody(t, e, admin); strings.Count(body, "صنف وصل للحد الأدنى في المخزون") != 0 {
		t.Error("fresh hub must not contain the low-stock sentence")
	} else if strings.Contains(body, `id="hub-alerts-strip"`) {
		t.Error("hub strip must be hidden entirely at 0")
	}

	// Seed a product at/below minimum: the sentence occurs exactly 1×,
	// in the persistent strip — the dismissible alert keeps a shorter
	// wording and stays dismissible.
	pid := e.createProduct(admin, "PFXLOW", "صنف منخفض", "10.00", "5")
	if _, err := e.store.DB.Exec(`UPDATE Products SET MinQuantity = Quantity + 1000 WHERE ProductID=?`, pid); err != nil {
		t.Fatalf("force low stock: %v", err)
	}
	body := hubBody(t, e, admin)
	if n := strings.Count(body, "صنف وصل للحد الأدنى في المخزون"); n != 1 {
		t.Errorf("low-stock sentence: want exactly 1 occurrence, got %d", n)
	}
	if !strings.Contains(body, `id="hub-alerts-strip"`) {
		t.Error("hub must keep the persistent alerts strip with the low-stock sentence")
	}
	if !strings.Contains(body, `id="hub-lowstock-alert"`) {
		t.Error("hub must keep the dismissible low-stock alert")
	}
	if !strings.Contains(body, `data-dismiss="hub-lowstock-alert"`) {
		t.Error("dismissible low-stock alert must keep its dismiss button")
	}
	// The alert itself must not repeat the sentence: extract it and check.
	start := strings.Index(body, `id="hub-lowstock-alert"`)
	if start < 0 {
		t.Fatal("hub-lowstock-alert not found")
	}
	seg := body[start:]
	if end := strings.Index(seg, "</div>"); end >= 0 {
		seg = seg[:end]
	}
	if strings.Contains(seg, "صنف وصل للحد الأدنى في المخزون") {
		t.Error("dismissible low-stock alert still repeats the strip sentence")
	}
}
