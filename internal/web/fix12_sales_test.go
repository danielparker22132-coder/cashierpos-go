// Package web_test — FIX-12 lane B (sales money) HTTP tests.
//
// SW-L08: sale discount/price-edit/below-cost denials answer 403.
// SW-H09: buy price hidden from cashier and warehouse.
// SW-H05 (HTTP half): cash-customer paid validated, not forced.
package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// fix12SaleBody builds a one-line sale request for product pid.
func fix12SaleBody(pid int64, extra map[string]any) map[string]any {
	body := map[string]any{
		"sale_type":      "retail",
		"lines":          []map[string]any{{"product_id": pid, "qty": "1"}},
		"paid":           1500,
		"payment_method": "Cash",
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestFix12_SaleDenialsAre403(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "F12-L08", "منتج ل08", "15.00", "10")
	cash := e.cashierClient()
	e.openShift(cash, 10000)

	// Cashier invoice discount: permission denial -> 403, uniform text.
	code, body := e.postJSON(cash, "/api/sales/create", fix12SaleBody(pid, map[string]any{
		"invoice_discount": 100,
	}))
	if code != http.StatusForbidden || body["ok"] != false || !strings.Contains(fmt.Sprint(body["error"]), "غير مسموح") {
		t.Fatalf("cashier invoice discount: want 403 غير مسموح, got %d %v", code, body)
	}
	// Cashier per-item discount: 403.
	code, body = e.postJSON(cash, "/api/sales/create", map[string]any{
		"sale_type": "retail",
		"lines": []map[string]any{{"product_id": pid, "qty": "1", "item_discount": 100}},
		"paid": 1500, "payment_method": "Cash",
	})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier item discount: want 403, got %d %v", code, body)
	}
	// Cashier price edit: 403.
	code, body = e.postJSON(cash, "/api/sales/create", map[string]any{
		"sale_type": "retail",
		"lines": []map[string]any{{"product_id": pid, "qty": "1", "unit_price": 1600}},
		"paid": 1600, "payment_method": "Cash",
	})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier price edit: want 403, got %d %v", code, body)
	}
	// Cashier below-cost WITH the flag (forged approval): 403.
	code, body = e.postJSON(cash, "/api/sales/create", map[string]any{
		"sale_type": "retail",
		"lines": []map[string]any{{"product_id": pid, "qty": "1", "unit_price": 400}},
		"paid": 400, "payment_method": "Cash", "allow_below_cost": true,
	})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("cashier below-cost flag: want 403, got %d %v", code, body)
	}
	// FIX-17 SW2-L11: below-cost WITHOUT the flag is now an upfront denial
	// for a role that can never confirm (cashier has no approve_below_cost)
	// — the old 400 confirm prompt was a dead end for this role.
	code, body = e.postJSON(cash, "/api/sales/create", map[string]any{
		"sale_type": "retail",
		"lines": []map[string]any{{"product_id": pid, "qty": "1", "unit_price": 400}},
		"paid": 400, "payment_method": "Cash",
	})
	if code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("below-cost without flag (cashier): want 403 denial, got %d %v", code, body)
	}
	// Plain validation still answers 400: cash-customer paid below net.
	code, body = e.postJSON(cash, "/api/sales/create", fix12SaleBody(pid, map[string]any{
		"paid": 500,
	}))
	if code != http.StatusBadRequest || body["ok"] != false ||
		!strings.Contains(fmt.Sprint(body["error"]), "المبلغ المدفوع غير صحيح") {
		t.Fatalf("cash paid 500: want 400 المبلغ المدفوع غير صحيح, got %d %v", code, body)
	}
	// Cash-customer paid above net: 400, not a silent force to net.
	code, body = e.postJSON(cash, "/api/sales/create", fix12SaleBody(pid, map[string]any{
		"paid": 2000,
	}))
	if code != http.StatusBadRequest || body["ok"] != false {
		t.Fatalf("cash paid 2000: want 400, got %d %v", code, body)
	}
	// Exact-net cash sale still succeeds (200).
	code, body = e.postJSON(cash, "/api/sales/create", fix12SaleBody(pid, nil))
	expectOK(t, "cash exact-net sale", code, body)
}

func TestFix12_BuyPriceHiddenFromCashierAndWarehouse(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "F12-H09", "منتج ح09", "15.00", "10")
	// createProduct sets buy_price 5.00 (500¢); retail 15.00 (1500¢).

	// Products list page: no buy-price column for cashier/warehouse.
	cash := e.cashierClient()
	wh := e.warehouseClient()
	admin := e.adminClient()
	sup := e.supClient()

	code, html := e.get(cash, "/products")
	if code != http.StatusOK {
		t.Fatalf("cashier GET /products: want 200, got %d", code)
	}
	if strings.Contains(html, "سعر الشراء") {
		t.Fatalf("cashier products page still shows the buy-price column")
	}
	code, html = e.get(wh, "/products")
	if code != http.StatusOK {
		t.Fatalf("warehouse GET /products: want 200, got %d", code)
	}
	if strings.Contains(html, "سعر الشراء") {
		t.Fatalf("warehouse products page still shows the buy-price column")
	}
	code, html = e.get(admin, "/products")
	if code != http.StatusOK || !strings.Contains(html, "سعر الشراء") {
		t.Fatalf("admin GET /products: want 200 with buy-price column, got %d", code)
	}

	// Product JSON: no buy_price field for cashier/warehouse.
	for _, tc := range []struct {
		name string
		cl   *http.Client
		want bool
	}{
		{"cashier", cash, false},
		{"warehouse", wh, false},
		{"admin", admin, true},
		{"supervisor", sup, true},
	} {
		code, body := e.getJSON(tc.cl, fmt.Sprintf("/api/products/%d", pid))
		expectOK(t, tc.name+" get product", code, body)
		prod, ok := body["product"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no product in %v", tc.name, body)
		}
		_, hasBuy := prod["buy_price"]
		if hasBuy != tc.want {
			t.Fatalf("%s: buy_price present=%v, want %v (product=%v)", tc.name, hasBuy, tc.want, prod)
		}
		if tc.want {
			if prod["buy_price"] != float64(500) {
				t.Fatalf("%s: buy_price = %v, want 500", tc.name, prod["buy_price"])
			}
		}
	}

	// Product search API: same rule.
	code, body := e.getJSON(cash, "/api/products/search?q=F12-H09")
	expectOK(t, "cashier product search", code, body)
	if prods, ok := body["products"].([]any); !ok || len(prods) == 0 {
		t.Fatalf("cashier search: no products in %v", body)
	} else if _, hasBuy := prods[0].(map[string]any)["buy_price"]; hasBuy {
		t.Fatalf("cashier search result still carries buy_price: %v", prods[0])
	}

	// Product detail page: cashier sees a dash, admin sees the value.
	code, html = e.get(cash, fmt.Sprintf("/products/%d", pid))
	if code != http.StatusOK {
		t.Fatalf("cashier GET /products/%d: want 200, got %d", pid, code)
	}
	if !strings.Contains(html, "سعر الشراء: —") || strings.Contains(html, "سعر الشراء: 5.00") {
		t.Fatalf("cashier product detail still shows the buy price: %q", truncate(html, 400))
	}
	code, html = e.get(admin, fmt.Sprintf("/products/%d", pid))
	if code != http.StatusOK || !strings.Contains(html, "سعر الشراء: 5.00 ج.م") {
		t.Fatalf("admin product detail: want buy price 5.00, got %d", code)
	}

	// Invoice-line JSON: no buy_price for the cashier's own invoice,
	// present for the admin.
	e.openShift(cash, 10000)
	invID, _ := e.cashSale(cash, pid, "1", 1500)
	code, body = e.getJSON(cash, fmt.Sprintf("/api/sales/%d", invID))
	expectOK(t, "cashier get own invoice", code, body)
	lines, ok := body["lines"].([]any)
	if !ok || len(lines) == 0 {
		t.Fatalf("cashier invoice: no lines in %v", body)
	}
	if _, hasBuy := lines[0].(map[string]any)["buy_price"]; hasBuy {
		t.Fatalf("cashier invoice lines still carry buy_price: %v", lines[0])
	}
	code, body = e.getJSON(admin, fmt.Sprintf("/api/sales/%d", invID))
	expectOK(t, "admin get invoice", code, body)
	lines, _ = body["lines"].([]any)
	if _, hasBuy := lines[0].(map[string]any)["buy_price"]; !hasBuy {
		t.Fatalf("admin invoice lines lack buy_price: %v", lines[0])
	}
}
