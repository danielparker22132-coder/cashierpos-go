// Package web_test — FIX-13 money leg (R13-M1, R13-M2): purchase-detail
// buy price hidden from cashier/warehouse, and delivery confirmation
// requires a courier name.
package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// fix13OrderID creates one online order (2× سكر 2000 + 1× زيت 5000 =
// 9000) as admin and returns its id.
func fix13OrderID(t *testing.T, e *e2eEnv, admin *http.Client) int64 {
	t.Helper()
	code, data := e.postJSON(admin, "/api/staff/orders", map[string]any{
		"customer_name": "عميل أونلاين", "customer_phone": "01000000000",
		"items": []map[string]any{
			{"product_id": 1, "name": "سكر", "qty": "2", "unit_price": 2000},
			{"product_id": 2, "name": "زيت", "qty": "1", "unit_price": 5000},
		},
	})
	if code != http.StatusCreated || data["ok"] != true {
		t.Fatalf("create order: want 201 ok, got %d %v", code, data)
	}
	order, _ := data["order"].(map[string]any)
	return int64(order["order_id"].(float64))
}

func fix13Advance(t *testing.T, e *e2eEnv, admin *http.Client, orderID int64, statuses ...string) {
	t.Helper()
	for _, st := range statuses {
		code, data := e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", orderID), map[string]any{"status": st})
		if code != http.StatusOK {
			t.Fatalf("order status %s: want 200, got %d %v", st, code, data)
		}
	}
}

func fix13OrderStatus(t *testing.T, e *e2eEnv, admin *http.Client, orderID int64) string {
	t.Helper()
	code, data := e.getJSON(admin, fmt.Sprintf("/api/staff/orders/%d", orderID))
	if code != http.StatusOK {
		t.Fatalf("GET order %d: want 200, got %d %v", orderID, code, data)
	}
	order, _ := data["order"].(map[string]any)
	st, _ := order["status"].(string)
	return st
}

func fix13CourierDebtCount(t *testing.T, e *e2eEnv, admin *http.Client) []any {
	t.Helper()
	code, data := e.getJSON(admin, "/api/courier-debts")
	if code != http.StatusOK {
		t.Fatalf("GET /api/courier-debts: %d", code)
	}
	debts, _ := data["debts"].([]any)
	return debts
}

// TestFix13_M1_PurchaseDetailBuyPriceHidden — R13-M1: the purchase-detail
// page hides the buy price (column + value) from cashier/warehouse and
// still shows it to admin/supervisor. Distinctive price 12.34 × 3, so
// the unit price cannot hide inside any total.
func TestFix13_M1_PurchaseDetailBuyPriceHidden(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	pid := e.createProduct(admin, "F13-M1", "سكر", "15.00", "100")
	code, data := e.postJSON(admin, "/api/vendors", map[string]any{"name": "مورد ف13"})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create vendor: want 200 ok, got %d %v", code, data)
	}
	vid, _ := data["vendor_id"].(float64)
	code, data = e.postJSON(admin, "/api/purchases", map[string]any{
		"vendor_id": int64(vid),
		"lines":     []map[string]any{{"product_id": pid, "qty": "3", "price": "12.34"}},
		"paid": "0", "method": "Visa",
	})
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("create purchase: want 200 ok, got %d %v", code, data)
	}
	purchaseID, _ := data["purchase_id"].(float64)
	path := fmt.Sprintf("/purchases/%d", int64(purchaseID))

	for _, tc := range []struct {
		name string
		cl   *http.Client
		want bool // want the buy price visible
	}{
		{"cashier", e.cashierClient(), false},
		{"warehouse", e.warehouseClient(), false},
		{"admin", admin, true},
		{"supervisor", e.supClient(), true},
	} {
		code, html := e.get(tc.cl, path)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: want 200, got %d", tc.name, path, code)
		}
		if !strings.Contains(html, "سكر") {
			t.Fatalf("%s: detail page does not show the purchased product", tc.name)
		}
		hasHeader := strings.Contains(html, "سعر الشراء")
		hasValue := strings.Contains(html, "12.34")
		if tc.want {
			if !hasHeader || !hasValue {
				t.Fatalf("%s: buy price must be visible (header=%v value=%v)", tc.name, hasHeader, hasValue)
			}
		} else if hasHeader || hasValue {
			t.Fatalf("%s: buy price leaked on the detail page (header=%v value=%v)", tc.name, hasHeader, hasValue)
		}
	}
}

// TestFix13_M2_DeliveryWithoutCourierRefused — R13-M2: DELIVERED without
// a courier name is refused (400), the order stays READY, zero debts.
func TestFix13_M2_DeliveryWithoutCourierRefused(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	orderID := fix13OrderID(t, e, admin)
	fix13Advance(t, e, admin, orderID, "PREPARED", "READY")

	code, data := e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", orderID), map[string]any{
		"status": "DELIVERED",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("DELIVERED without courier_name: want 400, got %d %v", code, data)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "اسم المندوب") {
		t.Fatalf("refusal must name the missing courier, got error=%q", msg)
	}
	// Whitespace-only name is the same refusal.
	code, data = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", orderID), map[string]any{
		"status": "DELIVERED", "courier_name": "   ",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("DELIVERED with blank courier_name: want 400, got %d %v", code, data)
	}
	if st := fix13OrderStatus(t, e, admin, orderID); st != "READY" {
		t.Fatalf("order status after refused confirm = %s, want READY", st)
	}
	if debts := fix13CourierDebtCount(t, e, admin); len(debts) != 0 {
		t.Fatalf("courier debts after refused confirm = %d, want 0", len(debts))
	}
}

// TestFix13_M2_DeliveryWithCourierBooksExactlyOneDebt — R13-M2: DELIVERED
// with a name books exactly one debt of the order total (9000), and a
// double confirmation still books exactly one.
func TestFix13_M2_DeliveryWithCourierBooksExactlyOneDebt(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	orderID := fix13OrderID(t, e, admin)
	fix13Advance(t, e, admin, orderID, "PREPARED", "READY")

	code, data := e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", orderID), map[string]any{
		"status": "DELIVERED", "courier_name": "مندوب التوصيل",
	})
	if code != http.StatusOK {
		t.Fatalf("DELIVERED with courier_name: want 200, got %d %v", code, data)
	}
	if debtID, _ := data["courier_debt_id"].(float64); debtID == 0 {
		t.Fatalf("delivery confirmation booked no courier debt: %v", data)
	}
	debts := fix13CourierDebtCount(t, e, admin)
	if len(debts) != 1 {
		t.Fatalf("courier debts = %d, want 1", len(debts))
	}
	if m, ok := debts[0].(map[string]any); ok {
		if m["CourierName"] != "مندوب التوصيل" || m["Amount"] != float64(9000) || m["Status"] != "open" {
			t.Fatalf("booked debt wrong: %v", m)
		}
		if m["OrderRef"] != fmt.Sprintf("ONLINE-%d", orderID) {
			t.Fatalf("OrderRef = %v, want ONLINE-%d", m["OrderRef"], orderID)
		}
	}
	// Double confirmation is refused by the state machine and books
	// nothing more.
	code, _ = e.postJSON(admin, fmt.Sprintf("/api/staff/orders/%d/status", orderID), map[string]any{
		"status": "DELIVERED", "courier_name": "مندوب التوصيل",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("second DELIVERED: want 400, got %d", code)
	}
	if debts := fix13CourierDebtCount(t, e, admin); len(debts) != 1 {
		t.Fatalf("courier debts after 2nd confirm = %d, want still 1", len(debts))
	}
}
