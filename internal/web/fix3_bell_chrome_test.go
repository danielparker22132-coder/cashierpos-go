package web_test

import (
	"strings"
	"testing"
)

// FIX-3 H1 (coordinator follow-up): the bell endpoints existed but the
// shared chrome had no nav item, no live banner and no 5s polling — the
// orders page even claimed the bell "updates every 5 seconds" while
// nothing polled. This test pins the chrome wiring.
func TestFix3_WebBellChrome(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	// No orders yet: nav item + badges + banner + polling script are all
	// present (banner hidden at zero, like the M8 bells' quiet zero state).
	code, body := e.get(admin, "/")
	if code != 200 {
		t.Fatalf("GET / as admin: want 200, got %d", code)
	}
	for _, want := range []string{
		"طلبات المتجر",
		`id="orders-badge"`,
		`id="orders-evidence-badge"`,
		`id="orders-banner"`,
		"setInterval(poll,5000)",
		"/api/staff/orders/pending-count",
		"/api/staff/orders/pending-evidence",
		"orders-refresh-now",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hub chrome missing %q", want)
		}
	}
	if !strings.Contains(body, `id="orders-badge">0<`) {
		t.Errorf("pending badge should render quiet zero, body has no orders-badge>0")
	}

	// One pending order + one awaiting-evidence order, via the intake API.
	for _, req := range []map[string]any{
		{"customer_name": "عميل اختبار", "status": "CREATED", "payment_status": "PENDING", "total": 5000,
			"items": []map[string]any{{"name": "سكر", "qty": "1", "unit_price": 5000}}},
		{"customer_name": "عميل دليل", "status": "PREPARED", "payment_status": "AWAITING_VERIFICATION", "total": 3000,
			"items": []map[string]any{{"name": "أرز", "qty": "2", "unit_price": 1500}}},
	} {
		code, resp := e.postJSON(admin, "/api/staff/orders", req)
		if code != 201 {
			t.Fatalf("intake order: want 201, got %d %v", code, resp)
		}
	}

	// Counts now flow into the chrome: pending 2, evidence 1, banner shown.
	code, body = e.get(admin, "/")
	if code != 200 {
		t.Fatalf("GET / as admin after orders: want 200, got %d", code)
	}
	if !strings.Contains(body, `id="orders-badge">2<`) {
		t.Errorf("pending badge should show 2")
	}
	if !strings.Contains(body, `id="orders-evidence-badge">1<`) {
		t.Errorf("evidence badge should show 1")
	}
	if strings.Contains(body, `id="orders-banner" class="alert warn" role="alert" style="display:none"`) {
		t.Errorf("banner should be visible with pending orders")
	}
	if !strings.Contains(body, `id="orders-banner-count">2<`) {
		t.Errorf("banner should show 2 pending orders")
	}

	// A cashier sees the same bell chrome (staff bell, not admin-only).
	cash := e.cashierClient()
	code, body = e.get(cash, "/")
	if code != 200 {
		t.Fatalf("GET / as cashier: want 200, got %d", code)
	}
	for _, want := range []string{"طلبات المتجر", `id="orders-badge">2<`, `id="orders-banner"`} {
		if !strings.Contains(body, want) {
			t.Errorf("cashier hub chrome missing %q", want)
		}
	}
}
