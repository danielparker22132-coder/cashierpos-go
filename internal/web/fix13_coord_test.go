// Package web_test — FIX-13 coordinator additions: the purchase API
// must not leak buy prices (R13-M1 extension), and the migrate-time
// table rebuild must leave FK enforcement ON (R13-M3 extension).
package web_test

import (
	"fmt"
	"net/http"
	"testing"
)

// TestFix13Coord_PurchaseAPIBuyPriceStripped — R13-M1 extension:
// GET /api/purchases/{id} returns the same buy prices the detail page
// hides, so it strips the per-line buy price (and the line total,
// qty × price) for roles without view_profit. Distinctive price
// 12.34 × 3 = 37.02, so the unit price cannot hide inside any total.
func TestFix13Coord_PurchaseAPIBuyPriceStripped(t *testing.T) {
	e := readyEnv(t, false)
	admin := e.adminClient()

	pid := e.createProduct(admin, "F13-C1", "سكر", "15.00", "100")
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
	path := fmt.Sprintf("/api/purchases/%d", int64(purchaseID))

	linePrice := func(t *testing.T, cl *http.Client) (float64, float64) {
		t.Helper()
		code, data := e.getJSON(cl, path)
		if code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d %v", path, code, data)
		}
		purchase, _ := data["purchase"].(map[string]any)
		lines, _ := purchase["Lines"].([]any)
		if len(lines) != 1 {
			t.Fatalf("purchase lines = %d, want 1: %v", len(lines), data)
		}
		ln, _ := lines[0].(map[string]any)
		price, _ := ln["PurchasePrice"].(float64)
		total, _ := ln["Total"].(float64)
		return price, total
	}

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
		price, total := linePrice(t, tc.cl)
		if tc.want {
			if price != 1234 || total != 3702 {
				t.Fatalf("%s: want line price 1234 total 3702, got %v/%v", tc.name, price, total)
			}
		} else if price != 0 || total != 0 {
			t.Fatalf("%s: buy price leaked via API (price=%v total=%v)", tc.name, price, total)
		}
	}
}
