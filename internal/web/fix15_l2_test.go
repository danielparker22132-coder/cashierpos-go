// Package web_test — FIX-15 (R15-L2): the sale-create API must not hand
// the profit back to the creating user — profit leaks the unit buy price
// (net - profit = cost). Distinctive prices: buy 5.00 × 1, retail 20.00,
// so a leaked profit would read 1500 and give buy = 2000 - 1500 = 500.
package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cashierpos-go/internal/auth"
	"cashierpos-go/internal/store"
)

// TestFix15L2_SaleCreateProfitHidden — R15-L2: POST /api/sales/create as
// cashier returns 200 with no "profit"/"free_cost" keys (and no other
// buy-price-derivable field); admin, manager (= Admin) and supervisor
// still get profit 1500 / free_cost 0. The sale itself still records:
// the cashier invoice exists and admin re-reads it with buy_price 500.
func TestFix15L2_SaleCreateProfitHidden(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "F15-L2", "منتج ل2", "20.00", "100")
	// createProduct sets buy_price 5.00 (500¢); retail 20.00 (2000¢).

	saleBody := map[string]any{
		"sale_type":      "retail",
		"lines":          []map[string]any{{"product_id": pid, "qty": "1"}},
		"paid":           2000,
		"payment_method": "Cash",
	}

	for _, tc := range []struct {
		name      string
		cl        *http.Client
		wantMoney bool // want profit/free_cost in the create response
	}{
		{"cashier", e.cashierClient(), false},
		{"admin", e.adminClient(), true},
		{"manager", mgr, true},
		{"supervisor", e.supClient(), true},
	} {
		e.openShift(tc.cl, 10000)
		code, data := e.postJSON(tc.cl, "/api/sales/create", saleBody)
		if code != http.StatusOK || data["ok"] != true {
			t.Fatalf("%s sale-create: want 200 ok, got %d %v", tc.name, code, data)
		}
		// The sale must still record: invoice id/number present, net 2000.
		if data["invoice_id"] == nil || data["invoice_number"] == nil {
			t.Fatalf("%s: create response missing invoice id/number: %v", tc.name, data)
		}
		if data["net"] != float64(2000) || data["paid"] != float64(2000) || data["remaining"] != float64(0) {
			t.Fatalf("%s: create response money want net/paid/remaining 2000/2000/0, got %v/%v/%v",
				tc.name, data["net"], data["paid"], data["remaining"])
		}
		// No buy-price-derivable field may appear for any role.
		for _, k := range []string{"buy_price", "buy_cost", "cost"} {
			if _, ok := data[k]; ok {
				t.Fatalf("%s: create response carries buy-derivable key %q: %v", tc.name, k, data)
			}
		}
		if tc.wantMoney {
			if data["profit"] != float64(1500) {
				t.Fatalf("%s: profit want 1500, got %v", tc.name, data["profit"])
			}
			if data["free_cost"] != float64(0) {
				t.Fatalf("%s: free_cost want 0, got %v", tc.name, data["free_cost"])
			}
		} else {
			if _, ok := data["profit"]; ok {
				t.Fatalf("%s: create response leaks profit=%v (buy = 2000 - profit)", tc.name, data["profit"])
			}
			if _, ok := data["free_cost"]; ok {
				t.Fatalf("%s: create response leaks free_cost=%v", tc.name, data["free_cost"])
			}
		}
	}

	// The cashier's sale still recorded correctly: admin re-reads the
	// first invoice (number 1) and sees buy_price 500 on the line.
	admin := e.adminClient()
	code, data := e.getJSON(admin, "/api/sales/1")
	if code != http.StatusOK {
		t.Fatalf("admin GET /api/sales/1: want 200, got %d %v", code, data)
	}
	lines, _ := data["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("admin GET /api/sales/1: want 1 line, got %v", data)
	}
	ln, _ := lines[0].(map[string]any)
	if ln["buy_price"] != float64(500) || ln["total"] != float64(2000) {
		t.Fatalf("cashier invoice line want buy_price/total 500/2000, got %v/%v", ln["buy_price"], ln["total"])
	}
	// And the cashier's own re-read still strips buy_price (FIX-12).
	cash := e.cashierClient()
	code, data = e.getJSON(cash, "/api/sales/1")
	if code != http.StatusOK {
		t.Fatalf("cashier GET /api/sales/1: want 200, got %d %v", code, data)
	}
	lines, _ = data["lines"].([]any)
	ln, _ = lines[0].(map[string]any)
	if _, ok := ln["buy_price"]; ok {
		t.Fatalf("cashier re-read leaks buy_price=%v", ln["buy_price"])
	}
}

// TestFix15L2_HTMLSaleCreateNoProfit — R15-L2, HTML path: the POS form
// POSTs to /sales and redirects to the detail page, which already gates
// the profit line on canViewProfit (sale_detail.html:41). A cashier
// creating through the form lands on a page with no profit line; an
// admin creating the same way sees الربح 15.00.
func TestFix15L2_HTMLSaleCreateNoProfit(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "F15-L2H", "منتج ل2هـ", "20.00", "100")

	form := url.Values{
		"customer_id":    {"0"},
		"sale_type":      {"retail"},
		"payment_method": {"Cash"},
		"paid":           {"20.00"},
		"line_product_id": {fmt.Sprint(pid)},
		"line_qty":        {"1"},
	}
	for _, tc := range []struct {
		name      string
		cl        *http.Client
		wantLabel bool // want the الربح line on the landing detail page
	}{
		{"cashier", e.cashierClient(), false},
		{"admin", e.adminClient(), true},
	} {
		e.openShift(tc.cl, 10000)
		code, loc, _ := e.postFormLoc(tc.cl, "/sales", form)
		if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/sales/") {
			t.Fatalf("%s POST /sales: want 303 to /sales/{id}, got %d %q", tc.name, code, loc)
		}
		code, html := e.get(tc.cl, loc)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: want 200, got %d", tc.name, loc, code)
		}
		if got := strings.Contains(html, "الربح:"); got != tc.wantLabel {
			t.Fatalf("%s: detail page contains profit line = %v, want %v", tc.name, got, tc.wantLabel)
		}
		if tc.wantLabel && !strings.Contains(html, "15.00") {
			t.Fatalf("%s: detail page must show the 15.00 profit", tc.name)
		}
	}
}

// TestFix15C1_SalePreviewFreeCostHidden — FIX-15 C1 (coordinator extension
// of R15-L2): the sale preview returns free_cost alongside free_units
// (productID -> qty in milli-units), so free_cost / qty is the unit buy
// price. A buy-2-get-1 offer on a 5.00-buy / 20.00-retail product:
// previewing 2 units earns 1 free unit (free_units 1000), free_cost 500.
// The cashier preview must carry no free_cost key; the admin preview must
// show free_cost 500. The preview itself still works for the cashier
// (free unit + net 4000 visible).
func TestFix15C1_SalePreviewFreeCostHidden(t *testing.T) {
	e := readyEnv(t, false)
	mgr := e.managerClient()
	pid := e.createProduct(mgr, "F15-C1", "منتج س1", "20.00", "100")
	// createProduct sets buy_price 5.00 (500¢); retail 20.00 (2000¢).
	if _, err := e.store.CreateOffer(context.Background(), &auth.User{ID: 1, UserName: "admin", FullName: "مدير النظام", Role: "admin"}, store.OfferInput{
		Name: "اشتر 2 واحصل على 1", Type: store.OfferBuyXGetY, BuyX: 2, FreeY: 1, IsActive: true,
		Targets: []store.OfferTarget{{TargetType: "product", TargetID: pid}},
	}); err != nil {
		t.Fatalf("CreateOffer buy-2-get-1: %v", err)
	}
	body := map[string]any{
		"customer_id": 1, "sale_type": "retail", "payment_method": "Cash",
		"paid": 4000, "lines": []any{map[string]any{"product_id": pid, "qty": "2"}},
	}
	for _, tc := range []struct {
		name       string
		cl         *http.Client
		wantFree   bool // want free_cost in the preview response
	}{
		{"cashier", e.cashierClient(), false},
		{"admin", e.adminClient(), true},
	} {
		e.openShift(tc.cl, 10000)
		code, data := e.postJSON(tc.cl, "/api/sales/preview", body)
		if code != http.StatusOK || data["ok"] != true {
			t.Fatalf("%s preview: want 200 ok, got %d %v", tc.name, code, data)
		}
		// The preview itself still works: 2 paid units gross/net 4000,
		// 1 free unit earned.
		if data["gross"] != float64(4000) || data["net"] != float64(4000) {
			t.Fatalf("%s: preview gross/net want 4000/4000, got %v/%v", tc.name, data["gross"], data["net"])
		}
		free, _ := data["free_units"].(map[string]any)
		if free[fmt.Sprint(pid)] != float64(1000) {
			t.Fatalf("%s: free_units want {%d: 1000}, got %v", tc.name, pid, data["free_units"])
		}
		if tc.wantFree {
			if data["free_cost"] != float64(500) {
				t.Fatalf("%s: free_cost want 500, got %v", tc.name, data["free_cost"])
			}
		} else if _, ok := data["free_cost"]; ok {
			t.Fatalf("%s: preview leaks free_cost=%v (buy = free_cost / 1 free unit)", tc.name, data["free_cost"])
		}
	}
}
